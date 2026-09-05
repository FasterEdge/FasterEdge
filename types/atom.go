// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
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
	mu                   sync.RWMutex
	name                 string
	data                 map[string]Data
	abilities            map[string]Ability
	state                AtomState
	mounted              []namedComponent
	mountedAbilities     []namedComponent
	transitioning        bool
	eventSink            EventSink
	commandAuthenticator CommandAuthenticator
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

// SetCommandAuthenticator installs the security boundary used by
// AuthenticatedCommandContext. It may only be configured before mounting.
func (a *Atom) SetCommandAuthenticator(auth CommandAuthenticator) error {
	if a == nil {
		return ErrNilAtom
	}
	if auth == nil || isNilInterface(auth) {
		return fmt.Errorf("set command authenticator: %w", ErrInvalidArguments)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state != AtomCreated || a.transitioning {
		return fmt.Errorf("set command authenticator: %w", ErrInvalidState)
	}
	a.commandAuthenticator = auth
	return nil
}

func isNilInterface(v any) bool {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
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
	// 跨注册表同名分裂: CommandNames 的目录(ability 覆盖 data)与 component()
	// 分发(data 优先)对同名组件得出相反结果——目录声称可用的命令实际路由
	// 到另一个组件并返回 ErrUnsupportedCommand。注册期直接拒绝。
	if _, ok := a.abilities[name]; ok {
		return fmt.Errorf("add data %s: %w (name conflicts with registered ability)", name, ErrDuplicateComponent)
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
	// 跨注册表同名分裂(见 addData 注释)。
	if _, ok := a.data[name]; ok {
		return fmt.Errorf("add ability %s: %w (name conflicts with registered data)", name, ErrDuplicateComponent)
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
		name   string
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
		// Call user code outside the lock. ListCommands 是唯一没有框架级
		// panic 兜底的组件回调之一——组件 panic 会让 CommandNames 冒泡
		// 造成进程级崩溃, 与其余回调(GetName/Check/Mount/Command)对齐。
		var cmds []string
		func() {
			defer func() {
				if v := recover(); v != nil {
					cmds = nil
				}
			}()
			cmds = e.lister.ListCommands()
		}()
		cp := append([]string(nil), cmds...)
		sort.Strings(cp)
		out[e.name] = cp
	}
	return out
}

// Descriptions aggregates Describe() across all registered data and abilities.
// Describe 是 Component 的必需成员但框架长期没有调用点(死契约)——这是唯一
// 的公开聚合入口, 让平台可以只读地展示组件描述。调用方持有返回 map 的所有权。
func (a *Atom) Descriptions() map[string]string {
	out := map[string]string{}
	if a == nil {
		return out
	}
	a.mu.RLock()
	entries := make([]Component, 0, len(a.data)+len(a.abilities))
	for _, c := range a.data {
		entries = append(entries, c)
	}
	for _, c := range a.abilities {
		entries = append(entries, c)
	}
	a.mu.RUnlock()
	for _, c := range entries {
		name := safeDescribeName(c)
		if name == "" {
			continue
		}
		out[name] = safeDescribeText(c)
	}
	return out
}

// safeDescribeName/safeDescribeText 与其余组件回调一致做 panic 兜底。
func safeDescribeName(c Component) (name string) {
	defer func() {
		if v := recover(); v != nil {
			name = ""
		}
	}()
	name = c.GetName()
	return name
}

func safeDescribeText(c Component) (desc string) {
	defer func() {
		if v := recover(); v != nil {
			desc = fmt.Sprintf("<panic: %v>", v)
		}
	}()
	return c.Describe()
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
