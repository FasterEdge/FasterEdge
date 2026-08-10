package FasterEdge

import "testing"

func TestInitAtomRegistersBaseComponents(t *testing.T) {
	a := InitAtom()
	if _, ok := a.Data("BaseData"); !ok {
		t.Fatal("BaseData not registered")
	}
	if _, ok := a.Ability("BaseAbility"); !ok {
		t.Fatal("BaseAbility not registered")
	}
}
