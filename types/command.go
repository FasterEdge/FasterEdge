// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package types

import (
	"context"
	"errors"
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

// ErrAuthorizationDenied reports that the credential was valid but does not
// authorize the requested component/command. 刻意与 ErrAuthenticationFailed
// 区分: 认证失败(401)与授权拒绝(403)对平台告警/审计是两类事件。定义在此文件
// 而非 errors.go —— 它只由命令分派边界的鉴权流程产生。
var ErrAuthorizationDenied = errors.New("command authorization denied")

// AuthenticatedCommandContext is the security boundary intended for HTTP,
// MQTT, RPC, and other remote adapters. The pipeline is:
//
//	authenticate + authorize (identity, scope) → dispatch (component lookup)
//
// 认证与授权都在目标组件查找之前完成: 作用域外的调用无法用来枚举已注册组件。
// 授权拒绝以 ErrAuthorizationDenied 返回, 与凭据错误(ErrAuthenticationFailed)
// 可区分——鉴权器用 %w 包装该哨兵即可让平台识别 403 而非 401。
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
	// 伪装成凭据错误, 无法告警)。%w 包装 panicErr: 文本泄露面与 %v 相同
	// (错误串都渲染 panic 值), 但 errors.As/Is 可识别 ComponentPanicError
	// 类型与 Stack 做告警聚合。凭据错误分支(下方)刻意丢弃详情只留哨兵。
	if panicked {
		out.Err = fmt.Errorf("authenticate command: %w: %w", ErrAuthenticationFailed, panicErr)
		return
	}
	if authErr != nil {
		// 授权拒绝(作用域外的 component/command)与凭据错误(验签失败)在
		// 上层折叠为同一 401 前, 先把 403 哨兵保留在错误链上: 平台可
		// errors.Is(err, ErrAuthorizationDenied) 区分两类事件。
		if errors.Is(authErr, ErrAuthorizationDenied) {
			out.Err = fmt.Errorf("authorize command: %w: %v", ErrAuthorizationDenied, errors.Unwrap(authErr))
			return
		}
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
		// 目标组件不存在同样是一次"失败的 command 相位": 旧实现在此处直接
		// 返回, metrics 里连 command:failure 都没有, 分派失败对埋点不可见。
		a.observeGateFailure(component, PhaseCommand, out.Err)
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
