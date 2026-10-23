package ability

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestTimeAbilityRunNormalizesZeroInterval(t *testing.T) {
	// 零配置实例(init.go 注册后直接 RunAll): interval=0 必须被 ensureDefaults
	// 归一化而非拒绝(旧行为会拖垮整个 Atom 的 runner 集合)。
	c := &taskFakeClock{wall: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)}
	a := &TimeAbility{clock: c, runMode: TimeRunModeMonotonic, interval: 0}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, nil) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return after cancel")
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

// TestTimeAbilityFetchNetworkTimeAcceptsNoZoneDatetime 回归测试:
// timeapi.io 返回无时区后缀的 UTC datetime (如 "2026-09-04T12:28:07.3240057"),
// RFC3339Nano 解析失败, 修复前 sync_net 永远报 "datetime parse" 错误。
func TestTimeAbilityFetchNetworkTimeAcceptsNoZoneDatetime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// timeapi.io 实际格式: 无时区后缀
		io.WriteString(w, `{"dateTime":"2026-09-04T12:28:07.3240057"}`)
	}))
	defer srv.Close()

	// allowPrivate 以允许访问本地测试服务器 (生产默认拒绝私网, 属安全设计)
	a := &TimeAbility{
		httpTimeout:      5 * time.Second,
		maxResponseBytes: 64 << 10,
		networkPolicy:    addressPolicy{allowPrivate: true},
	}
	ts, err := a.fetchNetworkTime(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetchNetworkTime(no-zone): %v", err)
	}
	want := time.Date(2026, 9, 4, 12, 28, 7, 324005700, time.UTC)
	if !ts.Equal(want) {
		t.Fatalf("got %s want %s", ts, want)
	}

	// 带时区后缀的 RFC3339 格式仍应正常解析
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"dateTime":"2026-09-04T12:28:07.3240057Z"}`)
	}))
	defer srv2.Close()
	ts2, err := a.fetchNetworkTime(context.Background(), srv2.URL)
	if err != nil {
		t.Fatalf("fetchNetworkTime(with-zone): %v", err)
	}
	if !ts2.Equal(want) {
		t.Fatalf("got %s want %s", ts2, want)
	}
}

func TestTimeAbilityMaxSyncOffset(t *testing.T) {
	c := &taskFakeClock{wall: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)}
	base := c.Now()
	// 默认 48h 上限(ensureDefaults 设置; 直接构造需先初始化)
	a := &TimeAbility{clock: c}
	a.ensureDefaults()
	if err := a.checkSyncOffset(base.Add(24 * time.Hour)); err != nil {
		t.Fatalf("within limit: %v", err)
	}
	if err := a.checkSyncOffset(base.Add(49 * time.Hour)); !errors.Is(err, types.ErrInvalidArguments) {
		t.Fatalf("beyond limit error = %v", err)
	}
	if err := a.checkSyncOffset(base.Add(-49 * time.Hour)); !errors.Is(err, types.ErrInvalidArguments) {
		t.Fatalf("negative beyond limit error = %v", err)
	}
	// 显式关闭(哨兵 -1): 任意偏差放行
	a2 := &TimeAbility{clock: c, maxSyncOffset: -1}
	if err := a2.checkSyncOffset(base.Add(10 * 365 * 24 * time.Hour)); err != nil {
		t.Fatalf("disabled limit: %v", err)
	}
	// WithMaxSyncOffset(0) 也必须等效关闭(不被 ensureDefaults 覆盖为 48h)
	ta, err := NewTimeAbility(WithMaxSyncOffset(0))
	if err != nil {
		t.Fatal(err)
	}
	ta.ensureDefaults()
	ta.mu.RLock()
	off := ta.maxSyncOffset
	ta.mu.RUnlock()
	if off != -1 {
		t.Fatalf("WithMaxSyncOffset(0) got %v, want sentinel -1", off)
	}
}
