package types

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Step 3 — RemoveData / RemoveAbility
// ---------------------------------------------------------------------------

func TestRemoveDataBeforeMount(t *testing.T) {
	a := &Atom{}
	if err := a.AddData(&testComponent{name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := a.AddData(&testComponent{name: "beta"}); err != nil {
		t.Fatal(err)
	}
	if err := a.RemoveData("alpha"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, ok := a.Data("alpha"); ok {
		t.Fatal("alpha still present")
	}
	if _, ok := a.Data("beta"); !ok {
		t.Fatal("beta missing")
	}
}

func TestRemoveAbilityBeforeMount(t *testing.T) {
	a := &Atom{}
	if err := a.AddAbility(&testComponent{name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := a.RemoveAbility("alpha"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, ok := a.Ability("alpha"); ok {
		t.Fatal("alpha still present")
	}
}

func TestRemoveAfterUnmountRoundTrip(t *testing.T) {
	a := &Atom{}
	if err := a.AddData(&testComponent{name: "d1"}); err != nil {
		t.Fatal(err)
	}
	if err := a.AddAbility(&recordingAbility{name: "a1"}); err != nil {
		t.Fatal(err)
	}
	if err := a.MountAll(); err != nil {
		t.Fatal(err)
	}
	if err := a.UnmountAll(context.Background(), time.Second); err != nil {
		t.Fatal(err)
	}
	if err := a.RemoveData("d1"); err != nil {
		t.Fatalf("remove data after unmount: %v", err)
	}
	if err := a.RemoveAbility("a1"); err != nil {
		t.Fatalf("remove ability after unmount: %v", err)
	}
	if _, ok := a.Data("d1"); ok {
		t.Fatal("d1 still present")
	}
}

func TestRemoveRejectsMissing(t *testing.T) {
	a := &Atom{}
	if err := a.RemoveData("nope"); !errors.Is(err, ErrMissingDependency) {
		t.Fatalf("missing data: %v", err)
	}
	if err := a.RemoveAbility("nope"); !errors.Is(err, ErrMissingDependency) {
		t.Fatalf("missing ability: %v", err)
	}
}

func TestRemoveRejectsMounted(t *testing.T) {
	a := &Atom{}
	if err := a.AddData(&testComponent{name: "d1"}); err != nil {
		t.Fatal(err)
	}
	if err := a.AddAbility(&recordingAbility{name: "a1"}); err != nil {
		t.Fatal(err)
	}
	if err := a.MountAll(); err != nil {
		t.Fatal(err)
	}
	if err := a.RemoveData("d1"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("remove data while mounted: %v", err)
	}
	if err := a.RemoveAbility("a1"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("remove ability while mounted: %v", err)
	}
}

func TestRemoveRejectsInvalidName(t *testing.T) {
	a := &Atom{}
	if err := a.RemoveData(""); !errors.Is(err, ErrInvalidComponentName) {
		t.Fatalf("empty: %v", err)
	}
	if err := a.RemoveData(" worker "); !errors.Is(err, ErrInvalidComponentName) {
		t.Fatalf("trim: %v", err)
	}
	if err := a.RemoveAbility(" worker "); !errors.Is(err, ErrInvalidComponentName) {
		t.Fatalf("ability trim: %v", err)
	}
}

func TestRemoveOnNilAtom(t *testing.T) {
	var a *Atom
	if err := a.RemoveData("x"); !errors.Is(err, ErrNilAtom) {
		t.Fatalf("data: %v", err)
	}
	if err := a.RemoveAbility("x"); !errors.Is(err, ErrNilAtom) {
		t.Fatalf("ability: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Step 2 — Atom.Reset()
// ---------------------------------------------------------------------------

func TestResetClearsMountedAndPreservesRegistrations(t *testing.T) {
	a := &Atom{}
	if err := a.AddData(&testComponent{name: "d1"}); err != nil {
		t.Fatal(err)
	}
	if err := a.AddAbility(&recordingAbility{name: "a1"}); err != nil {
		t.Fatal(err)
	}
	if err := a.MountAll(); err != nil {
		t.Fatal(err)
	}
	if err := a.UnmountAll(context.Background(), time.Second); err != nil {
		t.Fatal(err)
	}
	if err := a.Reset(); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if a.State() != AtomCreated {
		t.Fatalf("state=%v", a.State())
	}
	if _, ok := a.Data("d1"); !ok {
		t.Fatal("d1 missing after reset")
	}
	if _, ok := a.Ability("a1"); !ok {
		t.Fatal("a1 missing after reset")
	}
	mounted, mountedAbs := a.mountedSlices()
	if len(mounted) != 0 || len(mountedAbs) != 0 {
		t.Fatalf("bookkeeping not cleared: mounted=%d mountedAbilities=%d", len(mounted), len(mountedAbs))
	}
	// Second mount cycle must work.
	if err := a.MountAll(); err != nil {
		t.Fatalf("second mount: %v", err)
	}
}

func TestResetRejectsRunning(t *testing.T) {
	a := &Atom{}
	stuck := &recordingAbility{
		name: "stuck",
		runner: func(ctx context.Context, _ *Atom) error {
			<-make(chan struct{})
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
	done := make(chan struct{})
	go func() {
		_ = a.RunAll(ctx, time.Second)
		close(done)
	}()
	time.Sleep(10 * time.Millisecond)
	if err := a.Reset(); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("reset while running: %v", err)
	}
	cancel()
	<-done
}

func TestResetAfterFailedRunAll(t *testing.T) {
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
	if err := a.AddData(&testComponent{name: "d1"}); err != nil {
		t.Fatal(err)
	}
	if err := a.MountAll(); err != nil {
		t.Fatal(err)
	}
	if err := a.RunAll(context.Background(), time.Second); !errors.Is(err, sentinel) {
		t.Fatalf("RunAll err=%v", err)
	}
	if a.State() != AtomStopped {
		t.Fatalf("state after failed run=%v", a.State())
	}
	if err := a.Reset(); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if a.State() != AtomCreated {
		t.Fatalf("state after reset=%v", a.State())
	}
	if err := a.MountAll(); err != nil {
		t.Fatalf("second mount: %v", err)
	}
}

func TestResetNilAtom(t *testing.T) {
	var a *Atom
	if err := a.Reset(); !errors.Is(err, ErrNilAtom) {
		t.Fatalf("err=%v", err)
	}
}

func TestResetRejectsCreatedAndMounted(t *testing.T) {
	a := &Atom{}
	if err := a.Reset(); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("reset on created: %v", err)
	}
	if err := a.AddData(&testComponent{name: "d1"}); err != nil {
		t.Fatal(err)
	}
	if err := a.MountAll(); err != nil {
		t.Fatal(err)
	}
	if err := a.Reset(); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("reset on mounted: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Step 5 — CommandLister + Atom.CommandNames
// ---------------------------------------------------------------------------

type commandListingComponent struct {
	name     string
	commands []string
}

func (c *commandListingComponent) GetName() string                          { return c.name }
func (c *commandListingComponent) Describe() string                         { return "" }
func (c *commandListingComponent) Check(*Atom) error                        { return nil }
func (c *commandListingComponent) Mount(*Atom) error                        { return nil }
func (c *commandListingComponent) Command(*Atom, string, any) CommandOutput { return CommandOutput{} }
func (c *commandListingComponent) ListCommands() []string {
	out := append([]string(nil), c.commands...)
	sort.Strings(out)
	return out
}

type nonListingComponent struct{ name string }

func (c *nonListingComponent) GetName() string                          { return c.name }
func (c *nonListingComponent) Describe() string                         { return "" }
func (c *nonListingComponent) Check(*Atom) error                        { return nil }
func (c *nonListingComponent) Mount(*Atom) error                        { return nil }
func (c *nonListingComponent) Command(*Atom, string, any) CommandOutput { return CommandOutput{} }

func TestCommandNamesMixOfListersAndNonListers(t *testing.T) {
	a := &Atom{}
	if err := a.AddData(&commandListingComponent{name: "l1", commands: []string{"foo", "bar"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.AddAbility(&commandListingComponent{name: "l2", commands: []string{"baz"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.AddAbility(&nonListingComponent{name: "plain"}); err != nil {
		t.Fatal(err)
	}
	names := a.CommandNames()
	if got := names["l1"]; len(got) != 2 || got[0] != "bar" || got[1] != "foo" {
		t.Fatalf("l1=%v", got)
	}
	if got := names["l2"]; len(got) != 1 || got[0] != "baz" {
		t.Fatalf("l2=%v", got)
	}
	if got, ok := names["plain"]; !ok {
		t.Fatalf("plain missing from map")
	} else if len(got) != 0 {
		t.Fatalf("plain=%v want empty", got)
	}
}

func TestCommandNamesNilAtom(t *testing.T) {
	var a *Atom
	if got := a.CommandNames(); got == nil {
		// Either nil map or empty map is acceptable; the contract is "iterable".
		t.Fatal("CommandNames on nil atom must not panic")
	}
}

func TestCommandNamesEmptySliceIsEmptyNotNil(t *testing.T) {
	a := &Atom{}
	if err := a.AddAbility(&commandListingComponent{name: "empty", commands: nil}); err != nil {
		t.Fatal(err)
	}
	got := a.CommandNames()
	if _, ok := got["empty"]; !ok {
		t.Fatal("empty key missing")
	}
	if len(got["empty"]) != 0 {
		t.Fatalf("empty=%v want len 0", got["empty"])
	}
}

// ---------------------------------------------------------------------------
// Step 7 — MultiEventSink + Atom.AddEventSink
// ---------------------------------------------------------------------------

func TestMultiEventSinkFansOut(t *testing.T) {
	r1 := &EventRecorder{}
	r2 := &EventRecorder{}
	m := NewMultiEventSink(r1, r2)
	m.ObserveLifecycle(LifecycleEvent{Component: "x", Phase: PhaseMount, Status: EventSuccess})
	if len(r1.Events()) != 1 || len(r2.Events()) != 1 {
		t.Fatalf("r1=%d r2=%d", len(r1.Events()), len(r2.Events()))
	}
}

func TestMultiEventSinkRecoversFromPanic(t *testing.T) {
	bad := EventSinkFunc(func(LifecycleEvent) { panic("boom") })
	good := &EventRecorder{}
	m := NewMultiEventSink(bad, good)
	m.ObserveLifecycle(LifecycleEvent{Component: "x"})
	if len(good.Events()) != 1 {
		t.Fatalf("good sink missed event: %d", len(good.Events()))
	}
}

type addOnlySink struct {
	count atomic.Int32
}

func (s *addOnlySink) ObserveLifecycle(LifecycleEvent) { s.count.Add(1) }

func TestAddEventSinkComposesWithExisting(t *testing.T) {
	a := &Atom{}
	if err := a.AddAbility(&recordingAbility{name: "x"}); err != nil {
		t.Fatal(err)
	}
	first := &addOnlySink{}
	second := &addOnlySink{}
	third := &addOnlySink{}
	if err := a.SetEventSink(first); err != nil {
		t.Fatal(err)
	}
	if err := a.AddEventSink(second); err != nil {
		t.Fatal(err)
	}
	if err := a.AddEventSink(third); err != nil {
		t.Fatal(err)
	}
	a.Command("x", "noop", nil) // emits a start + success event through EventSink
	if first.count.Load() != 2 {
		t.Fatalf("first=%d", first.count.Load())
	}
	if second.count.Load() != 2 {
		t.Fatalf("second=%d", second.count.Load())
	}
	if third.count.Load() != 2 {
		t.Fatalf("third=%d", third.count.Load())
	}
}

func TestAddEventSinkNilAtom(t *testing.T) {
	var a *Atom
	if err := a.AddEventSink(&addOnlySink{}); !errors.Is(err, ErrNilAtom) {
		t.Fatalf("err=%v", err)
	}
}

// ---------------------------------------------------------------------------
// Step 8 — MetricsCollector
// ---------------------------------------------------------------------------

func TestMetricsCollectorCountsAndTimings(t *testing.T) {
	m := NewMetricsCollector()
	for i := 0; i < 10; i++ {
		m.ObserveLifecycle(LifecycleEvent{Phase: PhaseMount, Status: EventSuccess, Duration: 5 * time.Millisecond})
	}
	m.ObserveLifecycle(LifecycleEvent{Phase: PhaseMount, Status: EventFailure, Duration: 10 * time.Millisecond})
	snap := m.Snapshot()
	if snap.Counts["mount:success"] != 10 {
		t.Fatalf("mount:success=%d", snap.Counts["mount:success"])
	}
	if snap.Counts["mount:failure"] != 1 {
		t.Fatalf("mount:failure=%d", snap.Counts["mount:failure"])
	}
	if len(snap.Durations["mount"]) != 11 {
		t.Fatalf("durations len=%d", len(snap.Durations["mount"]))
	}
}

func TestMetricsCollectorIgnoresStarts(t *testing.T) {
	m := NewMetricsCollector()
	m.ObserveLifecycle(LifecycleEvent{Phase: PhaseMount, Status: EventStart})
	snap := m.Snapshot()
	for k, v := range snap.Counts {
		if v != 0 {
			t.Fatalf("nonzero count after EventStart: %s=%d", k, v)
		}
	}
}

func TestMetricsCollectorSnapshotIsCopy(t *testing.T) {
	m := NewMetricsCollector()
	m.ObserveLifecycle(LifecycleEvent{Phase: PhaseMount, Status: EventSuccess, Duration: time.Millisecond})
	snap := m.Snapshot()
	delete(snap.Counts, "mount:success")
	if m.Snapshot().Counts["mount:success"] != 1 {
		t.Fatal("snapshot leaked internal map")
	}
}

func TestMetricsCollectorRingBufferWraps(t *testing.T) {
	m := NewMetricsCollector()
	// Push more than ringCapacity to force wrap.
	for i := 0; i < ringCapacity+5; i++ {
		m.ObserveLifecycle(LifecycleEvent{Phase: PhaseRun, Status: EventSuccess, Duration: time.Duration(i) * time.Microsecond})
	}
	snap := m.Snapshot()
	if len(snap.Durations["run"]) != ringCapacity {
		t.Fatalf("ring len=%d want %d", len(snap.Durations["run"]), ringCapacity)
	}
}

// ---------------------------------------------------------------------------
// Step 6 — HealthChecker + Atom.Health
// ---------------------------------------------------------------------------

type healthyComponent struct{ name string }

func (c *healthyComponent) GetName() string                          { return c.name }
func (c *healthyComponent) Describe() string                         { return "" }
func (c *healthyComponent) Check(*Atom) error                        { return nil }
func (c *healthyComponent) Mount(*Atom) error                        { return nil }
func (c *healthyComponent) Command(*Atom, string, any) CommandOutput { return CommandOutput{} }
func (c *healthyComponent) HealthCheck(_ context.Context, _ *Atom) error {
	return nil
}

type sickComponent struct {
	name string
	err  error
}

func (c *sickComponent) GetName() string                          { return c.name }
func (c *sickComponent) Describe() string                         { return "" }
func (c *sickComponent) Check(*Atom) error                        { return nil }
func (c *sickComponent) Mount(*Atom) error                        { return nil }
func (c *sickComponent) Command(*Atom, string, any) CommandOutput { return CommandOutput{} }
func (c *sickComponent) HealthCheck(_ context.Context, _ *Atom) error {
	return c.err
}

func TestHealthAllOK(t *testing.T) {
	a := &Atom{}
	if err := a.AddAbility(&healthyComponent{name: "h1"}); err != nil {
		t.Fatal(err)
	}
	if err := a.MountAll(); err != nil {
		t.Fatal(err)
	}
	report, err := a.Health(context.Background())
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(report.Components) != 1 || !report.Components[0].OK {
		t.Fatalf("report=%+v", report)
	}
}

func TestHealthPartialFailure(t *testing.T) {
	sentinel := errors.New("sick")
	a := &Atom{}
	if err := a.AddAbility(&healthyComponent{name: "h1"}); err != nil {
		t.Fatal(err)
	}
	if err := a.AddAbility(&sickComponent{name: "s1", err: sentinel}); err != nil {
		t.Fatal(err)
	}
	if err := a.MountAll(); err != nil {
		t.Fatal(err)
	}
	report, err := a.Health(context.Background())
	if !errors.Is(err, sentinel) {
		t.Fatalf("err=%v", err)
	}
	ok, bad := 0, 0
	for _, c := range report.Components {
		if c.OK {
			ok++
		} else {
			bad++
		}
	}
	if ok != 1 || bad != 1 {
		t.Fatalf("ok=%d bad=%d", ok, bad)
	}
}

func TestHealthSkipsNonCheckers(t *testing.T) {
	a := &Atom{}
	if err := a.AddAbility(&healthyComponent{name: "h1"}); err != nil {
		t.Fatal(err)
	}
	if err := a.AddAbility(&recordingAbility{name: "n1"}); err != nil {
		t.Fatal(err)
	}
	if err := a.MountAll(); err != nil {
		t.Fatal(err)
	}
	report, err := a.Health(context.Background())
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	for _, c := range report.Components {
		if c.Name == "n1" && !c.Skipped {
			t.Fatalf("n1 should be skipped: %+v", c)
		}
	}
}

func TestHealthRespectsStateAfterUnmount(t *testing.T) {
	a := &Atom{}
	if err := a.AddAbility(&healthyComponent{name: "h1"}); err != nil {
		t.Fatal(err)
	}
	if err := a.MountAll(); err != nil {
		t.Fatal(err)
	}
	if err := a.UnmountAll(context.Background(), time.Second); err != nil {
		t.Fatal(err)
	}
	report, err := a.Health(context.Background())
	if !errors.Is(err, ErrNotMounted) {
		t.Fatalf("err=%v", err)
	}
	if report.Components[0].OK {
		t.Fatal("stopped component reported OK")
	}
}

func TestHealthNilAtom(t *testing.T) {
	var a *Atom
	if _, err := a.Health(context.Background()); !errors.Is(err, ErrNilAtom) {
		t.Fatalf("err=%v", err)
	}
}

func TestHealthNilContext(t *testing.T) {
	a := &Atom{}
	if _, err := a.Health(nil); !errors.Is(err, ErrNilContext) {
		t.Fatalf("err=%v", err)
	}
}

// ---------------------------------------------------------------------------
// sanity: race-free concurrent SetEventSink + observe
// ---------------------------------------------------------------------------

func TestSetEventSinkAndConcurrentObserveIsRaceFree(t *testing.T) {
	a := &Atom{}
	if err := a.AddAbility(&recordingAbility{name: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := a.MountAll(); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = a.SetEventSink(&EventRecorder{})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			a.Command("x", "noop", nil)
		}
	}()
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(stop)
	}()
	wg.Wait()
}