package types

import (
	"sync"
	"sync/atomic"
	"time"
)

// LifecyclePhase identifies an observable lifecycle operation.
type LifecyclePhase string

const (
	PhaseCheck    LifecyclePhase = "check"
	PhaseMount    LifecyclePhase = "mount"
	PhaseRun      LifecyclePhase = "run"
	PhaseUnmount  LifecyclePhase = "unmount"
	PhaseRollback LifecyclePhase = "rollback"
	PhaseCommand  LifecyclePhase = "command"
)

type EventStatus string

const (
	EventStart   EventStatus = "start"
	EventSuccess EventStatus = "success"
	EventFailure EventStatus = "failure"
)

// LifecycleEvent is an immutable value delivered to an EventSink.
type LifecycleEvent struct {
	Atom      string
	Component string
	Command   string
	Phase     LifecyclePhase
	Status    EventStatus
	StartedAt time.Time
	Duration  time.Duration
	Err       error
}

// EventSink receives lifecycle events. Implementations must return promptly.
type EventSink interface{ ObserveLifecycle(LifecycleEvent) }
type EventSinkFunc func(LifecycleEvent)

func (f EventSinkFunc) ObserveLifecycle(e LifecycleEvent) {
	if f != nil {
		f(e)
	}
}

func (a *Atom) SetEventSink(sink EventSink) error {
	if a == nil {
		return ErrNilAtom
	}
	a.mu.Lock()
	a.eventSink = sink
	a.mu.Unlock()
	return nil
}
func (a *Atom) EventSink() EventSink {
	if a == nil {
		return nil
	}
	a.mu.RLock()
	s := a.eventSink
	a.mu.RUnlock()
	return s
}

func (a *Atom) emit(e LifecycleEvent) {
	if a == nil {
		return
	}
	a.mu.RLock()
	sink, name := a.eventSink, a.name
	a.mu.RUnlock()
	if sink == nil {
		return
	}
	e.Atom = name
	func() { defer func() { _ = recover() }(); sink.ObserveLifecycle(e) }()
}

// phaseTimer 在一次相位的入口发 EventStart、在 finish 里最多发一次终态事件。
// once 是必须的: 超时路径(委托方已放弃等待, 但组件 goroutine 仍在跑)与组件
// 自己完成会竞争发放终态事件——旧实现里超时那一侧根本不发, 于是组件挂死时
// 该相位在 metrics 里只有 start 没有终态, 挂死这件事永远不可见; 而若两侧都
// 直接发, 又会把一次相位算成两次。CAS 保证"一次相位一对事件"。
type phaseTimer struct {
	atom      *Atom
	component string
	command   string
	phase     LifecyclePhase
	startedAt time.Time
	once      atomic.Bool
}

func (t *phaseTimer) finish(err error) {
	if t == nil || !t.once.CompareAndSwap(false, true) {
		return
	}
	status := EventSuccess
	if err != nil {
		status = EventFailure
	}
	t.atom.emit(LifecycleEvent{
		Component: t.component,
		Command:   t.command,
		Phase:     t.phase,
		Status:    status,
		StartedAt: t.startedAt,
		Duration:  time.Since(t.startedAt),
		Err:       err,
	})
}

// observePhased 是 observe 的计时器接管版: 调用方先用 beginPhase 发出
// EventStart(必须在启动 goroutine 之前调用, 保证 start 一定先于终态), 再把
// 返回的 timer 交给本函数; 本函数不再重复发 start。
//
// 用于超时路径(rollback / unmountWithTimeout): 这些路径必须在 select 的
// 超时分支也能补发终态事件, 而组件 goroutine 仍可能随后返回——两处竞争同一
// 个终态, 由 timer.once 串行化, 保证"一次相位一对事件"。
func (a *Atom) observePhased(t *phaseTimer, fn func() error) error {
	err := fn()
	t.finish(err)
	return err
}

// beginPhase 发出相位的 EventStart 并返回计时器。emit 本身是并发安全的,
// start 事件在这里同步发出, 因此观察者看到的 start 一定先于该相位的终态。
func (a *Atom) beginPhase(component, command string, phase LifecyclePhase) *phaseTimer {
	t := &phaseTimer{atom: a, component: component, command: command, phase: phase, startedAt: time.Now()}
	a.emit(LifecycleEvent{Component: component, Command: command, Phase: phase, Status: EventStart, StartedAt: t.startedAt})
	return t
}

// observe 是最常用的一对事件(start + 终态)封装: 相位内部无超时竞争时用它。
func (a *Atom) observe(component, command string, phase LifecyclePhase, fn func() error) error {
	return a.observePhased(a.beginPhase(component, command, phase), fn)
}

// observeGateFailure 记录"在相位入口处即被拒绝"的组件——例如组件在 Check
// 前后改名(verifyName 失败), 或命令分派时目标组件未注册。这类失败发生在
// 组件回调之外, 不经过 observe, 旧实现因此对埋点完全不可见: 挂载事务整体
// 失败, 指标里却只有该组件的 check:success, 没有失败样本。
//
// 成功时静默是刻意的: 成功路径随后由 observe 产出正常的 start/终态事件对,
// 若在闸门处也发 success 会让每个组件多出一对事件, 破坏"一次 Check 一对
// 事件"的计数语义。
func (a *Atom) observeGateFailure(component string, phase LifecyclePhase, err error) {
	if a == nil || err == nil {
		return
	}
	start := time.Now()
	a.emit(LifecycleEvent{Component: component, Phase: phase, Status: EventStart, StartedAt: start})
	a.emit(LifecycleEvent{Component: component, Phase: phase, Status: EventFailure, StartedAt: start, Duration: time.Since(start), Err: err})
}

// EventRecorder is a pure-Go, concurrency-safe event sink useful for tests and embedding.
type EventRecorder struct {
	mu     sync.RWMutex
	events []LifecycleEvent
}

func (r *EventRecorder) ObserveLifecycle(e LifecycleEvent) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
}
func (r *EventRecorder) Events() []LifecycleEvent {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]LifecycleEvent(nil), r.events...)
}

// MultiEventSink fans events out to N EventSinks. Each sink is invoked in
// its own panic-recover so one misbehaving sink cannot disrupt the rest.
// Safe for concurrent use; the contained slice is snapshotted under a read
// lock before fan-out.
type MultiEventSink struct {
	mu    sync.RWMutex
	sinks []EventSink
}

func NewMultiEventSink(sinks ...EventSink) *MultiEventSink {
	cp := append([]EventSink(nil), sinks...)
	return &MultiEventSink{sinks: cp}
}

func (m *MultiEventSink) Add(sink EventSink) {
	if m == nil || sink == nil {
		return
	}
	m.mu.Lock()
	m.sinks = append(m.sinks, sink)
	m.mu.Unlock()
}

// Sinks returns a defensive copy of the registered sinks.
func (m *MultiEventSink) Sinks() []EventSink {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]EventSink(nil), m.sinks...)
}

func (m *MultiEventSink) ObserveLifecycle(e LifecycleEvent) {
	if m == nil {
		return
	}
	m.mu.RLock()
	snapshot := append([]EventSink(nil), m.sinks...)
	m.mu.RUnlock()
	for _, s := range snapshot {
		func() {
			defer func() { _ = recover() }()
			s.ObserveLifecycle(e)
		}()
	}
}

// Compile-time assertion.
var _ EventSink = (*MultiEventSink)(nil)
