// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package types

import (
	"context"
	"fmt"
)

// CommandOutput is the result of invoking a component command.
type CommandOutput struct {
	Name  string
	Value any
	Err   error
}

// Success reports whether the command completed without an error.
func (o CommandOutput) Success() bool { return o.Err == nil }

// Command preserves the legacy dispatcher shape for trusted in-process calls.
func (a *Atom) Command(component, command string, args any) CommandOutput {
	return a.CommandContext(context.Background(), component, command, args)
}

// CommandContext dispatches a trusted in-process call without authentication.
// Remote transports should use AuthenticatedCommandContext instead.
func (a *Atom) CommandContext(ctx context.Context, component, command string, args any) CommandOutput {
	return a.dispatchContext(ctx, component, command, args)
}

// AuthenticatedCommand validates credential with the Atom's configured
// CommandAuthenticator before dispatching the target command.
func (a *Atom) AuthenticatedCommand(credential any, component, command string, args any) CommandOutput {
	return a.AuthenticatedCommandContext(context.Background(), credential, component, command, args)
}

// AuthenticatedCommandContext is the security boundary intended for HTTP,
// MQTT, RPC, and other remote adapters. Authentication happens before target
// lookup so failed callers cannot use it to enumerate registered components.
func (a *Atom) AuthenticatedCommandContext(ctx context.Context, credential any, component, command string, args any) (out CommandOutput) {
	out.Name = command
	if a == nil {
		out.Err = ErrNilAtom
		return
	}
	if ctx == nil {
		out.Err = ErrNilContext
		return
	}
	a.mu.RLock()
	auth := a.commandAuthenticator
	a.mu.RUnlock()
	if auth == nil || isNilInterface(auth) {
		out.Err = ErrAuthenticationRequired
		return
	}
	if err := ctx.Err(); err != nil {
		out.Err = err
		return
	}
	var authErr error
	var panicked bool
	var panicErr error
	func() {
		defer func() {
			if v := recover(); v != nil {
				panicked = true
				panicErr = NewComponentPanicError("command authenticator", "authenticate", v)
			}
		}()
		_, authErr = auth.AuthenticateCommand(ctx, a, credential, component, command, args)
	}()
	// 鉴权器 panic 与普通凭据错误保持可区分(旧实现统一折叠为哨兵,
	// ComponentPanicError 含 Stack 被构造后丢弃——鉴权后端崩溃被静默
	// 伪装成凭据错误, 无法告警)。只暴露哨兵 + 类型化错误, 不泄露 panic 值。
	if panicked {
		out.Err = fmt.Errorf("authenticate command: %w: %v", ErrAuthenticationFailed, panicErr)
		return
	}
	if authErr != nil {
		out.Err = fmt.Errorf("authenticate command: %w", ErrAuthenticationFailed)
		return
	}
	return a.dispatchContext(ctx, component, command, args)
}

// dispatchContext performs the shared post-authentication routing operation.
func (a *Atom) dispatchContext(ctx context.Context, component, command string, args any) (out CommandOutput) {
	out.Name = command
	if a == nil {
		out.Err = ErrNilAtom
		return
	}
	if ctx == nil {
		out.Err = ErrNilContext
		return
	}
	c, ok := a.component(component)
	if !ok {
		out.Err = fmt.Errorf("component %q: %w", component, ErrMissingDependency)
		return
	}
	_ = a.observe(component, command, PhaseCommand, func() (err error) {
		defer func() {
			if v := recover(); v != nil {
				err = NewComponentPanicError(component, "command", v)
				out = CommandOutput{Name: command, Err: err}
			}
		}()
		if cc, ok := c.(ContextCommander); ok {
			out = cc.CommandContext(ctx, a, command, args)
		} else {
			select {
			case <-ctx.Done():
				out = CommandOutput{Name: command, Err: ctx.Err()}
				return out.Err
			default:
			}
			out = c.Command(a, command, args)
		}
		if out.Name == "" {
			out.Name = command
		}
		return out.Err
	})
	return out
}

func (a *Atom) component(name string) (Component, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if c, ok := a.data[name]; ok {
		return c, true
	}
	if c, ok := a.abilities[name]; ok {
		return c, true
	}
	return nil, false
}
