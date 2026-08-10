package types

import (
	"errors"
	"sync"
	"testing"
)

type testComponent struct{ name string }

func (c *testComponent) GetName() string                          { return c.name }
func (c *testComponent) Describe() string                         { return "" }
func (c *testComponent) Check(*Atom) error                        { return nil }
func (c *testComponent) Mount(*Atom) error                        { return nil }
func (c *testComponent) Command(*Atom, string, any) CommandOutput { return CommandOutput{} }

func TestAtomRejectsInvalidAndDuplicateComponents(t *testing.T) {
	a := &Atom{}
	if err := a.AddData((*testComponent)(nil)); !errors.Is(err, ErrNilComponent) {
		t.Fatalf("nil: %v", err)
	}
	var nilAtom *Atom
	if err := nilAtom.AddAbility(&testComponent{name: "x"}); !errors.Is(err, ErrNilAtom) {
		t.Fatalf("nil atom: %v", err)
	}
	if err := a.AddData(&testComponent{name: " worker "}); !errors.Is(err, ErrInvalidComponentName) {
		t.Fatalf("name: %v", err)
	}
	c := &testComponent{name: "x"}
	if err := a.AddData(c); err != nil {
		t.Fatal(err)
	}
	if err := a.AddData(c); !errors.Is(err, ErrDuplicateComponent) {
		t.Fatalf("duplicate: %v", err)
	}
}

func TestAtomSnapshotsAndConcurrentAdd(t *testing.T) {
	a := &Atom{}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); _ = a.AddData(&testComponent{name: string(rune('a' + i))}) }(i)
	}
	wg.Wait()
	s := a.AllData()
	delete(s, "a")
	if _, ok := a.Data("a"); !ok {
		t.Fatal("snapshot leaked internal map")
	}
}

func TestAtomSetNameLifecycleAndState(t *testing.T) {
	a := &Atom{}
	if a.State() != AtomCreated {
		t.Fatalf("state=%v", a.State())
	}
	if err := a.SetName("atom"); err != nil {
		t.Fatal(err)
	}
	if a.GetName() != "atom" {
		t.Fatal(a.GetName())
	}
	if err := a.SetName("other "); !errors.Is(err, ErrInvalidComponentName) {
		t.Fatal(err)
	}
}
