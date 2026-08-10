package types

import (
	"context"
	"errors"
	"runtime/debug"
	"sort"
	"time"
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

type runnerResult struct {
	name string
	err  error
}

// RunAll supervises all mounted abilities implementing Runner, then cleans up
// mounted components in reverse order.
func (a *Atom) RunAll(ctx context.Context, shutdown time.Duration) error {
	if a == nil {
		return ErrNilAtom
	}
	if ctx == nil {
		return ErrNilContext
	}
	if shutdown <= 0 {
		return ErrInvalidShutdownTimeout
	}
	parentErr := ctx.Err()
	a.mu.Lock()
	if a.state != AtomMounted || a.transitioning {
		a.mu.Unlock()
		return ErrInvalidAtomState
	}
	a.state = AtomRunning
	runners := make([]namedComponent, 0, len(a.mountedAbilities))
	for _, m := range a.mountedAbilities {
		if _, ok := m.component.(Runner); ok {
			runners = append(runners, m)
		}
	}
	mounted := append([]namedComponent(nil), a.mounted...)
	a.mu.Unlock()

	var runErr error
	if parentErr == nil && len(runners) > 0 {
		child, cancel := context.WithCancel(ctx)
		results := make(chan runnerResult, len(runners))
		for _, m := range runners {
			m := m
			r := m.component.(Runner)
			go func() {
				results <- runnerResult{name: m.name, err: safeRun(r, child, a, m.name)}
			}()
		}
		pending := make(map[string]struct{}, len(runners))
		for _, m := range runners {
			pending[m.name] = struct{}{}
		}
		canceled := false
		for len(pending) > 0 {
			select {
			case <-ctx.Done():
				if runErr == nil {
					runErr = ctx.Err()
				}
				if !canceled {
					cancel()
					canceled = true
				}
			case res := <-results:
				delete(pending, res.name)
				if res.err != nil && !canceled {
					if !errors.Is(res.err, context.Canceled) && !errors.Is(res.err, context.DeadlineExceeded) {
						runErr = res.err
					}
					cancel()
					canceled = true
				} else if res.err != nil && runErr == nil && !errors.Is(res.err, context.Canceled) {
					runErr = res.err
				}
			}
			if canceled && len(pending) > 0 {
				timer := time.NewTimer(shutdown)
				for len(pending) > 0 {
					select {
					case res := <-results:
						delete(pending, res.name)
					case <-timer.C:
						names := make([]string, 0, len(pending))
						for n := range pending {
							names = append(names, n)
						}
						sort.Strings(names)
						cancel()
						a.setState(AtomFailed)
						return errors.Join(runErr, &ShutdownTimeoutError{Timeout: shutdown, Phase: "run", Components: names})
					}
				}
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
			}
		}
		cancel()
	} else if parentErr != nil {
		runErr = parentErr
	}

	cleanupErr := a.unmountWithTimeout(context.Background(), mounted, shutdown)
	if cleanupErr != nil {
		a.setState(AtomFailed)
		return errors.Join(runErr, cleanupErr)
	}
	a.setState(AtomStopped)
	return runErr
}

func safeRun(r Runner, ctx context.Context, atom *Atom, name string) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = NewComponentPanicError(name, "run", v)
		}
	}()
	return r.Run(ctx, atom)
}

func (a *Atom) unmountWithTimeout(base context.Context, mounted []namedComponent, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(base, timeout)
	defer cancel()
	var errs []error
	for i := len(mounted) - 1; i >= 0; i-- {
		m := mounted[i]
		u, ok := m.component.(Unmounter)
		if !ok {
			continue
		}
		result := make(chan error, 1)
		go func() {
			var err error
			defer func() {
				if v := recover(); v != nil {
					err = NewComponentPanicError(m.name, "unmount", v)
				}
				result <- err
			}()
			err = u.Unmount(ctx, a)
			if err != nil {
				err = &ComponentError{Name: m.name, Phase: "unmount", Err: err}
			}
		}()
		select {
		case err := <-result:
			if err != nil {
				errs = append(errs, err)
			}
		case <-ctx.Done():
			return errors.Join(append(errs, &ShutdownTimeoutError{Timeout: timeout, Phase: "unmount", Components: []string{m.name}})...)
		}
	}
	return errors.Join(errs...)
}
