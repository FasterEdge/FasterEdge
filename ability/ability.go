package ability

import "github.com/FasterEdge/FasterEdge/types"

type BaseAbility struct {
}

func (b *BaseAbility) Describe() string {
	return "BaseAbility is the base struct for all abilities. It provides common fields and methods that can be used by all abilities."
}

func (b *BaseAbility) Check(atmo types.Atom) bool {
	return false

}

func (b *BaseAbility) Mount(atmo types.Atom) bool {
	return false

}
