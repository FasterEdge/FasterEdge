package types

import (
	"context"
	"errors"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// 可观测性: 各相位事件数量与状态
//
// 断言口径是"一次相位一对事件"(start + 终态)。这与旧行为一致——observe 自
// 诞生起就是 start/终态成对; 本文件把这一口径写成可执行的契约, 防止后续改
// 动悄悄破坏计数语义。
// ---------------------------------------------------------------------------

// phaseEvents 把 recorder 的事件按 phase 归拢成 status -> 计数。
func phaseEvents(rec *EventRecorder) map[LifecyclePhase]map[EventStatus]int {
	out := map[LifecyclePhase]map[EventStatus]int{}
	for _, e := range rec.Events() {
		if out[e.Phase] == nil {
			out[e.Phase] = map[EventStatus]int{}
		}
		out[e.Phase][e.Status]++
	}
	return out
}

func TestLifecycleEmitsOneEventPairPerPhase(t *testing.T) {
	a := &Atom{}
	rec := &EventRecorder{}
	if err := a.SetEventSink(rec); err != nil {
		t.Fatal(err)
	}
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
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("RunAll: %v", err)
	}

	got := phaseEvents(rec)

	// check / mount: 2 个组件各一对 start+success。
	for _, phase := range []LifecyclePhase{PhaseCheck, PhaseMount} {
		if got[phase][EventStart] != 2 || got[phase][EventSuccess] != 2 {
			t.Fatalf("%s: start=%d success=%d want 2/2", phase, got[phase][EventStart], got[phase][EventSuccess])
		}
		if got[phase][EventFailure] != 0 {
			t.Fatalf("%s: unexpected failures=%d", phase, got[phase][EventFailure])
		}
	}
	// run: 2 个 ability 都是 Runner。注意终态是 failure 而非 success——
	// 被 cancel 的 runner 返回 context.Canceled, safeRun 把它包成
	// ComponentError, 因此 run 相位如实记 failure(并带 Err, 可 errors.Is
	// 识别)。这是既有行为, 不是缺陷: "被取消"确实不是成功跑完。
	for _, phase := range []LifecyclePhase{PhaseRun, PhaseUnmount} {
		if got[phase][EventStart] != 2 {
			t.Fatalf("%s: start=%d want 2", phase, got[phase][EventStart])
		}
		terminals := got[phase][EventSuccess] + got[phase][EventFailure]
		if terminals != 2 {
			t.Fatalf("%s: terminals=%d want 2", phase, terminals)
		}
	}
	if got[PhaseRun][EventFailure] != 2 {
		t.Fatalf("run failure=%d want 2 (canceled runners)", got[PhaseRun][EventFailure])
	}
	// 取消原因的 Err 必须可识别, 否则无法区分"失败"与"被取消"。
	for _, e := range rec.Events() {
		if e.Phase == PhaseRun && e.Status == EventFailure && !errors.Is(e.Err, context.Canceled) {
			t.Fatalf("run failure err=%v is not context.Canceled", e.Err)
		}
	}
	// unmount 在清理路径上正常完成, 应为 success。
	if got[PhaseUnmount][EventSuccess] != 2 {
		t.Fatalf("unmount success=%d want 2", got[PhaseUnmount][EventSuccess])
	}
	// 终态总数必须等于 start 总数: 不允许出现"只有 start 没有终态"的相位。
	var starts, terminals int
	for _, e := range rec.Events() {
		switch e.Status {
		case EventStart:
			starts++
		case EventSuccess, EventFailure:
			terminals++
		}
	}
	if starts != terminals {
		t.Fatalf("starts=%d terminals=%d (每个相位必须成对)", starts, terminals)
	}
}

func TestLifecycleRecordsFailureStatusPerPhase(t *testing.T) {
	a := &Atom{}
	rec := &EventRecorder{}
	if err := a.SetEventSink(rec); err != nil {
		t.Fatal(err)
	}
	checkErr := errors.New("check boom")
	mountErr := errors.New("mount boom")
	if err := a.AddAbility(&recordingAbility{name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := a.AddAbility(&recordingAbility{name: "bad", checkErr: checkErr}); err != nil {
		t.Fatal(err)
	}
	// check 失败的组件不会进入 mount, 因此 mount:success 只有 alpha 一次。
	if err := a.MountAll(); !errors.Is(err, checkErr) {
		t.Fatalf("MountAll err=%v want check boom", err)
	}
	got := phaseEvents(rec)
	if got[PhaseCheck][EventFailure] != 1 || got[PhaseCheck][EventSuccess] != 1 {
		t.Fatalf("check: success=%d failure=%d want 1/1", got[PhaseCheck][EventSuccess], got[PhaseCheck][EventFailure])
	}
	if got[PhaseMount][EventSuccess] != 0 {
		t.Fatalf("mount should not run for the failed component, got %d", got[PhaseMount][EventSuccess])
	}

	// 独立用例: mount 失败 -> rollback 卸载已挂载组件。
	a2 := &Atom{}
	rec2 := &EventRecorder{}
	if err := a2.SetEventSink(rec2); err != nil {
		t.Fatal(err)
	}
	if err := a2.AddAbility(&recordingAbility{name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := a2.AddAbility(&recordingAbility{name: "beta", mountErr: mountErr}); err != nil {
		t.Fatal(err)
	}
	if err := a2.MountAll(); !errors.Is(err, mountErr) {
		t.Fatalf("MountAll err=%v want mount boom", err)
	}
	got2 := phaseEvents(rec2)
	if got2[PhaseMount][EventFailure] != 1 || got2[PhaseMount][EventSuccess] != 1 {
		t.Fatalf("mount: success=%d failure=%d want 1/1", got2[PhaseMount][EventSuccess], got2[PhaseMount][EventFailure])
	}
	// rollback 只卸载已挂载的 alpha。
	if got2[PhaseRollback][EventSuccess] != 1 {
		t.Fatalf("rollback success=%d want 1", got2[PhaseRollback][EventSuccess])
	}
	if got2[PhaseRollback][EventFailure] != 0 {
		t.Fatalf("rollback failure=%d want 0", got2[PhaseRollback][EventFailure])
	}
}

func TestNameGateFailureIsObservable(t *testing.T) {
	// 回归: 组件在 Check 期间改名。旧实现在 verifyName 失败处直接 append
	// 错误并 continue, 不发任何事件——metrics 里该组件只有 check:success,
	// 一次真实的挂载失败在指标上完全不可见。
	a := &Atom{}
	rec := &EventRecorder{}
	metrics := NewMetricsCollector()
	if err := a.SetEventSink(NewMultiEventSink(rec, metrics)); err != nil {
		t.Fatal(err)
	}
	if err := a.AddAbility(&renamingGateAbility{name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := a.MountAll(); err == nil {
		t.Fatal("MountAll should fail when a component renames itself")
	}

	got := phaseEvents(rec)
	if got[PhaseCheck][EventFailure] != 1 {
		t.Fatalf("check failure=%d want 1 (name gate must be observable)", got[PhaseCheck][EventFailure])
	}
	// 终态事件必须携带真实错误, 否则埋点有计数无原因, 无法定位。
	var found bool
	for _, e := range rec.Events() {
		if e.Phase == PhaseCheck && e.Status == EventFailure {
			found = e.Err != nil
		}
	}
	if !found {
		t.Fatal("check failure event carries no error")
	}
	snap := metrics.Snapshot()
	if snap.Counts["check:failure"] != 1 {
		t.Fatalf("metrics check:failure=%d want 1", snap.Counts["check:failure"])
	}
}

// renamingGateAbility 在 Check 内改名, 触发 Check 之后的 verifyName 闸门。
type renamingGateAbility struct{ name string }

func (c *renamingGateAbility) GetName() string  { return c.name }
func (c *renamingGateAbility) Describe() string { return c.name }
func (c *renamingGateAbility) Check(*Atom) error {
	c.name = "renamed"
	return nil
}
func (c *renamingGateAbility) Mount(*Atom) error                        { return nil }
func (c *renamingGateAbility) Command(*Atom, string, any) CommandOutput { return CommandOutput{} }

func TestCommandDispatchIsObservable(t *testing.T) {
	a := &Atom{}
	rec := &EventRecorder{}
	if err := a.SetEventSink(rec); err != nil {
		t.Fatal(err)
	}
	if err := a.AddAbility(&recordingAbility{name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := a.MountAll(); err != nil {
		t.Fatal(err)
	}
	if out := a.Command("alpha", "noop", nil); out.Err != nil {
		t.Fatal(out.Err)
	}
	// 未注册的组件同样是一次失败的 command 相位(旧实现此处零事件)。
	if out := a.Command("missing", "noop", nil); !errors.Is(out.Err, ErrMissingDependency) {
		t.Fatalf("missing component err=%v", out.Err)
	}

	got := phaseEvents(rec)
	if got[PhaseCommand][EventSuccess] != 1 || got[PhaseCommand][EventFailure] != 1 {
		t.Fatalf("command: success=%d failure=%d want 1/1", got[PhaseCommand][EventSuccess], got[PhaseCommand][EventFailure])
	}
	// command 事件的 Command 字段必须被填上, 否则无法区分是哪个命令失败。
	var sawCommand bool
	for _, e := range rec.Events() {
		if e.Phase == PhaseCommand && e.Command == "noop" {
			sawCommand = true
		}
	}
	if !sawCommand {
		t.Fatal("command event missing Command field")
	}
}

func TestShutdownTimeoutEmitsTerminalEvent(t *testing.T) {
	// 回归: unmount 超时。旧实现超时分支只聚合 error, 不发终态事件——挂死
	// 组件在埋点里只有 start 没有终态, "卡住多久"完全不可观测。
	a := &Atom{}
	rec := &EventRecorder{}
	metrics := NewMetricsCollector()
	if err := a.SetEventSink(NewMultiEventSink(rec, metrics)); err != nil {
		t.Fatal(err)
	}
	hung := &recordingAbility{name: "hung", holdUnmount: true, blockUnmount: make(chan struct{})}
	if err := a.AddAbility(hung); err != nil {
		t.Fatal(err)
	}
	if err := a.MountAll(); err != nil {
		t.Fatal(err)
	}
	if err := a.UnmountAll(context.Background(), 20*time.Millisecond); !errors.Is(err, ErrShutdownTimeout) {
		t.Fatalf("UnmountAll err=%v want ErrShutdownTimeout", err)
	}
	close(hung.blockUnmount)

	got := phaseEvents(rec)
	if got[PhaseUnmount][EventStart] != 1 {
		t.Fatalf("unmount start=%d want 1", got[PhaseUnmount][EventStart])
	}
	// 关键断言: 超时后仍有一个终态事件, 且只应有一个(组件 goroutine 随后
	// 返回也不得再发第二个, 否则一次相位被计成两次)。
	if got[PhaseUnmount][EventFailure] != 1 {
		t.Fatalf("unmount failure=%d want 1 (timeout must produce a terminal event)", got[PhaseUnmount][EventFailure])
	}
	if got[PhaseUnmount][EventSuccess] != 0 {
		t.Fatalf("unmount success=%d want 0", got[PhaseUnmount][EventSuccess])
	}
	snap := metrics.Snapshot()
	if snap.Counts["unmount:failure"] != 1 {
		t.Fatalf("metrics unmount:failure=%d want 1", snap.Counts["unmount:failure"])
	}
	// 超时时长应当被记录, 否则耗时统计失真。
	if len(snap.Durations["unmount"]) != 1 {
		t.Fatalf("unmount samples=%d want 1", len(snap.Durations["unmount"]))
	}
}
