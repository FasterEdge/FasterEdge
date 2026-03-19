package FasterEdge

import (
	"testing"

	"github.com/FasterEdge/FasterEdge/ability"
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

func (b *TestData) Command(atmo types.Atom, act string, args struct{}) types.DataOutput[struct{}] {
	return types.DataOutput[struct{}]{Name: act, Success: false}
}

func (b *TestData) CommandAny(atmo types.Atom, act string, args any) types.DataOutput[any] {
	out := b.Command(atmo, act, struct{}{})
	return types.DataOutput[any]{Name: out.Name, Value: out.Value, Success: out.Success, Error: out.Error}
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

func (b *TestAbility) Command(atmo types.Atom, act string, args struct{}) types.AbilityOutput[struct{}] {
	return types.AbilityOutput[struct{}]{Name: act, Success: false}
}

func (b *TestAbility) CommandAny(atmo types.Atom, act string, args any) types.AbilityOutput[any] {
	out := b.Command(atmo, act, struct{}{})
	return types.AbilityOutput[any]{Name: out.Name, Value: out.Value, Success: out.Success, Error: out.Error}
}

func TestAtom(t *testing.T) {
	atom := InitAtom()
	atom.AddData(&TestData{})
	atom.AddAbility(&TestAbility{})
	atom.AddAbility(&ability.TimeAbility{})
	RunAtom(atom)
}
