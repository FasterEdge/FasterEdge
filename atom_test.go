package FasterEdge

import (
	"testing"

	"github.com/FasterEdge/FasterEdge/types"
)

type TestData struct {
}

func (b *TestData) GetName() string { return "TestData" }

func (b *TestData) Describe() string { return "" }

func (b *TestData) Check(atmo types.Atom) bool {
	return false
}

func (b *TestData) Mount(atmo types.Atom) bool {
	return false
}

func (b *TestData) Command(atmo types.Atom, act string, args ...string) bool {
	return false
}

type TestAbility struct {
}

func (b *TestAbility) GetName() string {
	return "TestAbility"
}

func (b *TestAbility) Describe() string {
	return ""
}

func (b *TestAbility) Check(atmo types.Atom) bool {
	return false

}

func (b *TestAbility) Mount(atmo types.Atom) bool {
	return false

}

func (b *TestAbility) Command(atmo types.Atom, act string, args ...string) bool {
	return false
}

func TestAtom(t *testing.T) {
	atom := InitAtom()
	atom.AddData(&TestData{})
	atom.AddAbility(&TestAbility{})
	RunAtom(atom)
}
