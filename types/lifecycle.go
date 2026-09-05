package types

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sort"
	"strings"
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

func (k componentKind) dependencyKind() DependencyKind {
	if k == componentAbility {
		return DependencyAbility
	}
	return DependencyData
}
func itemKey(item namedComponent) string {
	return item.kind.dependencyKind().String() + ":" + item.name
}
func sortComponents(items []namedComponent) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].kind != items[j].kind {
			return items[i].kind < items[j].kind
		}
		return items[i].name < items[j].name
	})
}

func dependencyOrder(items []namedComponent) ([]namedComponent, error) {
	sortComponents(items)
	byKey := make(map[string]namedComponent, len(items))
	byName := make(map[string][]namedComponent, len(items))
	for _, item := range items {
		byKey[itemKey(item)] = item
		byName[item.name] = append(byName[item.name], item)
	}
	deps := make(map[string][]string, len(items))
	var validation []error
	for _, item := range items {
		provider, ok := item.component.(DependencyProvider)
		if !ok {
			continue
		}
		// Dependencies 与 ListCommands 是仅有两个无框架级 panic 兜底的组件
		// 回调——组件 panic 会让 dependencyOrder 冒泡, 且 MountAll 的
		// transitioning 无 defer 复位, Atom 从此永久 ErrInvalidState 锁死。
		var decls []Dependency
		panicked := false
		func() {
			defer func() {
				if v := recover(); v != nil {
					panicked = true
				}
			}()
			decls = append([]Dependency(nil), provider.Dependencies()...)
		}()
		if panicked {
			return nil, &DependencyError{Component: item.name, Dependency: Dependency{Kind: item.kind.dependencyKind(), Name: item.name}, Err: fmt.Errorf("Dependencies() panicked")}
		}
		sort.Slice(decls, func(i, j int) bool {
			if decls[i].Kind != decls[j].Kind {
				return decls[i].Kind < decls[j].Kind
			}
			return decls[i].Name < decls[j].Name
		})
		for _, dep := range decls {
			if strings.TrimSpace(dep.Name) == "" || strings.TrimSpace(dep.Name) != dep.Name || (dep.Kind != DependencyData && dep.Kind != DependencyAbility) {
				validation = append(validation, &DependencyError{Component: item.name, Dependency: dep, Err: ErrMissingDependency})
				continue
			}
			key := dep.Kind.String() + ":" + dep.Name
			if _, exists := byKey[key]; !exists {
				actual := dep.Kind
				wrong := false
				for _, candidate := range byName[dep.Name] {
					if candidate.kind.dependencyKind() != dep.Kind {
						actual = candidate.kind.dependencyKind()
						wrong = true
						break
					}
				}
				if wrong {
					validation = append(validation, &DependencyError{Component: item.name, Dependency: dep, ActualKind: actual, Err: ErrWrongDependencyType})
				} else {
					validation = append(validation, &DependencyError{Component: item.name, Dependency: dep, Err: ErrMissingDependency})
				}
				continue
			}
			deps[itemKey(item)] = append(deps[itemKey(item)], key)
		}
	}
	if len(validation) > 0 {
		return nil, errors.Join(validation...)
	}
	state := make(map[string]uint8, len(items))
	ordered := make([]namedComponent, 0, len(items))
	stack := []string{}
	var visit func(string) error
	visit = func(key string) error {
		if state[key] == 2 {
			return nil
		}
		if state[key] == 1 {
			start := 0
			for i, k := range stack {
				if k == key {
					start = i
					break
				}
			}
			cycle := append([]string(nil), stack[start:]...)
			cycle = append(cycle, key)
			for i := range cycle {
				cycle[i] = byKey[cycle[i]].name
			}
			return &DependencyCycleError{Components: cycle}
		}
		state[key] = 1
		stack = append(stack, key)
		for _, dep := range deps[key] {
			if err := visit(dep); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		state[key] = 2
		ordered = append(ordered, byKey[key])
		return nil
	}
	for _, item := range items {
		if err := visit(itemKey(item)); err != nil {
			return nil, err
		}
	}
	return ordered, nil
}

// PreRun validates and mounts all registered components deterministically.
// It returns the atom to Created on failure (after rolling back mounts), so a
// caller may correct the registration and retry.
func (a *Atom) PreRun() error { return a.MountAll() }

// Reset transitions the atom from AtomStopped or AtomFailed back to
// AtomCreated so a caller may re-mount or re-run. Registered components
// are preserved; only mount bookkeeping (mounted, mountedAbilities) is
// cleared. Reset does NOT run Unmount — by the time you can call Reset,
// mounted components have already been torn down by UnmountAll or by
// finalizeAfterRun at the end of RunAll.
func (a *Atom) Reset() error {
	if a == nil {
		return ErrNilAtom
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.transitioning {
		return ErrInvalidAtomState
	}
	if a.state != AtomStopped && a.state != AtomFailed {
		return ErrInvalidAtomState
	}
	a.state = AtomCreated
	a.mounted = nil
	a.mountedAbilities = nil
	return nil
}

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
	// 双保险: dependencyOrder 内的 Dependencies() 已加 recover, 此处兜底
	// 其余未覆盖的组件回调 panic——transitioning 不复位会永久锁死 Atom。
	defer func() {
		if v := recover(); v != nil {
			a.mu.Lock()
			a.transitioning = false
			a.mu.Unlock()
		}
	}()
	items := make([]namedComponent, 0, len(a.data)+len(a.abilities))
	for n, c := range a.data {
		items = append(items, namedComponent{name: n, component: c, kind: componentData})
	}
	for n, c := range a.abilities {
		items = append(items, namedComponent{name: n, component: c, kind: componentAbility})
	}
	a.mu.Unlock()

	ordered, orderErr := dependencyOrder(items)
	if orderErr != nil {
		a.mu.Lock()
		a.transitioning = false
		a.mu.Unlock()
		return orderErr
	}
	items = ordered

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
		if err := a.observe(item.name, "", PhaseCheck, func() error { return callCheck(item, a) }); err != nil {
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
		if err := a.observe(item.name, "", PhaseMount, func() error { return callMount(item, a) }); err != nil {
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
		callErr := atom.observe(m.name, "", PhaseRollback, func() (err error) {
			defer func() {
				if v := recover(); v != nil {
					err = &ComponentPanicError{Name: m.name, Phase: "rollback", Value: v, Stack: debug.Stack()}
				}
			}()
			if err = u.Unmount(ctx, atom); err != nil {
				return &ComponentError{Name: m.name, Phase: "rollback", Err: err}
			}
			return nil
		})
		if callErr != nil {
			errs = append(errs, callErr)
		}
	}
	return errors.Join(errs...)
}

type runnerResult struct {
	name string
	err  error
}

// UnmountAll releases mounted components in reverse order and transitions the
// atom to Stopped. Cleanup is explicit and idempotent once the atom is stopped.
func (a *Atom) UnmountAll(ctx context.Context, shutdown time.Duration) error {
	if a == nil {
		return ErrNilAtom
	}
	if ctx == nil {
		return ErrNilContext
	}
	if shutdown <= 0 {
		return ErrInvalidShutdownTimeout
	}
	a.mu.Lock()
	if a.state == AtomStopped {
		a.mu.Unlock()
		return nil
	}
	if a.state != AtomMounted || a.transitioning {
		a.mu.Unlock()
		return ErrInvalidAtomState
	}
	a.transitioning = true
	mounted := append([]namedComponent(nil), a.mounted...)
	a.mu.Unlock()

	err := a.unmountWithTimeout(ctx, mounted, shutdown)
	a.mu.Lock()
	a.transitioning = false
	a.mounted = nil
	a.mountedAbilities = nil
	if err != nil {
		a.state = AtomFailed
	} else {
		a.state = AtomStopped
	}
	a.mu.Unlock()
	return err
}

// Close is the conventional cleanup alias for UnmountAll.
func (a *Atom) Close(ctx context.Context, shutdown time.Duration) error {
	return a.UnmountAll(ctx, shutdown)
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
					// 仅 context.Canceled 视为良性退出信号(被 cancel 的
					// runner 正常返回); DeadlineExceeded 只能源于 runner
					// 自身内部 deadline(child ctx 无 deadline, 父 ctx 过期
					// 由 L420 分支捕获)——旧实现把它一并排除, 导致单
					// runner 返回 DeadlineExceeded 时 RunAll 返回 nil 伪成功。
					if !errors.Is(res.err, context.Canceled) {
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
						return a.finalizeAfterRun(
							errors.Join(runErr, &ShutdownTimeoutError{Timeout: shutdown, Phase: "run", Components: names}),
							mounted, shutdown, true,
						)
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
	} else {
		<-ctx.Done()
		runErr = ctx.Err()
	}

	return a.finalizeAfterRun(runErr, mounted, shutdown, false)
}

// finalizeAfterRun tears down mounted components (mirroring UnmountAll), clears
// mounted bookkeeping, transitions the atom to Stopped/Failed, and returns
// the joined cleanup error alongside any pre-existing runErr. Both normal
// completion and the shutdown-timeout branch in RunAll funnel through here so
// the invariant "AtomStopped|AtomFailed ⇒ a.mounted == nil" is upheld by a
// single code path.
func (a *Atom) finalizeAfterRun(runErr error, mounted []namedComponent, shutdown time.Duration, forceFailed bool) error {
	cleanupErr := a.unmountWithTimeout(context.Background(), mounted, shutdown)
	a.mu.Lock()
	a.mounted = nil
	a.mountedAbilities = nil
	if cleanupErr != nil || forceFailed {
		a.state = AtomFailed
	} else {
		a.state = AtomStopped
	}
	a.mu.Unlock()
	if cleanupErr != nil {
		return errors.Join(runErr, cleanupErr)
	}
	return runErr
}

func safeRun(r Runner, ctx context.Context, atom *Atom, name string) (err error) {
	return atom.observe(name, "", PhaseRun, func() (err error) {
		defer func() {
			if v := recover(); v != nil {
				err = NewComponentPanicError(name, "run", v)
			}
		}()
		if err = r.Run(ctx, atom); err != nil {
			return &ComponentError{Name: name, Phase: "run", Err: err}
		}
		return nil
	})
}

func (a *Atom) unmountWithTimeout(base context.Context, mounted []namedComponent, timeout time.Duration) error {
	var errs []error
	for i := len(mounted) - 1; i >= 0; i-- {
		m := mounted[i]
		u, ok := m.component.(Unmounter)
		if !ok {
			continue
		}
		// 每个组件独立的卸载预算: 旧实现共享一个 WithTimeout(base),
		// 单个挂死组件(或已取消的 base ctx)会立即命中 Done 并 return,
		// 中止整条逆序卸载链——其余组件(如 Modbus/Serial 的 Close)永不
		// 执行, 且 UnmountAll 随即清空 mounted 置 AtomFailed, 无重试路径。
		compCtx, compCancel := context.WithTimeout(base, timeout)
		result := make(chan error, 1)
		go func() {
			defer compCancel()
			result <- a.observe(m.name, "", PhaseUnmount, func() (err error) {
				defer func() {
					if v := recover(); v != nil {
						err = NewComponentPanicError(m.name, "unmount", v)
					}
				}()
				if err = u.Unmount(compCtx, a); err != nil {
					return &ComponentError{Name: m.name, Phase: "unmount", Err: err}
				}
				return nil
			})
		}()
		select {
		case err := <-result:
			if err != nil {
				errs = append(errs, err)
			}
		case <-compCtx.Done():
			// 仅记录该组件超时并继续卸载剩余组件(goroutine 随 compCtx
			// 取消自行收敛; 缓冲 chan 保证发送侧不阻塞)。
			errs = append(errs, &ShutdownTimeoutError{Timeout: timeout, Phase: "unmount", Components: []string{m.name}})
		}
	}
	return errors.Join(errs...)
}
