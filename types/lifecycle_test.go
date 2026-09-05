package types

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// recordingAbility is a lifecycle participant used by these tests.
type recordingAbility struct {
	name         string
	checkErr     error
	mountErr     error
	unmountErr   error
	mountCalls   atomic.Int32
	unmountCalls atomic.Int32
	runReturned  atomic.Int32
	unmountDelay time.Duration
	blockUnmount chan struct{}
	holdUnmount  bool
	mu           sync.Mutex
	log          *[]string
	runner       func(ctx context.Context, atom *Atom) error
}

func (c *recordingAbility) GetName() string  { return c.name }
func (c *recordingAbility) Describe() string { return c.name + " ability" }
func (c *recordingAbility) Check(*Atom) error {
	if c.log != nil {
		*c.log = append(*c.log, "check:"+c.name)
	}
	return c.checkErr
}
func (c *recordingAbility) Mount(*Atom) error {
	c.mountCalls.Add(1)
	if c.log != nil {
		*c.log = append(*c.log, "mount:"+c.name)
	}
	return c.mountErr
}
func (c *recordingAbility) Command(*Atom, string, any) CommandOutput { return CommandOutput{} }

func (c *recordingAbility) Unmount(ctx context.Context, _ *Atom) error {
	c.unmountCalls.Add(1)
	if c.log != nil {
		*c.log = append(*c.log, "unmount:"+c.name)
	}
	if c.holdUnmount {
		<-c.blockUnmount
	}
	if c.unmountDelay > 0 {
		select {
		case <-time.After(c.unmountDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return c.unmountErr
}

func (c *recordingAbility) Run(ctx context.Context, atom *Atom) error {
	c.runReturned.Add(1)
	if c.runner != nil {
		return c.runner(ctx, atom)
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestUnmountAllReverseOrderAndState(t *testing.T) {
	a := &Atom{}
	if err := a.AddAbility(&recordingAbility{name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := a.AddAbility(&recordingAbility{name: "beta"}); err != nil {
		t.Fatal(err)
	}
	if err := a.MountAll(); err != nil {
		t.Fatal(err)
	}
	var log []string
	a.mu.RLock()
	for _, m := range a.mounted {
		switch m.name {
		case "alpha", "beta":
			if rc, ok := m.component.(*recordingAbility); ok {
				rc.log = &log
			}
		}
	}
	a.mu.RUnlock()

	if err := a.UnmountAll(context.Background(), time.Second); err != nil {
		t.Fatalf("UnmountAll err=%v", err)
	}
	if a.State() != AtomStopped {
		t.Fatalf("state=%v", a.State())
	}
	if len(log) != 2 {
		t.Fatalf("unmount log=%v", log)
	}
	if log[0] != "unmount:beta" || log[1] != "unmount:alpha" {
		t.Fatalf("unmount order wrong: %v", log)
	}
}

func TestCloseIdempotent(t *testing.T) {
	a := &Atom{}
	if err := a.AddAbility(&recordingAbility{name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := a.MountAll(); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(context.Background(), time.Second); err != nil {
		t.Fatalf("first close=%v", err)
	}
	if err := a.Close(context.Background(), time.Second); err != nil {
		t.Fatalf("second close=%v", err)
	}
	if err := a.UnmountAll(context.Background(), time.Second); err != nil {
		t.Fatalf("unmount after close=%v", err)
	}
	if a.State() != AtomStopped {
		t.Fatalf("state=%v", a.State())
	}
}

func TestCloseAtomicRejectsAfterDetached(t *testing.T) {
	a := &Atom{}
	if err := a.AddAbility(&recordingAbility{name: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := a.MountAll(); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(context.Background(), time.Second); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(context.Background(), time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestUnmountAllErrorAggregated(t *testing.T) {
	a := &Atom{}
	if err := a.AddAbility(&recordingAbility{name: "alpha", unmountErr: errors.New("alpha-unmount")}); err != nil {
		t.Fatal(err)
	}
	if err := a.AddAbility(&recordingAbility{name: "beta", unmountErr: errors.New("beta-unmount")}); err != nil {
		t.Fatal(err)
	}
	if err := a.MountAll(); err != nil {
		t.Fatal(err)
	}
	err := a.UnmountAll(context.Background(), time.Second)
	if err == nil {
		t.Fatal("expected aggregated unmount error")
	}
	if a.State() != AtomFailed {
		t.Fatalf("state=%v", a.State())
	}
}

func TestUnmountAllTimeout(t *testing.T) {
	a := &Atom{}
	if err := a.AddAbility(&recordingAbility{name: "alpha", unmountDelay: 100 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if err := a.AddAbility(&recordingAbility{name: "beta"}); err != nil {
		t.Fatal(err)
	}
	if err := a.MountAll(); err != nil {
		t.Fatal(err)
	}
	err := a.UnmountAll(context.Background(), 20*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !errors.Is(err, ErrShutdownTimeout) {
		t.Fatalf("err=%v", err)
	}
	if a.State() != AtomFailed {
		t.Fatalf("state=%v", a.State())
	}
}

func TestUnmountAllTimeoutContinuesToLaterComponents(t *testing.T) {
	// 回归: 旧实现首个组件超时即 return, 中止逆序卸载链——后续组件的
	// Unmount(如 Modbus/Serial 的 Close)永不执行。新行为: 超时只记录
	// 该组件错误, 继续卸载剩余组件。
	a := &Atom{}
	alpha := &recordingAbility{name: "alpha", holdUnmount: true, blockUnmount: make(chan struct{})}
	beta := &recordingAbility{name: "beta"}
	if err := a.AddAbility(alpha); err != nil {
		t.Fatal(err)
	}
	if err := a.AddAbility(beta); err != nil {
		t.Fatal(err)
	}
	if err := a.MountAll(); err != nil {
		t.Fatal(err)
	}
	err := a.UnmountAll(context.Background(), 10*time.Millisecond)
	if err == nil || !errors.Is(err, ErrShutdownTimeout) {
		t.Fatalf("err=%v want ErrShutdownTimeout", err)
	}
	if beta.unmountCalls.Load() != 1 {
		t.Fatalf("beta unmount calls = %d, want 1 (chain must continue after alpha timeout)", beta.unmountCalls.Load())
	}
}

func TestUnmountAllNotMounted(t *testing.T) {
	a := &Atom{}
	if err := a.UnmountAll(context.Background(), time.Second); !errors.Is(err, ErrInvalidAtomState) {
		t.Fatalf("err=%v", err)
	}
}

func TestUnmountAllNilArgs(t *testing.T) {
	var a *Atom
	if err := a.UnmountAll(context.Background(), time.Second); !errors.Is(err, ErrNilAtom) {
		t.Fatalf("nil atom err=%v", err)
	}
	a = &Atom{}
	if err := a.UnmountAll(nil, time.Second); !errors.Is(err, ErrNilContext) {
		t.Fatalf("nil ctx err=%v", err)
	}
	if err := a.UnmountAll(context.Background(), 0); !errors.Is(err, ErrInvalidShutdownTimeout) {
		t.Fatalf("zero shutdown err=%v", err)
	}
}

func TestUnmountAllReverseOrderWithPanic(t *testing.T) {
	var panicOnce sync.Once
	var normalCount atomic.Int32
	var panicCount atomic.Int32

	panicker := &componentWithUnmounter{
		name: "panicker",
		fn: func(ctx context.Context, _ *Atom) error {
			panicOnce.Do(func() {
				panicCount.Add(1)
				panic("kaboom")
			})
			return nil
		},
	}
	normal := &componentWithUnmounter{
		name: "normal",
		fn: func(ctx context.Context, _ *Atom) error {
			normalCount.Add(1)
			return nil
		},
	}

	a := &Atom{}
	if err := a.AddAbility(panicker); err != nil {
		t.Fatal(err)
	}
	if err := a.AddAbility(normal); err != nil {
		t.Fatal(err)
	}
	if err := a.MountAll(); err != nil {
		t.Fatal(err)
	}
	err := a.UnmountAll(context.Background(), 2*time.Second)
	if err == nil {
		t.Fatal("expected error from panic recovery")
	}
	if normalCount.Load() != 1 {
		t.Fatalf("expected normal component to have unmounted once, got %d", normalCount.Load())
	}
	if panicCount.Load() != 1 {
		t.Fatalf("expected panicker to have panicked once, got %d", panicCount.Load())
	}
}

// componentWithUnmounter lets a test inject a custom Unmount implementation
// without registering the ability on the atom.
type componentWithUnmounter struct {
	name string
	fn   func(ctx context.Context, atom *Atom) error
}

func (c *componentWithUnmounter) GetName() string  { return c.name }
func (c *componentWithUnmounter) Describe() string { return c.name }
func (c *componentWithUnmounter) Check(*Atom) error {
	return nil
}
func (c *componentWithUnmounter) Mount(*Atom) error { return nil }
func (c *componentWithUnmounter) Command(*Atom, string, any) CommandOutput {
	return CommandOutput{}
}
func (c *componentWithUnmounter) Unmount(ctx context.Context, atom *Atom) error {
	return c.fn(ctx, atom)
}

func TestRunAllZeroRunnersBlocksOnContext(t *testing.T) {
	a := &Atom{}
	if err := a.AddAbility(&recordingAbility{name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := a.MountAll(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- a.RunAll(ctx, time.Second)
	}()
	select {
	case <-done:
		t.Fatal("RunAll returned before cancel")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunAll err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RunAll did not return after cancel")
	}
}

func TestRunAllWithRunnerPropagatesContext(t *testing.T) {
	a := &Atom{}
	runner := &recordingAbility{
		name: "runner",
		runner: func(ctx context.Context, _ *Atom) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	if err := a.AddAbility(runner); err != nil {
		t.Fatal(err)
	}
	if err := a.MountAll(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- a.RunAll(ctx, time.Second)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunAll err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RunAll did not return")
	}
	if a.State() != AtomStopped {
		t.Fatalf("state=%v", a.State())
	}
}

// Compile-time assertion that recordingAbility satisfies Unmounter / Runner.
var (
	_ Unmounter = (*recordingAbility)(nil)
	_ Runner    = (*recordingAbility)(nil)
)

// mountedSlices returns the current private mounted bookkeeping so tests
// inside the package can assert the post-RunAll invariant.
func (a *Atom) mountedSlices() ([]namedComponent, []namedComponent) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]namedComponent(nil), a.mounted...), append([]namedComponent(nil), a.mountedAbilities...)
}

func TestRunAllClearsBookkeepingAfterNormalCompletion(t *testing.T) {
	a := &Atom{}
	if err := a.AddAbility(&recordingAbility{name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := a.AddAbility(&recordingAbility{name: "beta"}); err != nil {
		t.Fatal(err)
	}
	if err := a.MountAll(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.RunAll(ctx, time.Second) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("RunAll err=%v", err)
	}
	if a.State() != AtomStopped {
		t.Fatalf("state=%v", a.State())
	}
	mounted, mountedAbs := a.mountedSlices()
	if len(mounted) != 0 || len(mountedAbs) != 0 {
		t.Fatalf("bookkeeping not cleared: mounted=%d mountedAbilities=%d", len(mounted), len(mountedAbs))
	}
}

func TestRunAllClearsBookkeepingAfterShutdownTimeout(t *testing.T) {
	a := &Atom{}
	stuck := &recordingAbility{
		name: "stuck",
		runner: func(ctx context.Context, _ *Atom) error {
			<-make(chan struct{}) // intentionally ignores ctx
			return nil
		},
	}
	if err := a.AddAbility(stuck); err != nil {
		t.Fatal(err)
	}
	if err := a.MountAll(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.RunAll(ctx, 20*time.Millisecond) }()
	// Cancel parent ctx after RunAll has launched the runner goroutine but
	// before any "natural" completion. This drives RunAll into the
	// shutdown-timeout branch.
	time.Sleep(5 * time.Millisecond)
	cancel()
	err := <-done
	if !errors.Is(err, ErrShutdownTimeout) {
		t.Fatalf("RunAll err=%v want ErrShutdownTimeout", err)
	}
	if a.State() != AtomFailed {
		t.Fatalf("state=%v", a.State())
	}
	mounted, mountedAbs := a.mountedSlices()
	if len(mounted) != 0 || len(mountedAbs) != 0 {
		t.Fatalf("bookkeeping not cleared after shutdown timeout: mounted=%d mountedAbilities=%d", len(mounted), len(mountedAbs))
	}
}

func TestRunAllClearsBookkeepingOnRunnerError(t *testing.T) {
	sentinel := errors.New("runner explosion")
	a := &Atom{}
	if err := a.AddAbility(&recordingAbility{
		name: "boom",
		runner: func(ctx context.Context, _ *Atom) error {
			return sentinel
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.MountAll(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := a.RunAll(ctx, time.Second)
	if !errors.Is(err, sentinel) {
		t.Fatalf("RunAll err=%v want wrap of %v", err, sentinel)
	}
	if a.State() != AtomStopped {
		t.Fatalf("state=%v", a.State())
	}
	mounted, mountedAbs := a.mountedSlices()
	if len(mounted) != 0 || len(mountedAbs) != 0 {
		t.Fatalf("bookkeeping not cleared on runner error: mounted=%d mountedAbilities=%d", len(mounted), len(mountedAbs))
	}
}

func TestRunAllClearIsRaceFree(t *testing.T) {
	a := &Atom{}
	if err := a.AddAbility(&recordingAbility{
		name: "slow",
		runner: func(ctx context.Context, _ *Atom) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.MountAll(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = a.RunAll(ctx, time.Second)
		close(done)
	}()
	stop := time.After(50 * time.Millisecond)
loop:
	for {
		select {
		case <-stop:
			cancel()
			break loop
		default:
		}
		_ = a.Status()
	}
	<-done
	mounted, mountedAbs := a.mountedSlices()
	if len(mounted) != 0 || len(mountedAbs) != 0 {
		t.Fatalf("bookkeeping not cleared: mounted=%d mountedAbilities=%d", len(mounted), len(mountedAbs))
	}
}
