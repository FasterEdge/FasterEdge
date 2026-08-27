package types

import (
	"fmt"
	"reflect"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
)

type AtomState uint8

const (
	AtomCreated AtomState = iota
	AtomMounted
	AtomRunning
	AtomStopped
	AtomFailed
)

type Atom struct {
	mu               sync.RWMutex
	name             string
	data             map[string]Data
	abilities        map[string]Ability
	state            AtomState
	mounted          []namedComponent
	mountedAbilities []namedComponent
	transitioning    bool
	eventSink        EventSink
}

func (a *Atom) GetName() string {
	if a == nil {
		return ""
	}
	a.mu.RLock()
	n := a.name
	a.mu.RUnlock()
	return n
}
func (a *Atom) SetName(name string) error {
	if a == nil {
		return ErrNilAtom
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state != AtomCreated || a.transitioning {
		return fmt.Errorf("set atom name: %w", ErrInvalidState)
	}
	if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
		return fmt.Errorf("set atom name: %w", ErrInvalidComponentName)
	}
	a.name = name
	return nil
}
func (a *Atom) State() AtomState {
	if a == nil {
		return AtomFailed
	}
	a.mu.RLock()
	s := a.state
	a.mu.RUnlock()
	return s
}
func (a *Atom) AddData(d Data) error {
	if a == nil {
		return ErrNilAtom
	}
	return a.addData(d)
}
func (a *Atom) addData(d Data) error {
	name, err := validateComponent(d)
	if err != nil {
		return fmt.Errorf("add data: %w", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state != AtomCreated || a.transitioning {
		return fmt.Errorf("add data %s: %w", name, ErrInvalidState)
	}
	if a.data == nil {
		a.data = make(map[string]Data)
	}
	if _, ok := a.data[name]; ok {
		return fmt.Errorf("add data %s: %w", name, ErrDuplicateComponent)
	}
	a.data[name] = d
	return nil
}
func (a *Atom) AddAbility(ab Ability) error {
	if a == nil {
		return ErrNilAtom
	}
	name, err := validateComponent(ab)
	if err != nil {
		return fmt.Errorf("add ability: %w", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state != AtomCreated {
		return fmt.Errorf("add ability %s: %w", name, ErrInvalidState)
	}
	if a.abilities == nil {
		a.abilities = make(map[string]Ability)
	}
	if _, ok := a.abilities[name]; ok {
		return fmt.Errorf("add ability %s: %w", name, ErrDuplicateComponent)
	}
	a.abilities[name] = ab
	return nil
}

// RemoveData deletes a registered Data component. The atom must be in
// AtomCreated, AtomStopped, or AtomFailed state (not mounted, not running).
// Returns ErrMissingDependency if the name is not currently registered.
func (a *Atom) RemoveData(name string) error {
	if a == nil {
		return ErrNilAtom
	}
	if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
		return fmt.Errorf("remove data %q: %w", name, ErrInvalidComponentName)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.transitioning {
		return fmt.Errorf("remove data %s: %w", name, ErrInvalidState)
	}
	if a.state != AtomCreated && a.state != AtomStopped && a.state != AtomFailed {
		return fmt.Errorf("remove data %s: %w", name, ErrInvalidState)
	}
	if _, ok := a.data[name]; !ok {
		return fmt.Errorf("remove data %s: %w", name, ErrMissingDependency)
	}
	delete(a.data, name)
	return nil
}

// RemoveAbility deletes a registered Ability component. The atom must be in
// AtomCreated, AtomStopped, or AtomFailed state. Returns ErrMissingDependency
// if the name is not currently registered.
func (a *Atom) RemoveAbility(name string) error {
	if a == nil {
		return ErrNilAtom
	}
	if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
		return fmt.Errorf("remove ability %q: %w", name, ErrInvalidComponentName)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.transitioning {
		return fmt.Errorf("remove ability %s: %w", name, ErrInvalidState)
	}
	if a.state != AtomCreated && a.state != AtomStopped && a.state != AtomFailed {
		return fmt.Errorf("remove ability %s: %w", name, ErrInvalidState)
	}
	if _, ok := a.abilities[name]; !ok {
		return fmt.Errorf("remove ability %s: %w", name, ErrMissingDependency)
	}
	delete(a.abilities, name)
	return nil
}
func (a *Atom) Data(name string) (Data, bool) {
	if a == nil {
		return nil, false
	}
	a.mu.RLock()
	d, ok := a.data[name]
	a.mu.RUnlock()
	return d, ok
}
func (a *Atom) Ability(name string) (Ability, bool) {
	if a == nil {
		return nil, false
	}
	a.mu.RLock()
	d, ok := a.abilities[name]
	a.mu.RUnlock()
	return d, ok
}
func (a *Atom) AllData() map[string]Data {
	r := map[string]Data{}
	if a == nil {
		return r
	}
	a.mu.RLock()
	for k, v := range a.data {
		r[k] = v
	}
	a.mu.RUnlock()
	return r
}
func (a *Atom) AllAbilities() map[string]Ability {
	r := map[string]Ability{}
	if a == nil {
		return r
	}
	a.mu.RLock()
	for k, v := range a.abilities {
		r[k] = v
	}
	a.mu.RUnlock()
	return r
}

// Compatibility names retained while callers migrate.
func (a *Atom) GetAllData() map[string]Data       { return a.AllData() }
func (a *Atom) GetAllAbility() map[string]Ability { return a.AllAbilities() }

// AddEventSink appends a sink to the atom's lifecycle observer chain. The
// first call replaces the single-sink slot; subsequent calls fan out via a
// MultiEventSink so observers like a logging recorder and a metrics
// collector can coexist. Returns ErrNilAtom for a nil receiver; a nil sink
// is a no-op.
func (a *Atom) AddEventSink(sink EventSink) error {
	if a == nil {
		return ErrNilAtom
	}
	if sink == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	switch existing := a.eventSink.(type) {
	case nil:
		a.eventSink = sink
	case *MultiEventSink:
		existing.Add(sink)
	default:
		a.eventSink = NewMultiEventSink(existing, sink)
	}
	return nil
}

// CommandNames returns the canonical command catalogue exposed by every
// registered component. Components implementing CommandLister contribute
// their sorted list; components that don't are present with an empty slice
// (callers can distinguish "didn't declare" from "isn't registered" via the
// presence of the key). The returned map is owned by the caller.
func (a *Atom) CommandNames() map[string][]string {
	out := map[string][]string{}
	if a == nil {
		return out
	}
	type entry struct {
		name string
		lister CommandLister
	}
	a.mu.RLock()
	entries := make([]entry, 0, len(a.data)+len(a.abilities))
	for n, c := range a.data {
		if l, ok := c.(CommandLister); ok {
			entries = append(entries, entry{name: n, lister: l})
		} else {
			out[n] = []string{}
		}
	}
	for n, c := range a.abilities {
		if l, ok := c.(CommandLister); ok {
			entries = append(entries, entry{name: n, lister: l})
		} else {
			out[n] = []string{}
		}
	}
	a.mu.RUnlock()
	for _, e := range entries {
		// Call user code outside the lock.
		cmds := e.lister.ListCommands()
		cp := append([]string(nil), cmds...)
		sort.Strings(cp)
		out[e.name] = cp
	}
	return out
}

func validateComponent(c Component) (name string, err error) {
	if c == nil || isNilComponent(c) {
		return "", ErrNilComponent
	}
	defer func() {
		if v := recover(); v != nil {
			err = &ComponentPanicError{Name: "<unknown>", Phase: "name", Value: v, Stack: debug.Stack()}
		}
	}()
	name = c.GetName()
	if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
		return "", ErrInvalidComponentName
	}
	return name, nil
}
func isNilComponent(c Component) bool {
	v := reflect.ValueOf(c)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	}
	return false
}

// Internal lifecycle helpers for package-level orchestration.
func (a *Atom) setState(s AtomState) { a.mu.Lock(); a.state = s; a.mu.Unlock() }
