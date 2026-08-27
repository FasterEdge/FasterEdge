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

// Command preserves the legacy dispatcher shape while routing through the
// cancellation-aware implementation.
func (a *Atom) Command(component, command string, args any) CommandOutput {
	return a.CommandContext(context.Background(), component, command, args)
}

// CommandContext dispatches to data or ability by name, prefers an optional
// ContextCommander, and recovers panics without changing Component.Command.
func (a *Atom) CommandContext(ctx context.Context, component, command string, args any) (out CommandOutput) {
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
