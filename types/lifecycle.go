package types

import (
	"context"
	"errors"
	"runtime/debug"
	"sort"
)

// namedComponent is an immutable lifecycle snapshot. Keeping kind here is
// intentional: Data and Ability have distinct mount ordering and bookkeeping.
type namedComponent struct {
	name      string
	component Component
	kind      componentKind
}

type componentKind uint8

const (
	componentData componentKind = iota
	componentAbility
)

// PreRun validates and mounts all registered components deterministically.
// It returns the atom to Created on failure (after rolling back mounts), so a
// caller may correct the registration and retry.
func (a *Atom) PreRun() error { return a.MountAll() }

// MountAll performs the pre-run lifecycle transaction.
func (a *Atom) MountAll() error {
	if a == nil {
		return ErrNilAtom
	}
	a.mu.Lock()
	if a.state != AtomCreated || a.transitioning {
		a.mu.Unlock()
		return ErrInvalidAtomState
	}
	a.transitioning = true
	items := make([]namedComponent, 0, len(a.data)+len(a.abilities))
	for n, c := range a.data {
		items = append(items, namedComponent{name: n, component: c, kind: componentData})
	}
	for n, c := range a.abilities {
		items = append(items, namedComponent{name: n, component: c, kind: componentAbility})
	}
	a.mu.Unlock()

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].kind != items[j].kind {
			return items[i].kind < items[j].kind
		}
		return items[i].name < items[j].name
	})

	finish := func(err error, mounted []namedComponent) error {
		a.mu.Lock()
		if err == nil {
			a.state = AtomMounted
			a.mounted = append([]namedComponent(nil), mounted...)
			a.mountedAbilities = nil
			for _, m := range mounted {
				if m.kind == componentAbility {
					a.mountedAbilities = append(a.mountedAbilities, m)
				}
			}
		} else {
			a.mounted = nil
			a.mountedAbilities = nil
		}
		a.transitioning = false
		a.mu.Unlock()
		return err
	}

	var checkErrs []error
	for _, item := range items {
		if err := verifyName(item); err != nil {
			checkErrs = append(checkErrs, err)
			continue
		}
		if err := callCheck(item, a); err != nil {
			checkErrs = append(checkErrs, err)
			continue
		}
		if err := verifyName(item); err != nil {
			checkErrs = append(checkErrs, err)
		}
	}
	if len(checkErrs) > 0 {
		return finish(errors.Join(checkErrs...), nil)
	}

	mounted := make([]namedComponent, 0, len(items))
	for _, item := range items {
		if err := verifyName(item); err != nil {
			return finish(rollback(a, mounted, err), nil)
		}
		if err := callMount(item, a); err != nil {
			return finish(rollback(a, mounted, err), nil)
		}
		mounted = append(mounted, item)
		if err := verifyName(item); err != nil {
			return finish(rollback(a, mounted, err), nil)
		}
	}
	return finish(nil, mounted)
}

func verifyName(item namedComponent) error {
	var current string
	var panicValue any
	func() {
		defer func() {
			if v := recover(); v != nil {
				panicValue = v
			}
		}()
		current = item.component.GetName()
	}()
	if panicValue != nil {
		return &ComponentPanicError{Name: item.name, Phase: "name", Value: panicValue, Stack: debug.Stack()}
	}
	if current != item.name {
		return &ComponentNameChangedError{Name: item.name, Registered: item.name, Expected: item.name, Current: current}
	}
	return nil
}

func callCheck(item namedComponent, atom *Atom) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = &ComponentPanicError{Name: item.name, Phase: "check", Value: v, Stack: debug.Stack()}
		}
	}()
	err = item.component.Check(atom)
	if err != nil {
		return &ComponentError{Name: item.name, Phase: "check", Err: err}
	}
	return nil
}

func callMount(item namedComponent, atom *Atom) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = &ComponentPanicError{Name: item.name, Phase: "mount", Value: v, Stack: debug.Stack()}
		}
	}()
	err = item.component.Mount(atom)
	if err != nil {
		return &ComponentError{Name: item.name, Phase: "mount", Err: err}
	}
	return nil
}

func rollback(atom *Atom, mounted []namedComponent, cause error) error {
	ctx := context.Background()
	var errs []error
	errs = append(errs, cause)
	for i := len(mounted) - 1; i >= 0; i-- {
		m := mounted[i]
		u, ok := m.component.(Unmounter)
		if !ok {
			continue
		}
		func() {
			defer func() {
				if v := recover(); v != nil {
					errs = append(errs, &ComponentPanicError{Name: m.name, Phase: "unmount", Value: v, Stack: debug.Stack()})
				}
			}()
			if err := u.Unmount(ctx, atom); err != nil {
				errs = append(errs, &ComponentError{Name: m.name, Phase: "unmount", Err: err})
			}
		}()
	}
	return errors.Join(errs...)
}
