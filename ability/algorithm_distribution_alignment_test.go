package ability

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/FasterEdge/FasterEdge/data"
	"github.com/FasterEdge/FasterEdge/types"
)

// delayedFakeTransport delays Upload/Download by a configurable amount and
// optionally returns an error. It also observes whether per-call Target
// reached it without mutating shared state.
type delayedFakeTransport struct {
	mu        sync.Mutex
	uploads   int
	targets   []string
	delay     time.Duration
	uploadErr error
	block     chan struct{}
}

func (d *delayedFakeTransport) closeBlock() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.block != nil {
		select {
		case <-d.block:
			// already closed
		default:
			close(d.block)
		}
	}
}

func (d *delayedFakeTransport) Upload(ctx FileTransferContext) error {
	d.mu.Lock()
	d.uploads++
	d.targets = append(d.targets, ctx.Transfer.Target)
	block := d.block
	delay := d.delay
	err := d.uploadErr
	d.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Cancel():
			return errors.New("canceled")
		}
	}
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Cancel():
			return errors.New("canceled")
		}
	}
	return err
}

func (d *delayedFakeTransport) Download(ctx FileTransferContext) error {
	d.mu.Lock()
	d.targets = append(d.targets, ctx.Transfer.Target)
	block := d.block
	delay := d.delay
	err := d.uploadErr
	d.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Cancel():
			return errors.New("canceled")
		}
	}
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Cancel():
			return errors.New("canceled")
		}
	}
	return err
}

func (d *delayedFakeTransport) Targets() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, len(d.targets))
	copy(out, d.targets)
	return out
}

// registerAlgFixtures creates an atom wired with NetMapAbility, two peer nodes,
// a FileTransferAbility, and an AlgDistAbility resolved via the registry.
// The AlgDistAbility is the same instance returned for additional assertions.
func registerAlgFixtures(t *testing.T, peers int) (*types.Atom, *AlgDistAbility, *FileTransferAbility) {
	t.Helper()
	atom := &types.Atom{}
	if err := atom.AddData(&data.BaseData{}); err != nil {
		t.Fatal(err)
	}
	if err := atom.AddData(data.NewNetMapData()); err != nil {
		t.Fatal(err)
	}
	ft := NewFileTransferAbility()
	ad := NewAlgDistAbility()
	if err := atom.AddAbility(NewNetMapAbility()); err != nil {
		t.Fatal(err)
	}
	if err := atom.AddAbility(ft); err != nil {
		t.Fatal(err)
	}
	if err := atom.AddAbility(ad); err != nil {
		t.Fatal(err)
	}
	nm, _ := atom.Ability("NetMapAbility")
	for i := 0; i < peers; i++ {
		name := peerName(i)
		addr := peerAddr(i)
		if out := nm.Command(atom, NetMapCommandRegisterPeer, NetMapRegisterPeerArgs{Name: name, Address: addr}); out.Err != nil {
			t.Fatal(out.Err)
		}
	}
	return atom, ad, ft
}

func peerName(i int) string {
	return "edge-" + string(rune('A'+i))
}
func peerAddr(i int) string {
	return "10.0.0." + string(rune('2'+i)) + ":7000"
}

// registerAlg atom variant with FileTransferAbility injected via constructor.
func registerAlgFixturesWithInjected(t *testing.T, peers int) (*types.Atom, *AlgDistAbility, *FileTransferAbility) {
	t.Helper()
	atom := &types.Atom{}
	if err := atom.AddData(&data.BaseData{}); err != nil {
		t.Fatal(err)
	}
	if err := atom.AddData(data.NewNetMapData()); err != nil {
		t.Fatal(err)
	}
	ft := NewFileTransferAbility()
	ad := NewAlgDistAbilityWithTransfer(ft)
	if err := atom.AddAbility(NewNetMapAbility()); err != nil {
		t.Fatal(err)
	}
	if err := atom.AddAbility(ft); err != nil {
		t.Fatal(err)
	}
	if err := atom.AddAbility(ad); err != nil {
		t.Fatal(err)
	}
	nm, _ := atom.Ability("NetMapAbility")
	for i := 0; i < peers; i++ {
		name := peerName(i)
		addr := peerAddr(i)
		if out := nm.Command(atom, NetMapCommandRegisterPeer, NetMapRegisterPeerArgs{Name: name, Address: addr}); out.Err != nil {
			t.Fatal(out.Err)
		}
	}
	return atom, ad, ft
}

// registerAlgFixtureMissingTransfer verifies AlgDistAbility rejects commands
// when no FileTransferAbility can be resolved.
func registerAlgFixtureMissingTransfer(t *testing.T) (*types.Atom, *AlgDistAbility) {
	t.Helper()
	atom := &types.Atom{}
	if err := atom.AddData(&data.BaseData{}); err != nil {
		t.Fatal(err)
	}
	if err := atom.AddData(data.NewNetMapData()); err != nil {
		t.Fatal(err)
	}
	if err := atom.AddAbility(NewNetMapAbility()); err != nil {
		t.Fatal(err)
	}
	ad := NewAlgDistAbility()
	if err := atom.AddAbility(ad); err != nil {
		t.Fatal(err)
	}
	nm, _ := atom.Ability("NetMapAbility")
	if out := nm.Command(atom, NetMapCommandRegisterPeer, NetMapRegisterPeerArgs{Name: "edge-A", Address: "10.0.0.2:7000"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	return atom, ad
}

func registerAlgAndPayload(t *testing.T, peers int) (*types.Atom, *AlgDistAbility, *FileTransferAbility, string) {
	t.Helper()
	atom, ad, ft := registerAlgFixtures(t, peers)
	tmp := filepath.Join(t.TempDir(), "algo.bin")
	if err := os.WriteFile(tmp, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := ad.Command(atom, AlgDistCommandRegister, AlgDistRegisterArgs{
		Name: "edge-detector", Version: "1.0.0", SourcePath: tmp,
	}); out.Err != nil {
		t.Fatal(out.Err)
	}
	return atom, ad, ft, tmp
}

// waitForJobStatus waits until the named job reaches one of the expected
// statuses, up to timeout. It polls frequently and tolerates race detector
// scheduling jitter.
func waitForJobStatus(t *testing.T, ad *AlgDistAbility, jobID string, want ...AlgDistStatus) AlgDistStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ad.mu.RLock()
		j, ok := ad.jobs[jobID]
		var status AlgDistStatus
		if ok {
			status = j.Status
		}
		ad.mu.RUnlock()
		if !ok {
			return ""
		}
		for _, w := range want {
			if status == w {
				return status
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s never reached %v", jobID, want)
	return ""
}

// TestAlgDistSharedTransferViaRegistry verifies that AlgDistAbility resolves
// the FileTransferAbility registered in atom (not a private hidden one).
func TestAlgDistSharedTransferViaRegistry(t *testing.T) {
	atom, ad, ft := registerAlgFixtures(t, 1)
	// 验证算法分发和文件传输共享 atom 中注册的同一个 FileTransferAbility 实例
	// (解析时 ad.transfer 应指向 ft)。
	if out := ad.Command(atom, AlgDistCommandRegister, AlgDistRegisterArgs{
		Name: "edge-detector", Version: "1.0.0", SourcePath: filepath.Join(t.TempDir(), "x"),
	}); out.Err != nil {
		t.Fatal(out.Err)
	}
	ad.mu.RLock()
	resolved := ad.transfer == ft
	ad.mu.RUnlock()
	if !resolved {
		t.Fatal("AlgDistAbility did not resolve to the registered FileTransferAbility")
	}
}

// TestAlgDistOwnedTransferViaConstructor verifies constructor-injected
// FileTransferAbility is honored and not overridden by the registry path.
func TestAlgDistOwnedTransferViaConstructor(t *testing.T) {
	atom, ad, ft := registerAlgFixturesWithInjected(t, 1)
	ad.mu.RLock()
	resolved := ad.transfer == ft
	ad.mu.RUnlock()
	if !resolved {
		t.Fatal("AlgDistAbility did not honor constructor-injected FileTransferAbility")
	}
	// Sanity: distribute works
	tmp := filepath.Join(t.TempDir(), "x")
	os.WriteFile(tmp, []byte("x"), 0o644)
	if out := ad.Command(atom, AlgDistCommandRegister, AlgDistRegisterArgs{
		Name: "owned", Version: "1", SourcePath: tmp,
	}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := ad.Command(atom, AlgDistCommandDistribute, AlgDistDistributeArgs{
		Name: "owned", Version: "1", Target: peerName(0),
	}); out.Err != nil {
		t.Fatal(out.Err)
	}
}

// TestAlgDistRejectsMissingTransferDependency verifies that without a
// FileTransferAbility (registered or injected) distribute returns missing
// dependency and AlgDist job is never created.
func TestAlgDistRejectsMissingTransferDependency(t *testing.T) {
	atom, ad := registerAlgFixtureMissingTransfer(t)
	tmp := filepath.Join(t.TempDir(), "x")
	os.WriteFile(tmp, []byte("x"), 0o644)
	if out := ad.Command(atom, AlgDistCommandRegister, AlgDistRegisterArgs{
		Name: "x", Version: "1", SourcePath: tmp,
	}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := ad.Command(atom, AlgDistCommandDistribute, AlgDistDistributeArgs{
		Name: "x", Version: "1", Target: "edge-A",
	}); !errors.Is(out.Err, types.ErrMissingDependency) {
		t.Fatalf("expected missing dependency, got %v", out.Err)
	}
	ad.mu.RLock()
	jobs := len(ad.jobs)
	ad.mu.RUnlock()
	if jobs != 0 {
		t.Fatalf("expected zero jobs, got %d", jobs)
	}
}

// TestAlgDistDelayedSuccess verifies AlgDistJob transitions to completed
// after a delayed FileTransfer success.
func TestAlgDistDelayedSuccess(t *testing.T) {
	atom, ad, ft, _ := registerAlgAndPayload(t, 1)
	transport := &delayedFakeTransport{delay: 200 * time.Millisecond}
	ft.SetTransport(transport)

	distOut := ad.Command(atom, AlgDistCommandDistribute, AlgDistDistributeArgs{
		Name: "edge-detector", Version: "1.0.0", Target: peerName(0),
	})
	if distOut.Err != nil {
		t.Fatal(distOut.Err)
	}
	jobID := distOut.Value.(string)
	// 骨架模式下 transfer 立即完成(无 transport);但是我们注入了 transport,
	// 所以 transfer 会异步完成,AlgDistJob 也需要异步对齐。
	status := waitForJobStatus(t, ad, jobID, AlgDistStatusCompleted)
	if status != AlgDistStatusCompleted {
		t.Fatalf("status = %s, want completed", status)
	}
	ad.mu.RLock()
	j := ad.jobs[jobID]
	ad.mu.RUnlock()
	if j.Error != "" {
		t.Fatalf("unexpected error: %q", j.Error)
	}
	if j.FinishedAt.IsZero() {
		t.Fatal("FinishedAt not set")
	}
}

// TestAlgDistDelayedFailure verifies AlgDistJob transitions to failed after
// a delayed FileTransfer failure and propagates the error message.
func TestAlgDistDelayedFailure(t *testing.T) {
	atom, ad, ft, _ := registerAlgAndPayload(t, 1)
	transport := &delayedFakeTransport{
		delay:     150 * time.Millisecond,
		uploadErr: errors.New("connection refused"),
	}
	ft.SetTransport(transport)

	distOut := ad.Command(atom, AlgDistCommandDistribute, AlgDistDistributeArgs{
		Name: "edge-detector", Version: "1.0.0", Target: peerName(0),
	})
	if distOut.Err != nil {
		t.Fatal(distOut.Err)
	}
	jobID := distOut.Value.(string)
	status := waitForJobStatus(t, ad, jobID, AlgDistStatusFailed)
	if status != AlgDistStatusFailed {
		t.Fatalf("status = %s, want failed", status)
	}
	ad.mu.RLock()
	j := ad.jobs[jobID]
	ad.mu.RUnlock()
	if j.Error == "" {
		t.Fatal("expected error message set on failure")
	}
	if j.FinishedAt.IsZero() {
		t.Fatal("FinishedAt not set")
	}
}

// TestAlgDistCancelPendingTransport verifies cancel propagates to the
// FileTransfer and the AlgDistJob reaches canceled state.
func TestAlgDistCancelPendingTransport(t *testing.T) {
	atom, ad, ft, _ := registerAlgAndPayload(t, 1)
	transport := &delayedFakeTransport{
		delay: 1 * time.Second,
		block: make(chan struct{}),
	}
	ft.SetTransport(transport)

	distOut := ad.Command(atom, AlgDistCommandDistribute, AlgDistDistributeArgs{
		Name: "edge-detector", Version: "1.0.0", Target: peerName(0),
	})
	if distOut.Err != nil {
		t.Fatal(distOut.Err)
	}
	jobID := distOut.Value.(string)

	if out := ad.Command(atom, AlgDistCommandCancel, AlgDistIDArg{ID: jobID}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// 解除 transport 阻塞,让其返回错误(模仿收到取消信号)
	transport.closeBlock()
	status := waitForJobStatus(t, ad, jobID, AlgDistStatusCanceled, AlgDistStatusFailed)
	// 取消信号优先保持 canceled 状态(参考 FileTransferAbility.run 的语义)
	if status != AlgDistStatusCanceled {
		t.Fatalf("status = %s, want canceled", status)
	}
}

// TestAlgDistMidDistributeUnmount verifies that an Unmount on the
// FileTransferAbility cancels in-flight transfers and the AlgDistJob
// observes the canceled state.
func TestAlgDistMidDistributeUnmount(t *testing.T) {
	atom, ad, ft, _ := registerAlgAndPayload(t, 1)
	transport := &delayedFakeTransport{
		delay: 2 * time.Second,
		block: make(chan struct{}),
	}
	ft.SetTransport(transport)

	distOut := ad.Command(atom, AlgDistCommandDistribute, AlgDistDistributeArgs{
		Name: "edge-detector", Version: "1.0.0", Target: peerName(0),
	})
	if distOut.Err != nil {
		t.Fatal(distOut.Err)
	}
	jobID := distOut.Value.(string)

	// 等 transport worker 进入阻塞
	time.Sleep(50 * time.Millisecond)
	if err := ft.Unmount(context.Background(), atom); err != nil {
		t.Fatalf("Unmount err = %v", err)
	}
	// transport 已经因为 cancel 通道关闭而退出;block 通道可能从未被消费,
	// 这里安全释放,避免泄漏。
	transport.closeBlock()
	// AlgDistAbility 也会被卸载,等待 watcher 退出
	if err := ad.Unmount(context.Background(), atom); err != nil {
		t.Fatalf("Ad Unmount err = %v", err)
	}
	// 此时应当观察到 canceled 或 failed(若 transport 完成太快,会变 failed)
	ad.mu.RLock()
	j := ad.jobs[jobID]
	ad.mu.RUnlock()
	if j == nil {
		t.Fatal("job gone after Unmount")
	}
	if j.Status != AlgDistStatusCanceled && j.Status != AlgDistStatusFailed {
		t.Fatalf("unexpected final status %q", j.Status)
	}
}

// TestAlgDistConcurrentDifferentTargets verifies concurrent distribution to
// different targets does not race on shared state and each job targets the
// right peer.
func TestAlgDistConcurrentDifferentTargets(t *testing.T) {
	atom, ad, ft, _ := registerAlgAndPayload(t, 4)
	transport := &delayedFakeTransport{delay: 100 * time.Millisecond}
	ft.SetTransport(transport)

	const n = 4
	jobIDs := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			out := ad.Command(atom, AlgDistCommandDistribute, AlgDistDistributeArgs{
				Name: "edge-detector", Version: "1.0.0", Target: peerName(i),
			})
			if out.Err != nil {
				t.Errorf("distribute %d err = %v", i, out.Err)
				return
			}
			jobIDs[i] = out.Value.(string)
		}()
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		if jobIDs[i] == "" {
			t.Fatalf("job %d id empty", i)
		}
		waitForJobStatus(t, ad, jobIDs[i], AlgDistStatusCompleted)
	}

	targets := transport.Targets()
	if len(targets) != n {
		t.Fatalf("transport received %d uploads, want %d (targets=%v)", len(targets), n, targets)
	}
	// 每个目标各被投递一次,顺序不固定但应一一对应
	got := make(map[string]int)
	for _, tgt := range targets {
		got[tgt]++
	}
	for i := 0; i < n; i++ {
		if got[peerName(i)] != 1 {
			t.Fatalf("peer %s received %d uploads, want 1", peerName(i), got[peerName(i)])
		}
	}
	// 共享 target 字段不应被并发修改
	ad.mu.RLock()
	// 不直接看 a.target,FileTransferAbility 的共享 target 应该保持空(从未调用 set_target)
	ad.mu.RUnlock()
	ft.mu.RLock()
	shared := ft.target
	ft.mu.RUnlock()
	if shared != "" {
		t.Fatalf("FileTransferAbility.target was mutated to %q; per-call target must not mutate shared state", shared)
	}
}

// TestAlgDistInvalidPerCallTargetRejected verifies per-call target
// validation rejects unknown peers without mutating shared state.
func TestAlgDistInvalidPerCallTargetRejected(t *testing.T) {
	atom, ad, _, _ := registerAlgAndPayload(t, 1)
	out := ad.Command(atom, AlgDistCommandDistribute, AlgDistDistributeArgs{
		Name: "edge-detector", Version: "1.0.0", Target: "ghost",
	})
	if out.Err == nil {
		t.Fatal("expected error for unknown target")
	}
	// 只断非 nil 不断哨兵会让实现误返回 ErrMissingDependency/ErrOperationFailed
	// 时测试照样 PASS——未知目标属参数校验, 必须命中 ErrInvalidArguments。
	if !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("unknown target error = %v, want ErrInvalidArguments", out.Err)
	}
}
