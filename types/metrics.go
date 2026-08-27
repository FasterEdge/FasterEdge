package types

import (
	"sync"
	"time"
)

// ringCapacity is the maximum number of duration samples retained per phase
// inside a MetricsCollector. Older samples are overwritten in FIFO order.
const ringCapacity = 1024

// MetricsCollector is an EventSink that aggregates lifecycle activity into
// per-phase counts and a bounded ring buffer of duration samples. It is
// safe for concurrent use.
type MetricsCollector struct {
	mu        sync.Mutex
	startedAt time.Time
	counts    map[string]uint64         // "<phase>:<status>" -> count
	durations map[string][]time.Duration // phase -> ring buffer of durations
	ringPos   map[string]int            // phase -> next write index
	ringFull  map[string]bool           // phase -> ring buffer filled at least once
}

func NewMetricsCollector() *MetricsCollector {
	return &MetricsCollector{
		startedAt: time.Now(),
		counts:    map[string]uint64{},
		durations: map[string][]time.Duration{},
		ringPos:   map[string]int{},
		ringFull:  map[string]bool{},
	}
}

// ObserveLifecycle counts terminal events and records their duration into
// the per-phase ring buffer. EventStart events are intentionally ignored
// because they have no terminal counterpart and would double-count.
func (m *MetricsCollector) ObserveLifecycle(e LifecycleEvent) {
	if m == nil {
		return
	}
	if e.Status == EventStart {
		return
	}
	key := string(e.Phase) + ":" + string(e.Status)
	m.mu.Lock()
	m.counts[key]++
	phase := string(e.Phase)
	buf := m.durations[phase]
	if len(buf) == 0 {
		buf = make([]time.Duration, ringCapacity)
		m.durations[phase] = buf
	}
	pos := m.ringPos[phase]
	if pos >= ringCapacity {
		pos = 0
		m.ringFull[phase] = true
	}
	buf[pos] = e.Duration
	m.ringPos[phase] = pos + 1
	m.mu.Unlock()
}

// MetricsSnapshot is a defensive copy of a MetricsCollector's state.
type MetricsSnapshot struct {
	StartedAt time.Time
	Counts    map[string]uint64
	Durations map[string][]time.Duration
}

// Snapshot returns a defensive copy of the current counts and durations.
// The durations slices are also copied so callers may sort or analyse them
// without affecting the collector.
func (m *MetricsCollector) Snapshot() MetricsSnapshot {
	if m == nil {
		return MetricsSnapshot{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	counts := make(map[string]uint64, len(m.counts))
	for k, v := range m.counts {
		counts[k] = v
	}
	durations := make(map[string][]time.Duration, len(m.durations))
	for phase, buf := range m.durations {
		var length int
		if m.ringFull[phase] {
			length = ringCapacity
		} else {
			length = m.ringPos[phase]
		}
		cp := make([]time.Duration, length)
		copy(cp, buf[:length])
		durations[phase] = cp
	}
	return MetricsSnapshot{StartedAt: m.startedAt, Counts: counts, Durations: durations}
}

// Compile-time assertion.
var _ EventSink = (*MetricsCollector)(nil)