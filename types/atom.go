package types

import (
	"fmt"
	"reflect"
	"runtime/debug"
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
