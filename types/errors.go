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
)

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
