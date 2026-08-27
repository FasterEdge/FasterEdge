package types

import "fmt"

// DependencyKind identifies which component registry must provide a dependency.
type DependencyKind uint8

const (
	DependencyData DependencyKind = iota
	DependencyAbility
)

func (k DependencyKind) String() string {
	switch k {
	case DependencyData:
		return "data"
	case DependencyAbility:
		return "ability"
	default:
		return fmt.Sprintf("DependencyKind(%d)", k)
	}
}

// Dependency declares a named lifecycle prerequisite.
type Dependency struct {
	Kind DependencyKind
	Name string
}

// DependencyProvider is optional. Components implementing it participate in
// preflight validation and deterministic topological lifecycle ordering.
type DependencyProvider interface {
	Dependencies() []Dependency
}
