package FasterEdge

import (
	"testing"

	"github.com/FasterEdge/FasterEdge/ability"
	"github.com/FasterEdge/FasterEdge/types"
)

type TestData struct{}

func (b *TestData) GetName() string            { return "TestData" }
func (b *TestData) Describe() string           { return "" }
func (b *TestData) Check(atmo types.Atom) bool { _ = atmo; return false }
func (b *TestData) Mount(atmo types.Atom) bool { _ = atmo; return false }
func (b *TestData) Command(atmo types.Atom, act string, args any) types.DataOutput {
	_ = atmo
	_ = args
	return types.DataOutput{Name: act, Success: false}
}

type TestAbility struct{}

func (b *TestAbility) GetName() string            { return "TestAbility" }
func (b *TestAbility) Describe() string           { return "" }
func (b *TestAbility) Check(atmo types.Atom) bool { _ = atmo; return false }
func (b *TestAbility) Mount(atmo types.Atom) bool { _ = atmo; return false }
func (b *TestAbility) Command(atmo types.Atom, act string, args any) types.AbilityOutput {
	_ = atmo
	_ = args
	return types.AbilityOutput{Name: act, Success: false}
}

func TestTimeAtom(t *testing.T) {
	atom := InitAtom()
	atom.AddData(&TestData{})
	atom.AddAbility(&TestAbility{})
	atom.AddAbility(&ability.TimeAbility{})
	PreRunAtom(atom)
	atom.GetAllAbility()["TimeAbility"].Command(atom, "sync_ntp", nil)
	atom.GetAllAbility()["TimeAbility"].Command(atom, "get_time", nil)
}

func TestSendUnixSocket(t *testing.T) {

}

func TestReceiveUnixSocket(t *testing.T) {

}
