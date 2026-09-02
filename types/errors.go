// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package types

import (
	"errors"
	"fmt"
	"runtime/debug"
	"sort"
	"strings"
	"time"
)

var (
	ErrNilAtom              = errors.New("atom is nil")
	ErrNilContext           = errors.New("context is nil")
	ErrNilComponent         = errors.New("component is nil")
	ErrInvalidComponentName = errors.New("component name is empty or invalid")
	ErrDuplicateComponent   = errors.New("component name is already registered")
	ErrComponentNameChanged = errors.New("component name changed after registration")
	ErrInvalidAtomState     = errors.New("atom lifecycle state does not allow this operation")
	// ErrInvalidState is retained as an alias for callers using the original
	// lifecycle vocabulary.
	ErrInvalidState           = ErrInvalidAtomState
	ErrInvalidArguments       = errors.New("invalid command arguments")
	ErrUnsupportedCommand     = errors.New("unsupported command")
	ErrInvalidShutdownTimeout = errors.New("invalid shutdown timeout")
	ErrShutdownTimeout        = errors.New("lifecycle shutdown timed out")
	ErrMissingDependency      = errors.New("component dependency is missing")
	ErrWrongDependencyType    = errors.New("component dependency has wrong type")
	ErrDependencyCycle        = errors.New("component dependency cycle")
	ErrUnhealthy              = errors.New("atom is not healthy")
	ErrNotMounted             = errors.New("component is not mounted")
	ErrAuthenticationRequired = errors.New("command authentication is required")
	ErrAuthenticationFailed   = errors.New("command authentication failed")
)

// DependencyError identifies an invalid dependency declaration or registration.
type DependencyError struct {
	Component  string
	Dependency Dependency
	ActualKind DependencyKind
	Err        error
}

func (e *DependencyError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if errors.Is(e.Err, ErrWrongDependencyType) {
		return fmt.Sprintf("component %q requires %s %q, found %s: %v", e.Component, e.Dependency.Kind, e.Dependency.Name, e.ActualKind, e.Err)
	}
	return fmt.Sprintf("component %q requires %s %q: %v", e.Component, e.Dependency.Kind, e.Dependency.Name, e.Err)
}
func (e *DependencyError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// DependencyCycleError reports a deterministic closed dependency path.
type DependencyCycleError struct{ Components []string }

func (e *DependencyCycleError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("dependency cycle %s: %v", strings.Join(e.Components, " -> "), ErrDependencyCycle)
}
func (e *DependencyCycleError) Unwrap() error { return ErrDependencyCycle }

// ComponentError annotates an error with the component and lifecycle phase
// in which it occurred.
type ComponentError struct {
	Name  string
	Phase string
	Err   error
}

func (e *ComponentError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s %s: %v", e.Phase, e.Name, e.Err)
}

func (e *ComponentError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// ComponentPanicError is returned when component code panics. Err can be
// used when a panic is being annotated alongside an underlying error.
type ComponentPanicError struct {
	Name  string
	Phase string
	Value any
	Stack []byte
	Err   error
}

func (e *ComponentPanicError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s %s panicked: %v", e.Phase, e.Name, e.Value)
}

func (e *ComponentPanicError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// NewComponentPanicError captures a panic with the current stack.
func NewComponentPanicError(name, phase string, value any) *ComponentPanicError {
	return &ComponentPanicError{Name: name, Phase: phase, Value: value, Stack: debug.Stack()}
}

// ComponentNameChangedError reports a component whose registered name no
// longer matches the name returned during lifecycle validation.
type ComponentNameChangedError struct {
	Name       string
	Registered string
	Expected   string
	Current    string
}

func (e *ComponentNameChangedError) Error() string {
	if e == nil {
		return "<nil>"
	}
	expected := e.Registered
	if expected == "" {
		expected = e.Expected
	}
	return fmt.Sprintf("component name changed from %q to %q: %v", expected, e.Current, ErrComponentNameChanged)
}

func (e *ComponentNameChangedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return ErrComponentNameChanged
}

// ShutdownTimeoutError describes components that remained active after the
// bounded shutdown interval. Components are normalized to an owned sorted
// copy whenever the error is observed.
type ShutdownTimeoutError struct {
	Timeout    time.Duration
	Phase      string
	Components []string
}

func (e *ShutdownTimeoutError) normalize() {
	if e == nil {
		return
	}
	components := append([]string(nil), e.Components...)
	sort.Strings(components)
	e.Components = components
}

func (e *ShutdownTimeoutError) Error() string {
	if e == nil {
		return "<nil>"
	}
	e.normalize()
	return fmt.Sprintf("%s after %s waiting for %s: %v", e.Phase, e.Timeout, strings.Join(e.Components, ", "), ErrShutdownTimeout)
}

func (e *ShutdownTimeoutError) Unwrap() error {
	if e == nil {
		return nil
	}
	e.normalize()
	return ErrShutdownTimeout
}

// UnhealthyError aggregates one or more failing component health reports.
// The aggregated Err field exists for callers that don't care about the
// individual components; the Components slice preserves the original
// errors so callers (and errors.Is) can introspect.
type UnhealthyError struct {
	Components []ComponentHealth
}

func (e *UnhealthyError) Error() string {
	if e == nil || len(e.Components) == 0 {
		return "<nil>"
	}
	names := make([]string, 0, len(e.Components))
	for _, c := range e.Components {
		if !c.OK && !c.Skipped {
			names = append(names, c.Name)
		}
	}
	sort.Strings(names)
	return fmt.Sprintf("atom unhealthy: %s: %v", strings.Join(names, ", "), ErrUnhealthy)
}

// Unwrap returns the aggregated ErrUnhealthy sentinel plus each component's
// underlying error. Implements the Go 1.20+ multi-error unwrap interface so
// errors.Is(err, ErrUnhealthy) and errors.Is(err, componentSentinel) both
// work transparently.
func (e *UnhealthyError) Unwrap() []error {
	if e == nil || len(e.Components) == 0 {
		return nil
	}
	out := make([]error, 0, len(e.Components)+1)
	out = append(out, ErrUnhealthy)
	for _, c := range e.Components {
		if c.Err != nil {
			out = append(out, c.Err)
		}
	}
	return out
}
