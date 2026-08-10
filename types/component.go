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
