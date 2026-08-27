package types

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type depComponent struct {
	testComponent
	deps  []Dependency
	calls *[]string
}

func (c *depComponent) Dependencies() []Dependency { return append([]Dependency(nil), c.deps...) }
func (c *depComponent) Mount(*Atom) error          { *c.calls = append(*c.calls, c.name); return nil }

type contextComponent struct{ testComponent }

func (c *contextComponent) CommandContext(ctx context.Context, _ *Atom, act string, _ any) CommandOutput {
	return CommandOutput{Name: act, Err: ctx.Err()}
}

type panicCommandComponent struct{ testComponent }

func (c *panicCommandComponent) Command(*Atom, string, any) CommandOutput { panic("boom") }

func TestDependencyTopologicalOrder(t *testing.T) {
	calls := []string{}
	a := &Atom{}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(a.AddAbility(&depComponent{testComponent: testComponent{name: "consumer"}, deps: []Dependency{{Kind: DependencyData, Name: "store"}}, calls: &calls}))
	must(a.AddData(&depComponent{testComponent: testComponent{name: "store"}, calls: &calls}))
	must(a.PreRun())
	if !reflect.DeepEqual(calls, []string{"store", "consumer"}) {
		t.Fatalf("order=%v", calls)
	}
}
func TestDependencyErrors(t *testing.T) {
	for _, tc := range []struct {
		name     string
		setup    func(*Atom)
		sentinel error
	}{
		{"missing", func(a *Atom) {
			_ = a.AddAbility(&depComponent{testComponent: testComponent{name: "x"}, deps: []Dependency{{Kind: DependencyData, Name: "missing"}}, calls: &[]string{}})
		}, ErrMissingDependency},
		{"wrong", func(a *Atom) {
			_ = a.AddAbility(&depComponent{testComponent: testComponent{name: "x"}, deps: []Dependency{{Kind: DependencyData, Name: "same"}}, calls: &[]string{}})
			_ = a.AddAbility(&testComponent{name: "same"})
		}, ErrWrongDependencyType},
		{"cycle", func(a *Atom) {
			calls := []string{}
			_ = a.AddAbility(&depComponent{testComponent: testComponent{name: "a"}, deps: []Dependency{{Kind: DependencyAbility, Name: "b"}}, calls: &calls})
			_ = a.AddAbility(&depComponent{testComponent: testComponent{name: "b"}, deps: []Dependency{{Kind: DependencyAbility, Name: "a"}}, calls: &calls})
		}, ErrDependencyCycle},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Atom{}
			tc.setup(a)
			if err := a.PreRun(); !errors.Is(err, tc.sentinel) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
func TestStateWaitSnapshotAndString(t *testing.T) {
	a := &Atom{}
	_ = a.AddData(&testComponent{name: "d"})
	if AtomCreated.String() != "created" {
		t.Fatal(AtomCreated.String())
	}
	text, _ := AtomRunning.MarshalText()
	if string(text) != "running" {
		t.Fatal(string(text))
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() { time.Sleep(time.Millisecond); a.setState(AtomMounted) }()
	if err := a.WaitState(ctx, AtomMounted); err != nil {
		t.Fatal(err)
	}
	s := a.Status()
	s.Components[0].Name = "changed"
	if a.Status().Components[0].Name != "d" {
		t.Fatal("snapshot mutable")
	}
}
func TestCommandContextFallbackPanicAndEvents(t *testing.T) {
	a := &Atom{}
	recorder := &EventRecorder{}
	_ = a.SetEventSink(recorder)
	legacy := &testComponent{name: "legacy"}
	_ = a.AddAbility(legacy)
	if out := a.CommandContext(context.Background(), "legacy", "x", nil); out.Err != nil {
		t.Fatal(out.Err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cc := &contextComponent{testComponent{name: "ctx"}}
	_ = a.AddAbility(cc)
	if out := a.CommandContext(ctx, "ctx", "x", nil); !errors.Is(out.Err, context.Canceled) {
		t.Fatal(out.Err)
	}
	pc := &panicCommandComponent{testComponent{name: "panic"}}
	_ = a.AddAbility(pc)
	var pe *ComponentPanicError
	if out := a.Command("panic", "x", nil); !errors.As(out.Err, &pe) {
		t.Fatalf("%v", out.Err)
	}
	if len(recorder.Events()) != 6 {
		t.Fatalf("events=%d", len(recorder.Events()))
	}
}
