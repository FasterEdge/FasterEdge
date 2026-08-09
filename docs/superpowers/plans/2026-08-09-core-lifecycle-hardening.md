# FasterEdge Core Lifecycle Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace FasterEdge's unsafe registration and permanent-block lifecycle with a pointer-based, concurrent-safe Atom, deterministic mounting, supervised long-running Abilities, explicit errors, and offline regression tests.

**Architecture:** `types.Atom` owns private candidate registries, lifecycle state, mounted order, and all transitions. Root-package helpers remain the public entry points and delegate to Atom lifecycle methods. Components retain one `Command` entry point; long-running Abilities opt into `types.Runner` and are supervised with context cancellation, panic recovery, bounded shutdown, and reverse-order cleanup.

**Tech Stack:** Go 1.25.5 standard library (`context`, `errors`, `reflect`, `runtime/debug`, `sort`, `sync`, `time`), existing FasterEdge packages, built-in `testing` and race detector.

**Design:** `docs/superpowers/specs/2026-08-09-core-hardening-design.md`

**Execution order:** Complete this plan before `2026-08-09-time-ability-hardening.md`. This plan only migrates TimeAbility to the new component contract; the second plan replaces its clock and network implementation.

---

## File map

- Create `types/component.go`: shared component, runner, unmount, and command-output contracts.
- Create `types/errors.go`: sentinel errors plus component and panic error wrappers.
- Replace `types/atom.go`: private registries, state, registration, immutable snapshots, and name validation.
- Create `types/atom_test.go`: registration, snapshot, typed-nil, state, and concurrency tests.
- Create `types/lifecycle.go`: deterministic check/mount/unmount and Runner supervision.
- Create `types/lifecycle_test.go`: lifecycle ordering, rollback, cancellation, timeout, error, and panic tests.
- Replace `types/ability.go` and `types/data.go`: thin interfaces embedding `Component`.
- Modify `init.go`: pointer-returning initialization and lifecycle wrappers.
- Create `lifecycle.go`: `RunOption` and shutdown-timeout validation.
- Replace `atom_test.go`: offline root-package integration tests.
- Modify `ability/base_ability.go`, `ability/role_ability.go`, `ability/time_ability.go`, and `data/base_data.go`: migrate signatures and remove discarded errors.
- Create `ability/base_ability_test.go`, `ability/role_ability_test.go`, and `data/base_data_test.go`: strict built-in command tests.
- Modify `README.md`: core initialization, Runner, and graceful-shutdown examples.

### Task 1: Shared component contract and safe Atom registration

**Files:**
- Create: `types/component.go`
- Create: `types/errors.go`
- Replace: `types/ability.go`
- Replace: `types/data.go`
- Replace: `types/atom.go`
- Create: `types/atom_test.go`
- Modify: `ability/base_ability.go`
- Modify: `ability/role_ability.go`
- Modify: `ability/time_ability.go`
- Modify: `data/base_data.go`
- Modify: `init.go`
- Replace: `atom_test.go`

- [ ] **Step 1: Write registration tests before changing production interfaces**

Create `types/atom_test.go` with a local component that exposes a mutable name and satisfies the intended interface:

```go
package types

import (
    "errors"
    "testing"
)

type registrationComponent struct {
    name string
}

func (c *registrationComponent) GetName() string                    { return c.name }
func (c *registrationComponent) Describe() string                   { return "registration test component" }
func (c *registrationComponent) Check(*Atom) error                  { return nil }
func (c *registrationComponent) Mount(*Atom) error                  { return nil }
func (c *registrationComponent) Command(*Atom, string, any) CommandOutput {
    return CommandOutput{}
}

func TestAtomRejectsInvalidDataRegistration(t *testing.T) {
    var typedNil *registrationComponent
    tests := []struct {
        name string
        data Data
        want error
    }{
        {name: "nil interface", data: nil, want: ErrNilComponent},
        {name: "typed nil", data: typedNil, want: ErrNilComponent},
        {name: "blank name", data: &registrationComponent{name: "  "}, want: ErrInvalidComponentName},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            var atom Atom
            if err := atom.AddData(tt.data); !errors.Is(err, tt.want) {
                t.Fatalf("AddData() error = %v, want %v", err, tt.want)
            }
        })
    }
}

func TestAtomRejectsDuplicateNamesWithinARegistry(t *testing.T) {
    var atom Atom
    if err := atom.AddAbility(&registrationComponent{name: "worker"}); err != nil {
        t.Fatalf("first AddAbility() error = %v", err)
    }
    err := atom.AddAbility(&registrationComponent{name: "worker"})
    if !errors.Is(err, ErrDuplicateComponent) {
        t.Fatalf("second AddAbility() error = %v, want ErrDuplicateComponent", err)
    }
}

func TestAtomSnapshotsDoNotExposeRegistryMaps(t *testing.T) {
    var atom Atom
    original := &registrationComponent{name: "data"}
    if err := atom.AddData(original); err != nil {
        t.Fatal(err)
    }
    snapshot := atom.AllData()
    delete(snapshot, "data")
    snapshot["replacement"] = &registrationComponent{name: "replacement"}

    got, ok := atom.Data("data")
    if !ok || got != original {
        t.Fatalf("Data(data) = (%v, %v), want original component", got, ok)
    }
    if _, ok := atom.Data("replacement"); ok {
        t.Fatal("mutation of AllData result changed Atom registry")
    }
}

func TestSetNameRejectsChangesAfterCreatedState(t *testing.T) {
    var atom Atom
    if err := atom.SetName("edge-1"); err != nil {
        t.Fatal(err)
    }
    atom.state = AtomMounted
    if err := atom.SetName("edge-2"); !errors.Is(err, ErrInvalidState) {
        t.Fatalf("SetName() error = %v, want ErrInvalidState", err)
    }
}

func TestShutdownTimeoutErrorUnwrapsSentinel(t *testing.T) {
    err := &ShutdownTimeoutError{
        Timeout:    time.Second,
        Phase:      "runner",
        Components: []string{"beta", "alpha"},
    }
    if !errors.Is(err, ErrShutdownTimeout) {
        t.Fatalf("errors.Is(%v, ErrShutdownTimeout) = false", err)
    }
}
```

Add `time` to the test imports. Keep this test in package `types`, not `types_test`, because later lifecycle tests need controlled access to unexported test-only state without adding production escape hatches.

Also add `TestAtomConcurrentRegistrationAndSnapshots`: release 32 goroutines together, register unique Ability names while another goroutine repeatedly calls `AllAbilities`, wait with `sync.WaitGroup`, and assert all 32 names exist. Add `TestAtomPreservesCaseSensitiveNames` to register `Worker` and `worker` and retrieve both exact keys. These tests establish the lock and key semantics before implementation.

- [ ] **Step 2: Run the new test and verify the RED state**

Run:

```bash
go test ./types -run 'TestAtom(Rejects|Snapshots|Concurrent|Preserves)|TestSetName|TestShutdownTimeoutError' -count=1
```

Expected: build failure because `CommandOutput`, sentinel errors, pointer-based component methods, and the new Atom API do not exist. Confirm the failure is about the intended API, not a syntax error in the test.

- [ ] **Step 3: Add the shared contracts and error vocabulary**

Create `types/component.go`:

```go
package types

import "context"

type CommandOutput struct {
    Name  string
    Value any
    Err   error
}

func (o CommandOutput) Success() bool { return o.Err == nil }

type Component interface {
    GetName() string
    Describe() string
    Check(*Atom) error
    Mount(*Atom) error
    Command(*Atom, string, any) CommandOutput
}

type Runner interface {
    Run(context.Context, *Atom) error
}

type Unmounter interface {
    Unmount(context.Context, *Atom) error
}
```

Replace `types/data.go` and `types/ability.go` with:

```go
package types

type Data interface {
    Component
}
```

and:

```go
package types

type Ability interface {
    Component
}
```

Create `types/errors.go`:

```go
package types

import (
    "errors"
    "fmt"
    "strings"
    "time"
)

var (
    ErrNilAtom              = errors.New("atom is nil")
    ErrNilContext           = errors.New("context is nil")
    ErrNilComponent         = errors.New("component is nil")
    ErrInvalidComponentName = errors.New("component name is empty")
    ErrDuplicateComponent   = errors.New("component name is already registered")
    ErrComponentNameChanged = errors.New("component name changed after registration")
    ErrMissingDependency    = errors.New("component dependency is missing")
    ErrInvalidState         = errors.New("atom lifecycle state does not allow this operation")
    ErrInvalidArguments     = errors.New("invalid command arguments")
    ErrUnsupportedCommand   = errors.New("unsupported command")
    ErrShutdownTimeout      = errors.New("lifecycle shutdown timed out")
)

type ComponentError struct {
    Name  string
    Phase string
    Err   error
}

func (e *ComponentError) Error() string {
    return fmt.Sprintf("%s %s: %v", e.Phase, e.Name, e.Err)
}

func (e *ComponentError) Unwrap() error { return e.Err }

type ComponentPanicError struct {
    Name  string
    Phase string
    Value any
    Stack []byte
}

func (e *ComponentPanicError) Error() string {
    return fmt.Sprintf("%s %s panicked: %v", e.Phase, e.Name, e.Value)
}

type ShutdownTimeoutError struct {
    Timeout    time.Duration
    Phase      string
    Components []string
}

func (e *ShutdownTimeoutError) Error() string {
    return fmt.Sprintf("%s after %s waiting for %s: %v", e.Phase, e.Timeout, strings.Join(e.Components, ", "), ErrShutdownTimeout)
}

func (e *ShutdownTimeoutError) Unwrap() error { return ErrShutdownTimeout }
```

- [ ] **Step 4: Replace Atom's public maps with locked registration methods**

Replace `types/atom.go` with an implementation containing these exact public types and methods:

```go
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
    mu        sync.RWMutex
    name      string
    data      map[string]Data
    abilities map[string]Ability
    state     AtomState
}

func (a *Atom) GetName() string {
    a.mu.RLock()
    defer a.mu.RUnlock()
    return a.name
}

func (a *Atom) SetName(name string) error {
    a.mu.Lock()
    defer a.mu.Unlock()
    if a.state != AtomCreated {
        return fmt.Errorf("set atom name: %w", ErrInvalidState)
    }
    a.name = strings.TrimSpace(name)
    return nil
}

func (a *Atom) State() AtomState {
    a.mu.RLock()
    defer a.mu.RUnlock()
    return a.state
}

func (a *Atom) AddData(data Data) error {
    name, err := validateComponent(data)
    if err != nil {
        return fmt.Errorf("add data: %w", err)
    }
    a.mu.Lock()
    defer a.mu.Unlock()
    if a.state != AtomCreated {
        return fmt.Errorf("add data %s: %w", name, ErrInvalidState)
    }
    if a.data == nil {
        a.data = make(map[string]Data)
    }
    if _, exists := a.data[name]; exists {
        return fmt.Errorf("add data %s: %w", name, ErrDuplicateComponent)
    }
    a.data[name] = data
    return nil
}

func (a *Atom) AddAbility(ability Ability) error {
    name, err := validateComponent(ability)
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
    if _, exists := a.abilities[name]; exists {
        return fmt.Errorf("add ability %s: %w", name, ErrDuplicateComponent)
    }
    a.abilities[name] = ability
    return nil
}

func (a *Atom) Data(name string) (Data, bool) {
    a.mu.RLock()
    defer a.mu.RUnlock()
    value, ok := a.data[name]
    return value, ok
}

func (a *Atom) Ability(name string) (Ability, bool) {
    a.mu.RLock()
    defer a.mu.RUnlock()
    value, ok := a.abilities[name]
    return value, ok
}

func (a *Atom) AllData() map[string]Data {
    a.mu.RLock()
    defer a.mu.RUnlock()
    result := make(map[string]Data, len(a.data))
    for name, value := range a.data {
        result[name] = value
    }
    return result
}

func (a *Atom) AllAbilities() map[string]Ability {
    a.mu.RLock()
    defer a.mu.RUnlock()
    result := make(map[string]Ability, len(a.abilities))
    for name, value := range a.abilities {
        result[name] = value
    }
    return result
}

func validateComponent(component Component) (name string, err error) {
    if isNilComponent(component) {
        return "", ErrNilComponent
    }
    defer func() {
        if value := recover(); value != nil {
            name = ""
            err = &ComponentPanicError{Name: "<unknown>", Phase: "name", Value: value, Stack: debug.Stack()}
        }
    }()
    name = component.GetName()
    trimmed := strings.TrimSpace(name)
    if trimmed == "" || trimmed != name {
        return "", ErrInvalidComponentName
    }
    return name, nil
}

func isNilComponent(component Component) bool {
    if component == nil {
        return true
    }
    value := reflect.ValueOf(component)
    switch value.Kind() {
    case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
        return value.IsNil()
    default:
        return false
    }
}
```

Do not call `GetName` while holding Atom's mutex: it is plugin code and may re-enter Atom. Validation must finish before taking the write lock. Extend the RED table with a component named `" worker "` and a component whose `GetName` panics; assert the former wraps `ErrInvalidComponentName` and the latter is a `ComponentPanicError` with phase `name` and a non-empty stack.

- [ ] **Step 5: Mechanically migrate every current component to the pointer/error contract**

For `BaseData`, `BaseAbility`, `RoleAbility`, and `TimeAbility`:

- change every `types.Atom` parameter to `*types.Atom`;
- change `Check` and `Mount` to return `error`;
- make successful checks and mounts return nil;
- make BaseAbility, RoleAbility, and TimeAbility checks wrap `types.ErrMissingDependency` when BaseData is absent;
- remove `atom.AddData` and `atom.AddAbility` calls from `Mount`;
- change `Command` to return `types.CommandOutput`;
- represent failure as `Err: fmt.Errorf("command %s: %w", act, types.ErrUnsupportedCommand)`;
- replace old `Success: true` results with a zero `Err`;
- preserve current business behavior until Tasks 4 and the TimeAbility plan add regression tests.

Update `init.go` so `InitAtom` returns `*types.Atom` and checks built-in registration:

```go
func InitAtom() *types.Atom {
    atom := &types.Atom{}
    if err := atom.AddData(&data.BaseData{}); err != nil {
        panic(fmt.Sprintf("register BaseData: %v", err))
    }
    if err := atom.AddAbility(&ability.BaseAbility{}); err != nil {
        panic(fmt.Sprintf("register BaseAbility: %v", err))
    }
    return atom
}
```

Temporarily update existing `PreRunAtom` and `RunAtom` call sites to use pointers so the repository compiles. Their final implementations are Tasks 2 and 3.

Replace the live-NTP `atom_test.go` with an offline initialization test:

```go
package FasterEdge

import "testing"

func TestInitAtomRegistersBaseComponents(t *testing.T) {
    atom := InitAtom()
    if _, ok := atom.Data("BaseData"); !ok {
        t.Fatal("BaseData not registered")
    }
    if _, ok := atom.Ability("BaseAbility"); !ok {
        t.Fatal("BaseAbility not registered")
    }
}
```

- [ ] **Step 6: Verify GREEN for registration and the whole repository**

Run:

```bash
gofmt -w types/component.go types/errors.go types/ability.go types/data.go types/atom.go types/atom_test.go init.go atom_test.go ability/base_ability.go ability/role_ability.go ability/time_ability.go data/base_data.go
go test ./types -run 'TestAtom(Rejects|Snapshots)|TestSetName' -count=1
go test ./... -count=1
```

Expected: all commands exit 0; no test opens an NTP or HTTP connection.

- [ ] **Step 7: Commit the contract and registration change**

```bash
git add types init.go atom_test.go ability data
git commit -m "refactor: define safe component contracts"
```

### Task 2: Deterministic check, mount, rollback, and panic isolation

**Files:**
- Create: `types/lifecycle.go`
- Create: `types/lifecycle_test.go`
- Modify: `init.go`

- [ ] **Step 1: Write failing lifecycle tests**

Create a `lifecycleComponent` test helper with function fields for `check`, `mount`, and `unmount` plus a mutex-protected event slice. Add independent tests named `TestMountAllChecksEveryComponentBeforeMounting`, `TestMountAllDoesNotMountWhenAnyCheckFails`, `TestMountAllRollsBackInReverseOrder`, `TestMountAllRejectsChangedComponentName`, `TestMountAllConvertsCheckMountAndUnmountPanics`, and `TestMountAllRejectsConcurrentLifecycleTransition`.

Use channels to stop `Check` at a known boundary in the concurrency test. Assert:

- Data names are checked in sorted order before sorted Ability names.
- No `Mount` event exists when any check returns a sentinel error.
- A mount failure on component C produces unmount events B then A.
- `errors.Is` reaches the original check/mount/unmount error.
- `errors.As` reaches `*ComponentPanicError` and `len(Stack) > 0`.
- changing `GetName()` after registration yields `ErrComponentNameChanged`.
- Atom finishes failed mount attempts in `AtomFailed`.

- [ ] **Step 2: Run the lifecycle tests and verify RED**

Run:

```bash
go test ./types -run 'TestMountAll' -count=1
```

Expected: build failure because `MountAll` and safe lifecycle callbacks do not exist.

- [ ] **Step 3: Implement the mount transaction without calling plugins under a lock**

Create `types/lifecycle.go` with:

```go
func (a *Atom) MountAll() error
```

At this point add these private Atom fields and entry type; do not add `RunAll` until its failing tests exist in Task 3:

```go
type namedComponent struct {
    name      string
    component Component
}

// fields added to Atom
mounted          []namedComponent
mountedAbilities []namedComponent
transitioning    bool
```

Implement `MountAll` as this state machine:

1. lock, require `AtomCreated && !transitioning`, set `transitioning = true`, copy Data and Ability entries, unlock;
2. sort Data and Ability entries separately by registered name;
3. before every callback, recover `GetName` panic and compare the returned string exactly to the registered name;
4. invoke every `Check` through `safeComponentCall` and join all check failures;
5. only when all checks pass, invoke `Mount` in stable order;
6. on mount failure, invoke `Unmount` for already-mounted components in reverse order with `context.Background()` rather than a canceled context;
7. lock only to publish `mounted`, the distinct `mountedAbilities` slice, `state`, and `transitioning` after callbacks finish.

In the same GREEN change, update `SetName`, `AddData`, and `AddAbility` to require `AtomCreated && !transitioning`. This closes the window where a component could be registered after the pre-run snapshot but before Atom becomes mounted.

Add the panic wrapper:

```go
func safeComponentCall(name, phase string, call func() error) (err error) {
    defer func() {
        if value := recover(); value != nil {
            err = &ComponentPanicError{
                Name:  name,
                Phase: phase,
                Value: value,
                Stack: debug.Stack(),
            }
        }
    }()
    if callErr := call(); callErr != nil {
        return &ComponentError{Name: name, Phase: phase, Err: callErr}
    }
    return nil
}
```

Add `safeComponentName` with the same panic recovery discipline and `unmountReverse` that calls only components implementing `Unmounter` and uses `errors.Join`. Never hold `Atom.mu` while invoking `GetName`, `Check`, `Mount`, or `Unmount`. Do not infer Data versus Ability from the markerless `Component` method set; populate `mountedAbilities` while copying the Ability registry.

- [ ] **Step 4: Make PreRunAtom delegate to the mount transaction**

Replace `PreRunAtom` with:

```go
func PreRunAtom(atom *types.Atom) error {
    if atom == nil {
        return types.ErrNilAtom
    }
    return atom.MountAll()
}
```

Printing logo and version is no longer a lifecycle side effect.

- [ ] **Step 5: Verify GREEN and race safety**

Run:

```bash
gofmt -w types/lifecycle.go types/lifecycle_test.go init.go
go test ./types -run 'TestMountAll' -count=1
go test -race ./types -run 'TestMountAll|TestAtom' -count=1
go test ./... -count=1
```

Expected: all commands exit 0. The race detector reports no races.

- [ ] **Step 6: Commit the mount lifecycle**

```bash
git add types/lifecycle.go types/lifecycle_test.go init.go
git commit -m "feat: enforce component mount lifecycle"
```

### Task 3: Supervise long-running Abilities and shut down safely

**Files:**
- Create: `lifecycle.go`
- Modify: `init.go`
- Modify: `types/lifecycle.go`
- Modify: `types/lifecycle_test.go`
- Modify: `ability/base_ability.go`

- [ ] **Step 1: Write Runner tests before implementing RunAll**

Add channel-driven test runners and tests named `TestRunAllBlocksUntilCallerCancellation`, `TestRunAllHandlesAlreadyCanceledContext`, `TestRunAllRejectsInvalidStates`, `TestRunAllCancelsSiblingAfterRunnerError`, `TestRunAllConvertsRunnerPanicToComponentError`, `TestRunAllReportsShutdownTimeoutWithoutUnmountingLiveRunner`, `TestRunAllUnmountsInReverseOrderAfterEveryRunnerStops`, `TestRunAllUsesFreshCleanupContext`, and `TestRunAllReturnsNilWhenNoRunnerExists` to `types/lifecycle_test.go`.

Each runner receives `started` and `stopped` channels. Tests wait on those channels rather than sleeping. For the timeout case:

- the runner closes `started` then blocks on a test-controlled `release` channel and deliberately ignores context;
- call `RunAll` with a 20 ms shutdown timeout;
- assert `errors.Is(err, ErrShutdownTimeout)`, then declare `var timeoutErr *ShutdownTimeoutError` and require `errors.As(err, &timeoutErr)`; verify sorted active component names and `State() == AtomFailed`;
- assert no unmount callback ran;
- close `release` at test cleanup so the deliberately leaked goroutine terminates.

For the sibling-cancellation case, start both runners before allowing one to return a sentinel error; assert the sibling observes `ctx.Done()` and `errors.Is` reaches the sentinel.

- [ ] **Step 2: Run Runner tests and verify RED**

Run:

```bash
go test ./types -run 'TestRunAll' -count=1
```

Expected: failures because `RunAll` has no supervisor behavior.

- [ ] **Step 3: Implement the Runner supervisor**

In `types/lifecycle.go`:

- require non-nil context, `AtomMounted` state, and a positive shutdown timeout;
- snapshot `ctx.Err()` before launching Runners; an already-canceled parent starts no Runner, performs cleanup with a fresh context, and returns the parent error;
- atomically switch to `AtomRunning` and copy the mounted Ability list;
- select only Abilities implementing `Runner`;
- launch one goroutine per Runner and send a buffered result containing the registered name and error;
- wrap each `Run` call with panic recovery using `safeComponentCall(name, "run", func() error { return runner.Run(childCtx, a) })`;
- on parent cancellation or the first non-context Runner error, cancel the child context and start the shutdown timer;
- track pending Runner names in a map so `ShutdownTimeoutError` contains an owned, sorted copy of exactly the implementations that ignored cancellation;
- ignore child `context.Canceled` errors after supervisor cancellation, while preserving independent Runner errors;
- call `unmountReverse` only after every Runner has returned, using a new cleanup context derived from `context.Background()` rather than the canceled Runner context;
- bound cleanup with the same shutdown duration; run each Unmount callback through a buffered result channel so a callback that ignores its cleanup context cannot keep `RunAll` blocked forever;
- set `AtomStopped` after complete cleanup, `AtomFailed` after cleanup failure or shutdown timeout;
- combine runner, parent-context, timeout, and unmount errors with `errors.Join`.

Use a timer created only after shutdown begins. Stop and drain it on the successful path. A cleanup timeout returns `ShutdownTimeoutError{Phase: "unmount"}` and leaves Atom in `AtomFailed`; every lifecycle result channel is buffered to the number of possible senders so a timed-out supervisor never strands a goroutine on send.

- [ ] **Step 4: Add the public RunAtom options and wrapper**

Create root `lifecycle.go`:

```go
package FasterEdge

import (
    "context"
    "fmt"
    "time"

    "github.com/FasterEdge/FasterEdge/types"
)

const defaultShutdownTimeout = 5 * time.Second

type runOptions struct {
    shutdownTimeout time.Duration
}

type RunOption func(*runOptions) error

func WithShutdownTimeout(timeout time.Duration) RunOption {
    return func(options *runOptions) error {
        if timeout <= 0 {
            return fmt.Errorf("shutdown timeout %s: %w", timeout, types.ErrInvalidArguments)
        }
        options.shutdownTimeout = timeout
        return nil
    }
}

func RunAtom(ctx context.Context, atom *types.Atom, opts ...RunOption) error {
    if ctx == nil {
        return types.ErrNilContext
    }
    if atom == nil {
        return types.ErrNilAtom
    }
    options := runOptions{shutdownTimeout: defaultShutdownTimeout}
    for _, option := range opts {
        if option == nil {
            return fmt.Errorf("nil RunOption: %w", types.ErrInvalidArguments)
        }
        if err := option(&options); err != nil {
            return err
        }
    }
    return atom.RunAll(ctx, options.shutdownTimeout)
}
```

Remove the old `RunAtom` implementation from `init.go`. Remove `blocking`, `runnable`, and `run` branches from `BaseAbility.Command`.

- [ ] **Step 5: Add root API validation tests**

In `atom_test.go` add:

```go
func TestRunAtomRejectsNilInputsAndOptions(t *testing.T) {
    atom := InitAtom()
    if err := RunAtom(nil, atom); !errors.Is(err, types.ErrNilContext) {
        t.Fatalf("nil context error = %v", err)
    }
    if err := RunAtom(context.Background(), nil); !errors.Is(err, types.ErrNilAtom) {
        t.Fatalf("nil atom error = %v", err)
    }
    if err := RunAtom(context.Background(), atom, WithShutdownTimeout(0)); !errors.Is(err, types.ErrInvalidArguments) {
        t.Fatalf("zero timeout error = %v", err)
    }
}
```

- [ ] **Step 6: Verify GREEN, race safety, and repeatability**

Run:

```bash
gofmt -w lifecycle.go init.go atom_test.go types/lifecycle.go types/lifecycle_test.go ability/base_ability.go
go test ./types -run 'TestRunAll' -count=1
go test ./... -count=20
go test -race ./... -count=1
```

Expected: all commands exit 0. Repeated tests do not hang or rely on external services.

- [ ] **Step 7: Commit Runner supervision**

```bash
git add lifecycle.go init.go atom_test.go types/lifecycle.go types/lifecycle_test.go ability/base_ability.go
git commit -m "feat: supervise long-running abilities"
```

### Task 4: Make built-in commands strict, deterministic, and race-safe

**Files:**
- Modify: `ability/base_ability.go`
- Create: `ability/base_ability_test.go`
- Modify: `ability/role_ability.go`
- Create: `ability/role_ability_test.go`
- Modify: `data/base_data.go`
- Create: `data/base_data_test.go`
- Modify: `init.go`
- Modify: `atom_test.go`

- [ ] **Step 1: Write failing BaseData and BaseAbility command tests**

Add tests for these command constants and exact result types:

```go
const (
    CommandLogo             = "logo"
    CommandInfo             = "info"
    CommandListDataNames    = "list_data_names"
    CommandListAbilityNames = "list_ability_names"
)
```

Tests must assert:

- logo and info are returned as strings in `CommandOutput.Value`;
- commands produce no stdout lifecycle side effect;
- BaseAbility returns sorted `[]string` names;
- a non-nil argument to a no-argument command returns `ErrInvalidArguments`;
- an unknown command returns `ErrUnsupportedCommand`;
- BaseAbility.Check returns an error wrapping `types.ErrMissingDependency` when BaseData is absent.

- [ ] **Step 2: Write failing RoleAbility validation and race tests**

In `ability/role_ability_test.go` add tests named `TestRoleAbilityRejectsWrongAndBlankSetRoleArguments`, `TestRoleAbilitySetAndGetRole`, and `TestRoleAbilityConcurrentCommands`.

Use `RoleAbilityArgs{Role: "worker"}` for the valid call. In the concurrency test start 16 goroutines behind one channel, have each issue `set_role` followed by `get_role`, collect every returned error, and wait with `sync.WaitGroup`. Do not assert one final role value because concurrent writers have no defined order.

- [ ] **Step 3: Run built-in tests and verify RED**

Run:

```bash
go test ./ability ./data -run 'Test(Base|Role)' -count=1
```

Expected: failures because commands still print, accept zero-value arguments, return unsorted output, or access RoleAbility state without a mutex.

- [ ] **Step 4: Implement strict commands**

Implement:

- BaseData `logo` and `info` by returning strings without printing.
- BaseAbility list commands using `atom.AllData()` and `atom.AllAbilities()`, collecting keys and calling `sort.Strings`.
- RoleAbility with `sync.RWMutex` around its role field.
- no-argument validation by returning `types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}` whenever `args != nil`.
- strict `typed, ok := args.(RoleAbilityArgs)` and `strings.TrimSpace(typed.Role) != ""` before mutation.
- all unknown commands wrap `types.ErrUnsupportedCommand`.

Remove obsolete `BaseAbilityArgs`, `BaseDataArgs`, duplicate output structs, debug prints, and discarded `fmt.Errorf` calls.

- [ ] **Step 5: Verify GREEN and run the race detector**

Run:

```bash
gofmt -w ability/base_ability.go ability/base_ability_test.go ability/role_ability.go ability/role_ability_test.go data/base_data.go data/base_data_test.go init.go atom_test.go
go test ./ability ./data -count=1
go test -race ./ability ./data -count=1
go vet ./...
```

Expected: all commands exit 0. `go vet` no longer reports ignored `fmt.Errorf` results.

- [ ] **Step 6: Commit built-in command hardening**

```bash
git add ability/base_ability.go ability/base_ability_test.go ability/role_ability.go ability/role_ability_test.go data/base_data.go data/base_data_test.go init.go atom_test.go
git commit -m "refactor: harden built-in commands"
```

### Task 5: Document and verify the core lifecycle

**Files:**
- Modify: `README.md`
- Modify: `go.mod`
- Modify: `go.sum`

- [ ] **Step 1: Replace empty README sections with executable examples**

Document:

- the project is a Go library, not a `package main` program;
- `InitAtom()` returns `*types.Atom`;
- registration errors must be handled;
- `PreRunAtom` performs dependency checks and mounting;
- a long-running Ability implements `Run(context.Context, *types.Atom) error` and exits on `ctx.Done()`;
- `RunAtom` blocks while Runners are active and returns cancellation, Runner, timeout, or cleanup errors;
- registration is frozen after pre-run;
- TimeAbility security details are linked to the second plan rather than described before implementation.

The Runner example must compile as written and use a channel or ticker that is stopped on context cancellation.

- [ ] **Step 2: Normalize module metadata and formatting**

Run:

```bash
gofmt -w $(git ls-files '*.go')
go mod tidy
```

Confirm `github.com/beevik/ntp v1.5.0` is a direct requirement and no production dependency beyond the existing modules was added.

- [ ] **Step 3: Run the complete core gate**

Run:

```bash
gofmt -l $(git ls-files '*.go')
go mod tidy -diff
go vet ./...
go test -count=1 ./...
go test -race -count=1 ./...
git diff --check
```

Expected: every command exits 0; `gofmt -l` and `go mod tidy -diff` print nothing. Tests perform no live HTTP or NTP request.

- [ ] **Step 4: Commit core documentation and module cleanup**

```bash
git add README.md go.mod go.sum
git commit -m "docs: document core lifecycle"
```

- [ ] **Step 5: Record the handoff baseline for the TimeAbility plan**

Run:

```bash
git status --short
git log -5 --oneline
```

Expected: status is empty and the five commits from this plan are visible. Proceed to `docs/superpowers/plans/2026-08-09-time-ability-hardening.md`.
