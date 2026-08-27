package ability

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/FasterEdge/FasterEdge/types"
)

type taskFakeClock struct {
	mu   sync.Mutex
	wall time.Time
	mono time.Duration
}

func (c *taskFakeClock) Now() time.Time           { c.mu.Lock(); defer c.mu.Unlock(); return c.wall }
func (c *taskFakeClock) Monotonic() time.Duration { c.mu.Lock(); defer c.mu.Unlock(); return c.mono }

func TestTimeAbilityStrictArguments(t *testing.T) {
	a := new(TimeAbility)
	for _, tc := range []struct {
		cmd  string
		args any
	}{
		{TimeCommandSyncManual, nil}, {TimeCommandSyncManual, TimeSyncManualArgs{}},
		{TimeCommandSyncSystem, TimeSyncManualArgs{}}, {TimeCommandLastSync, struct{}{}},
		{"unknown", nil},
	} {
		out := a.Command(nil, tc.cmd, tc.args)
		if tc.cmd == "unknown" {
			if !errors.Is(out.Err, types.ErrUnsupportedCommand) {
				t.Errorf("%s: got %v", tc.cmd, out.Err)
			}
		} else if !errors.Is(out.Err, types.ErrInvalidArguments) {
			t.Errorf("%s: got %v", tc.cmd, out.Err)
		}
	}
}

func TestTimeAbilityMonotonicElapsed(t *testing.T) {
	c := &taskFakeClock{wall: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)}
	a := &TimeAbility{clock: c}
	want := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	if out := a.Command(nil, TimeCommandSyncManual, TimeSyncManualArgs{Value: want.Format(time.RFC3339)}); out.Err != nil {
		t.Fatal(out.Err)
	}
	c.mu.Lock()
	c.mono += 2 * time.Second
	c.wall = c.wall.Add(-24 * time.Hour)
	c.mu.Unlock()
	out := a.Command(nil, TimeCommandGetTime, nil)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	got := out.Value.(TimeSnapshot).Time
	if !got.Equal(want.Add(2 * time.Second)) {
		t.Fatalf("got %s", got)
	}
}

func TestTimeAbilityImplementsRunner(t *testing.T) {
	var _ types.Runner = (*TimeAbility)(nil)
	var _ types.CommandLister = (*TimeAbility)(nil)
	var _ types.HealthChecker = (*TimeAbility)(nil)
}

func TestTimeAbilityRunMonotonicStaysAliveUntilCancelled(t *testing.T) {
	c := &taskFakeClock{wall: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)}
	a := &TimeAbility{clock: c, runMode: TimeRunModeMonotonic, interval: 5 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, nil) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return")
	}
}

func TestTimeAbilityRunTickerFiresSyncSystem(t *testing.T) {
	c := &taskFakeClock{wall: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)}
	a := &TimeAbility{clock: c, runMode: TimeRunModeTicker, interval: 5 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, nil) }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run err=%v", err)
	}
	a.mu.RLock()
	src := a.lastSource
	a.mu.RUnlock()
	if src != "system" {
		t.Fatalf("lastSource=%s want system", src)
	}
}

func TestTimeAbilityRunHonorsImmediateCtxCancel(t *testing.T) {
	c := &taskFakeClock{wall: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)}
	a := &TimeAbility{clock: c, runMode: TimeRunModeMonotonic, interval: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, nil) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestTimeAbilityRunRejectsZeroInterval(t *testing.T) {
	c := &taskFakeClock{wall: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)}
	a := &TimeAbility{clock: c, runMode: TimeRunModeTicker, interval: 0}
	if err := a.Run(context.Background(), nil); !errors.Is(err, types.ErrInvalidArguments) {
		t.Fatalf("Run err=%v", err)
	}
}

func TestTimeAbilityHealthCheck(t *testing.T) {
	c := &taskFakeClock{wall: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)}
	a := &TimeAbility{clock: c}
	if err := a.HealthCheck(context.Background(), nil); !errors.Is(err, ErrTimeNotSynced) {
		t.Fatalf("unsynced: err=%v", err)
	}
	if out := a.Command(nil, TimeCommandSyncSystem, nil); out.Err != nil {
		t.Fatal(out.Err)
	}
	if err := a.HealthCheck(context.Background(), nil); err != nil {
		t.Fatalf("synced: err=%v", err)
	}
}

func TestTimeAbilityListCommandsExactMatch(t *testing.T) {
	a := new(TimeAbility)
	got := a.ListCommands()
	want := []string{
		TimeCommandSyncNetwork, TimeCommandSyncManual, TimeCommandSyncSystem,
		TimeCommandSyncNTP, TimeCommandLastSync, TimeCommandGetTime,
		TimeCommandConfigureRun,
	}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("at %d: %s vs %s", i, got[i], want[i])
		}
	}
}
