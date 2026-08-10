package ability

import (
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
