package data

import "github.com/FasterEdge/FasterEdge/types"

func init() {
	dataPool["faster_edge_version"] = "1.0.20260225"
}

type BaseData struct {
}

func (b *BaseData) Describe() string {
	return "BaseAbility is the base struct for all abilities. It provides common fields and methods that can be used by all abilities."
}

func (b *BaseData) Check(atmo types.Atom) bool {
	return false

}

func (b *BaseData) Mount(atmo types.Atom) bool {
	return false
}
