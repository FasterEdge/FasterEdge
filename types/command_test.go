package types

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type testCommandAuthenticator struct {
	err    error
	called int
}

func (a *testCommandAuthenticator) AuthenticateCommand(ctx context.Context, _ *Atom, _ any, _, _ string, _ any) (string, error) {
	a.called++
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "node", a.err
}

func TestAuthenticatedCommandRequiresAndUsesAuthenticator(t *testing.T) {
	atom := &Atom{}
	target := &testComponent{name: "target"}
	if err := atom.AddAbility(target); err != nil {
		t.Fatal(err)
	}
	if out := atom.AuthenticatedCommand("credential", "target", "ping", nil); !errors.Is(out.Err, ErrAuthenticationRequired) {
		t.Fatalf("without authenticator: %v", out.Err)
	}
	auth := &testCommandAuthenticator{}
	if err := atom.SetCommandAuthenticator(auth); err != nil {
		t.Fatal(err)
	}
	if out := atom.AuthenticatedCommand("credential", "target", "ping", nil); out.Err != nil {
		t.Fatalf("authenticated dispatch: %v", out.Err)
	}
	if auth.called != 1 {
		t.Fatalf("auth calls = %d", auth.called)
	}
	auth.err = errors.New("denied")
	if out := atom.AuthenticatedCommand("credential", "missing", "ping", nil); !errors.Is(out.Err, ErrAuthenticationFailed) {
		t.Fatalf("authentication must precede target lookup: %v", out.Err)
	}
}

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

// panickingAuthenticator 的 AuthenticateCommand 直接 panic(测试鉴权 panic
// 分支 %w 链——旧实现用 %v 嵌入 panicErr, errors.As 无法识别
// ComponentPanicError, 鉴权后端崩溃被静默伪装成凭据错误)。
type panickingAuthenticator struct{}

func (panickingAuthenticator) AuthenticateCommand(_ context.Context, _ *Atom, _ any, _, _ string, _ any) (string, error) {
	panic("auth boom")
}

func TestAuthenticatedCommandPanicPreservesComponentPanicError(t *testing.T) {
	atom := &Atom{}
	if err := atom.SetCommandAuthenticator(panickingAuthenticator{}); err != nil {
		t.Fatal(err)
	}
	out := atom.AuthenticatedCommand("cred", "BaseAbility", "list", nil)
	if out.Err == nil {
		t.Fatal("should fail on authenticator panic")
	}
	if !errors.Is(out.Err, ErrAuthenticationFailed) {
		t.Fatalf("err=%v not ErrAuthenticationFailed", out.Err)
	}
	var cpe *ComponentPanicError
	if !errors.As(out.Err, &cpe) {
		t.Fatalf("err=%v not ComponentPanicError (panic chain broken)", out.Err)
	}
	if cpe.Phase != "authenticate" || cpe.Name != "command authenticator" {
		t.Fatalf("cpe=%+v", cpe)
	}
}
