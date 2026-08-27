package types

import (
	"sync"
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

func (a *Atom) observe(component, command string, phase LifecyclePhase, fn func() error) error {
	start := time.Now()
	a.emit(LifecycleEvent{Component: component, Command: command, Phase: phase, Status: EventStart, StartedAt: start})
	err := fn()
	status := EventSuccess
	if err != nil {
		status = EventFailure
	}
	a.emit(LifecycleEvent{Component: component, Command: command, Phase: phase, Status: status, StartedAt: start, Duration: time.Since(start), Err: err})
	return err
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
