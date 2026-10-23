// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package types

import "context"

// Component is the common lifecycle and command contract implemented by
// registered data and abilities.
type Component interface {
	GetName() string
	Describe() string
	Check(*Atom) error
	Mount(*Atom) error
	Command(*Atom, string, any) CommandOutput
}

// Runner marks a component that has a supervised, long-running operation.
// 注意: RunAll 只监督 Ability 组件——Data 组件实现 Runner 时其 Run 永不被
// 调用(挂载数据泵请以 Ability 形态注册, 或自行起 goroutine)。
type Runner interface {
	Run(context.Context, *Atom) error
}

// Unmounter marks a component that releases resources during cleanup.
type Unmounter interface {
	Unmount(context.Context, *Atom) error
}

// ContextCommander optionally handles cancellation-aware commands. Components
// not implementing it continue to work through the legacy Command method.
type ContextCommander interface {
	CommandContext(context.Context, *Atom, string, any) CommandOutput
}

// CommandAuthenticator validates a credential before Atom dispatches a
// command through its authenticated entry point. The target is included so
// implementations may add authorization policies without changing Atom.
type CommandAuthenticator interface {
	AuthenticateCommand(context.Context, *Atom, any, string, string, any) (string, error)
}

// CommandLister is an optional interface components implement to declare the
// canonical names of their commands. Atom.CommandNames uses it to surface a
// machine-readable command catalogue without dispatching each command to
// probe it.
type CommandLister interface {
	ListCommands() []string
}

// HealthChecker is an optional interface components implement to expose an
// explicit liveness/readiness signal. Atom.Health uses it to aggregate
// per-component health into a single report.
type HealthChecker interface {
	HealthCheck(context.Context, *Atom) error
}

// Describer is implemented by the Atom itself (see Atom.Descriptions); it is
// declared here only to keep the Describe contract discoverable next to the
// Component interface. Atom.Descriptions aggregates Describe() across all
// registered data and abilities.
type Describer interface {
	Descriptions() map[string]string
}
