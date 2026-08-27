package FasterEdge

// Batch 6 combination tests. These exercise the new types/ surface
// (Reset, Remove, Health, Metrics, MultiEventSink, AddEventSink,
// CommandNames) together with the now-Runner TimeAbility on a real
// InitStandardAtom stack. The file is intentionally parallel to
// integration_test.go to keep responsibilities separate.

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/FasterEdge/FasterEdge/ability"
	"github.com/FasterEdge/FasterEdge/types"
)

func TestBatch6FullStackLifecycle(t *testing.T) {
	atom := InitStandardAtom()

	// Attach an EventRecorder + MetricsCollector via MultiEventSink so we
	// can assert that all observers see the same lifecycle.
	rec := &types.EventRecorder{}
	metrics := types.NewMetricsCollector()
	multi := types.NewMultiEventSink(rec, metrics)
	if err := atom.SetEventSink(multi); err != nil {
		t.Fatal(err)
	}
	if err := atom.AddEventSink(&types.EventRecorder{}); err != nil {
		t.Fatal(err)
	}

	if err := atom.PreRun(); err != nil {
		t.Fatalf("PreRun: %v", err)
	}

	// Health report should show every component as OK or Skipped (no
	// HealthChecker adopters beyond TimeAbility yet, but TimeAbility has
	// been synced by the next command).
	ta, _ := atom.Ability("TimeAbility")
	if out := ta.Command(atom, ability.TimeCommandSyncSystem, nil); out.Err != nil {
		t.Fatal(out.Err)
	}

	report, err := atom.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	for _, c := range report.Components {
		if c.Name == "TimeAbility" && !c.OK {
			t.Fatalf("TimeAbility not OK: %+v", c)
		}
	}

	// CommandNames must include TimeAbility's catalogue.
	names := atom.CommandNames()
	if got := names["TimeAbility"]; len(got) < 6 {
		t.Fatalf("TimeAbility commands too few: %v", got)
	}

	// MetricsCollector saw the mount/check events.
	snap := metrics.Snapshot()
	if snap.Counts["mount:success"] == 0 {
		t.Fatalf("metrics: %+v", snap.Counts)
	}
	if len(rec.Events()) == 0 {
		t.Fatal("EventRecorder saw no events")
	}

	// Run the atom — TimeAbility is now a Runner. Cancel after a short
	// delay and confirm the supervision path completes cleanly.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- atom.RunAll(ctx, time.Second) }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("RunAll: %v", err)
	}
	if atom.State() != types.AtomStopped {
		t.Fatalf("state after run=%v", atom.State())
	}
	if report, _ := atom.Health(context.Background()); report.Components[0].OK {
		t.Fatal("post-stop Health should not be OK")
	}

	// Reset to reuse the atom. Registered components must survive.
	if err := atom.Reset(); err != nil {
		t.Fatal(err)
	}
	if atom.State() != types.AtomCreated {
		t.Fatalf("state after reset=%v", atom.State())
	}
	if _, ok := atom.Ability("TimeAbility"); !ok {
		t.Fatal("TimeAbility dropped after reset")
	}

	// Second lifecycle round-trip.
	if err := atom.PreRun(); err != nil {
		t.Fatalf("second PreRun: %v", err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan error, 1)
	go func() { done2 <- atom.RunAll(ctx2, time.Second) }()
	time.Sleep(30 * time.Millisecond)
	cancel2()
	if err := <-done2; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("second RunAll: %v", err)
	}
	if err := atom.Reset(); err != nil {
		t.Fatal(err)
	}
}

func TestBatch6RemoveAfterResetTriggersDependencyError(t *testing.T) {
	atom := InitStandardAtom()
	if err := atom.PreRun(); err != nil {
		t.Fatal(err)
	}
	if err := atom.UnmountAll(context.Background(), time.Second); err != nil {
		t.Fatal(err)
	}
	if err := atom.Reset(); err != nil {
		t.Fatal(err)
	}
	if err := atom.RemoveData("NetMapData"); err != nil {
		t.Fatal(err)
	}
	if err := atom.PreRun(); !errors.Is(err, types.ErrMissingDependency) {
		t.Fatalf("PreRun after removing NetMapData: %v", err)
	}
}

func TestBatch6MetricsCompositionIsolated(t *testing.T) {
	// Two atoms, two collectors — confirm data does not cross-contaminate.
	makeMounted := func() (*types.Atom, *types.MetricsCollector) {
		atom := InitStandardAtom()
		m := types.NewMetricsCollector()
		if err := atom.SetEventSink(m); err != nil {
			t.Fatal(err)
		}
		if err := atom.PreRun(); err != nil {
			t.Fatal(err)
		}
		ta, _ := atom.Ability("TimeAbility")
		ta.Command(atom, ability.TimeCommandSyncSystem, nil)
		return atom, m
	}
	atom1, m1 := makeMounted()
	defer atom1.Reset()
	atom2, m2 := makeMounted()
	defer atom2.Reset()
	snap1 := m1.Snapshot()
	snap2 := m2.Snapshot()
	if snap1.Counts["mount:success"] == 0 || snap2.Counts["mount:success"] == 0 {
		t.Fatal("expected non-zero mount counts on both collectors")
	}
	// Mutating one snapshot must not affect the other (defensive copy).
	snap1.Counts["synthetic"] = 999
	if _, ok := snap2.Counts["synthetic"]; ok {
		t.Fatal("snapshots share backing storage")
	}
}

func TestBatch6CommandNamesCatalogueIsSorted(t *testing.T) {
	atom := InitStandardAtom()
	names := atom.CommandNames()
	ta := names["TimeAbility"]
	if !sort.StringsAreSorted(ta) {
		t.Fatalf("TimeAbility commands not sorted: %v", ta)
	}
	// Every name returned must be accepted by Command (or ErrUnsupported
	// for backwards-compat). Pick a known command and verify it works.
	out := atom.Command("TimeAbility", ability.TimeCommandSyncSystem, nil)
	if out.Err != nil {
		t.Fatalf("sync_system: %v", out.Err)
	}
}

func TestBatch6EventRecorderSeesRunAndUnmount(t *testing.T) {
	atom := InitStandardAtom()
	rec := &types.EventRecorder{}
	if err := atom.SetEventSink(rec); err != nil {
		t.Fatal(err)
	}
	if err := atom.PreRun(); err != nil {
		t.Fatal(err)
	}
	ta, _ := atom.Ability("TimeAbility")
	if out := ta.Command(atom, ability.TimeCommandSyncSystem, nil); out.Err != nil {
		t.Fatal(out.Err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- atom.RunAll(ctx, time.Second) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done

	phases := map[types.LifecyclePhase]int{}
	for _, e := range rec.Events() {
		phases[e.Phase]++
	}
	if phases[types.PhaseMount] == 0 {
		t.Fatal("no mount events")
	}
	if phases[types.PhaseRun] == 0 {
		t.Fatal("no run events")
	}
	if phases[types.PhaseUnmount] == 0 {
		t.Fatal("no unmount events")
	}
}

func TestBatch6HealthAfterResetIsHealthy(t *testing.T) {
	atom := InitStandardAtom()
	ta, _ := atom.Ability("TimeAbility")
	ta.Command(atom, ability.TimeCommandSyncSystem, nil)
	if err := atom.PreRun(); err != nil {
		t.Fatal(err)
	}
	if err := atom.UnmountAll(context.Background(), time.Second); err != nil {
		t.Fatal(err)
	}
	if err := atom.Reset(); err != nil {
		t.Fatal(err)
	}
	// After reset, the atom is back to AtomCreated — health should be
	// skipped for every component.
	report, err := atom.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if report.State != types.AtomCreated {
		t.Fatalf("state=%v", report.State)
	}
	for _, c := range report.Components {
		if !c.Skipped {
			t.Fatalf("expected skipped, got %+v", c)
		}
	}
}

func TestBatch6UnhealthyErrorChainPreservesOriginals(t *testing.T) {
	sentinel := errors.New("not connected")
	atom := InitStandardAtom()
	// Register a synthetic ability that fails HealthCheck.
	if err := atom.AddAbility(&unhealthyAbility{name: "fake", err: sentinel}); err != nil {
		t.Fatal(err)
	}
	if err := atom.PreRun(); err != nil {
		t.Fatal(err)
	}
	_, err := atom.Health(context.Background())
	if !errors.Is(err, sentinel) {
		t.Fatalf("err=%v", err)
	}
	if !errors.Is(err, types.ErrUnhealthy) {
		t.Fatalf("err=%v", err)
	}
}

// unhealthyAbility is a HealthChecker that always reports failure. It lives
// here so batch6 integration tests can exercise the UnhealthyError chain
// without polluting the public test fixtures.
type unhealthyAbility struct {
	name string
	err  error
}

func (u *unhealthyAbility) GetName() string                          { return u.name }
func (u *unhealthyAbility) Describe() string                         { return u.name }
func (u *unhealthyAbility) Check(*types.Atom) error                  { return nil }
func (u *unhealthyAbility) Mount(*types.Atom) error                  { return nil }
func (u *unhealthyAbility) Command(*types.Atom, string, any) types.CommandOutput {
	return types.CommandOutput{Name: "noop"}
}
func (u *unhealthyAbility) HealthCheck(_ context.Context, _ *types.Atom) error {
	return u.err
}

// Compile-time assertions for the unhealthyAbility test fixture.
var (
	_ types.HealthChecker = (*unhealthyAbility)(nil)
)

func TestBatch6MultiEventSinkAttachesFromThirdParty(t *testing.T) {
	atom := InitStandardAtom()
	a := &countingSink{}
	b := &countingSink{}
	if err := atom.SetEventSink(a); err != nil {
		t.Fatal(err)
	}
	if err := atom.AddEventSink(b); err != nil {
		t.Fatal(err)
	}
	if err := atom.PreRun(); err != nil {
		t.Fatal(err)
	}
	if a.n == 0 || b.n == 0 {
		t.Fatalf("a=%d b=%d", a.n, b.n)
	}
}

type countingSink struct{ n int }

func (c *countingSink) ObserveLifecycle(types.LifecycleEvent) { c.n++ }

// Smoke: ensure a fresh atom with the new event-sink fan-out does not
// break the existing role chain.
func TestBatch6CompatibleWithRoleChain(t *testing.T) {
	atom := InitStandardAtom()
	if err := atom.SetEventSink(&types.EventRecorder{}); err != nil {
		t.Fatal(err)
	}
	if err := atom.PreRun(); err != nil {
		t.Fatal(err)
	}
	ra, _ := atom.Ability("RoleAbility")
	out := ra.Command(atom, ability.CommandSetRole, ability.RoleAbilityArgs{Role: "edge"})
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	if v := ra.Command(atom, ability.CommandGetRole, nil).Value; v != "edge" {
		t.Fatalf("role=%v", v)
	}
}

// Touch strings to keep the import even if some tests are filtered.
var _ = strings.TrimSpace