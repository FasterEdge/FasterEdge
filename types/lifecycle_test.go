package types

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
)

type lifecycleComponent struct {
	name     string
	kind     string
	mu       sync.Mutex
	log      *[]string
	checkErr error
	mountErr error
}

func (c *lifecycleComponent) GetName() string  { c.mu.Lock(); defer c.mu.Unlock(); return c.name }
func (c *lifecycleComponent) Describe() string { return "" }
func (c *lifecycleComponent) Check(*Atom) error {
	if c.log != nil {
		*c.log = append(*c.log, "check:"+c.name)
	}
	return c.checkErr
}
func (c *lifecycleComponent) Mount(*Atom) error {
	if c.log != nil {
		*c.log = append(*c.log, "mount:"+c.name)
	}
	return c.mountErr
}
func (c *lifecycleComponent) Command(*Atom, string, any) CommandOutput { return CommandOutput{} }
func (c *lifecycleComponent) Unmount(context.Context, *Atom) error {
	if c.log != nil {
		*c.log = append(*c.log, "unmount:"+c.name)
	}
	return nil
}

func TestPreRunDeterministicOrderAndState(t *testing.T) {
	var log []string
	a := &Atom{}
	if err := a.AddAbility(&lifecycleComponent{name: "z", log: &log}); err != nil {
		t.Fatal(err)
	}
	if err := a.AddData(&lifecycleComponent{name: "b", log: &log}); err != nil {
		t.Fatal(err)
	}
	if err := a.AddData(&lifecycleComponent{name: "a", log: &log}); err != nil {
		t.Fatal(err)
	}
	if err := a.PreRun(); err != nil {
		t.Fatal(err)
	}
	want := []string{"check:a", "check:b", "check:z", "mount:a", "mount:b", "mount:z"}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("order=%v want=%v", log, want)
	}
	if a.State() != AtomMounted {
		t.Fatalf("state=%v", a.State())
	}
	if err := a.PreRun(); !errors.Is(err, ErrInvalidAtomState) {
		t.Fatalf("repeat=%v", err)
	}
}

func TestPreRunCheckFailureMountsNothing(t *testing.T) {
	var log []string
	a := &Atom{}
	if err := a.AddData(&lifecycleComponent{name: "bad", log: &log, checkErr: errors.New("no")}); err != nil {
		t.Fatal(err)
	}
	if err := a.PreRun(); err == nil {
		t.Fatal("expected check error")
	}
	if len(log) != 1 || log[0] != "check:bad" {
		t.Fatalf("callbacks=%v", log)
	}
	if a.State() != AtomCreated {
		t.Fatalf("state=%v", a.State())
	}
}
