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
