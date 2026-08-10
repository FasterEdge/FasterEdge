package types

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestCommandOutputSuccessReflectsError(t *testing.T) {
	if !(CommandOutput{Value: "ok"}).Success() {
		t.Fatal("output without an error should succeed")
	}
	if (CommandOutput{Err: errors.New("failed")}).Success() {
		t.Fatal("output with an error should not succeed")
	}
}

func TestComponentErrorsSupportIsAndAs(t *testing.T) {
	base := errors.New("dependency unavailable")
	wrapped := &ComponentError{Name: "cache", Phase: "mount", Err: base}
	if !errors.Is(wrapped, base) {
		t.Fatal("ComponentError should unwrap its cause")
	}
	var gotComponent *ComponentError
	if !errors.As(wrapped, &gotComponent) || gotComponent.Name != "cache" {
		t.Fatal("errors.As should recover ComponentError")
	}

	panicErr := &ComponentPanicError{Name: "cache", Phase: "check", Value: "boom", Stack: []byte("stack"), Err: base}
	if !errors.Is(panicErr, base) {
		t.Fatal("ComponentPanicError should unwrap its cause")
	}
	var gotPanic *ComponentPanicError
	if !errors.As(panicErr, &gotPanic) || len(gotPanic.Stack) == 0 {
		t.Fatal("errors.As should recover ComponentPanicError")
	}

	nameErr := &ComponentNameChangedError{Name: "old", Current: "new"}
	if !errors.Is(nameErr, ErrComponentNameChanged) {
		t.Fatal("name-change error should unwrap sentinel")
	}
	var gotName *ComponentNameChangedError
	if !errors.As(nameErr, &gotName) || gotName.Current != "new" {
		t.Fatal("errors.As should recover ComponentNameChangedError")
	}

	timeoutErr := &ShutdownTimeoutError{Timeout: time.Second, Phase: "run", Components: []string{"zeta", "alpha"}}
	if !errors.Is(timeoutErr, ErrShutdownTimeout) {
		t.Fatal("timeout error should unwrap sentinel")
	}
	var gotTimeout *ShutdownTimeoutError
	if !errors.As(timeoutErr, &gotTimeout) {
		t.Fatal("errors.As should recover ShutdownTimeoutError")
	}
	if !reflect.DeepEqual(gotTimeout.Components, []string{"alpha", "zeta"}) {
		t.Fatalf("components = %v, want sorted copy", gotTimeout.Components)
	}
}
