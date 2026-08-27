package types

import (
	"context"
	"encoding"
	"fmt"
	"time"
)

var _ encoding.TextMarshaler = AtomState(0)

func (s AtomState) String() string {
	switch s {
	case AtomCreated:
		return "created"
	case AtomMounted:
		return "mounted"
	case AtomRunning:
		return "running"
	case AtomStopped:
		return "stopped"
	case AtomFailed:
		return "failed"
	default:
		return fmt.Sprintf("AtomState(%d)", s)
	}
}

func (s AtomState) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// ComponentStatus is an immutable status value returned inside an AtomStatus.
type ComponentStatus struct {
	Name    string
	Kind    DependencyKind
	Mounted bool
}

// ComponentHealth captures a single component's health snapshot.
type ComponentHealth struct {
	Name    string
	Kind    DependencyKind
	OK      bool
	Err     error
	Skipped bool // true if the component does not implement HealthChecker
}

// AtomHealthReport is the aggregate of every component's health check.
type AtomHealthReport struct {
	Name       string
	State      AtomState
	Components []ComponentHealth
}

// Health returns the aggregate health report. Components implementing
// HealthChecker are called with a 2-second per-call timeout. Components that
// do not implement HealthChecker are recorded as Skipped. From AtomCreated
// state every registered component is reported as Skipped. From
// AtomStopped / AtomFailed state every component is reported as not OK
// (ErrNotMounted), reflecting that the atom is not live.
func (a *Atom) Health(ctx context.Context) (AtomHealthReport, error) {
	if a == nil {
		return AtomHealthReport{}, ErrNilAtom
	}
	if ctx == nil {
		return AtomHealthReport{}, ErrNilContext
	}
	a.mu.RLock()
	state := a.state
	name := a.name
	all := make([]namedComponent, 0, len(a.data)+len(a.abilities))
	for n, c := range a.data {
		all = append(all, namedComponent{name: n, component: c, kind: componentData})
	}
	for n, c := range a.abilities {
		all = append(all, namedComponent{name: n, component: c, kind: componentAbility})
	}
	a.mu.RUnlock()
	sortComponents(all)

	report := AtomHealthReport{Name: name, State: state, Components: make([]ComponentHealth, 0, len(all))}
	var bad []ComponentHealth
	for _, item := range all {
		ch := ComponentHealth{Name: item.name, Kind: item.kind.dependencyKind()}
		switch state {
		case AtomCreated:
			ch.Skipped = true
		case AtomMounted, AtomRunning:
			hc, ok := item.component.(HealthChecker)
			if !ok {
				ch.Skipped = true
			} else {
				callCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				var err error
				func() {
					defer func() {
						if v := recover(); v != nil {
							err = NewComponentPanicError(item.name, "health", v)
						}
					}()
					err = hc.HealthCheck(callCtx, a)
				}()
				cancel()
				if err != nil {
					ch.Err = err
				} else {
					ch.OK = true
				}
			}
		default: // Stopped, Failed
			ch.Err = ErrNotMounted
		}
		report.Components = append(report.Components, ch)
		if !ch.OK && !ch.Skipped {
			bad = append(bad, ch)
		}
	}
	if len(bad) > 0 {
		return report, &UnhealthyError{Components: bad}
	}
	return report, nil
}

// AtomStatus is an immutable lifecycle snapshot.
type AtomStatus struct {
	Name          string
	State         AtomState
	Transitioning bool
	Components    []ComponentStatus
}

func (a *Atom) Status() AtomStatus {
	if a == nil {
		return AtomStatus{State: AtomFailed}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	mounted := make(map[string]bool, len(a.mounted))
	for _, item := range a.mounted {
		mounted[itemKey(item)] = true
	}
	items := make([]namedComponent, 0, len(a.data)+len(a.abilities))
	for n, c := range a.data {
		items = append(items, namedComponent{name: n, component: c, kind: componentData})
	}
	for n, c := range a.abilities {
		items = append(items, namedComponent{name: n, component: c, kind: componentAbility})
	}
	sortComponents(items)
	components := make([]ComponentStatus, 0, len(items))
	for _, item := range items {
		components = append(components, ComponentStatus{Name: item.name, Kind: item.kind.dependencyKind(), Mounted: mounted[itemKey(item)]})
	}
	return AtomStatus{Name: a.name, State: a.state, Transitioning: a.transitioning, Components: components}
}

// WaitState blocks until target is observed or ctx ends.
func (a *Atom) WaitState(ctx context.Context, target AtomState) error {
	if a == nil {
		return ErrNilAtom
	}
	if ctx == nil {
		return ErrNilContext
	}
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if a.State() == target {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
