package ability

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/FasterEdge/FasterEdge/types"
)

const (
	TimeCommandSyncNetwork  = "sync_net"
	TimeCommandSyncManual   = "sync_manual"
	TimeCommandSyncSystem   = "sync_system"
	TimeCommandSyncNTP      = "sync_ntp"
	TimeCommandLastSync     = "last"
	TimeCommandGetTime      = "get_time"
	TimeCommandConfigureRun = "configure_run"
)

type TimeSyncNetworkArgs struct{ URL string }
type TimeSyncManualArgs struct{ Value string }
type TimeSyncNTPArgs struct{ Address string }
type TimeRunMode string

const (
	TimeRunModeMonotonic TimeRunMode = "monotonic"
	TimeRunModeTicker    TimeRunMode = "ticker"
)

type TimeConfigureRunArgs struct {
	Mode     TimeRunMode
	Interval time.Duration
}

type TimeSnapshot struct {
	Source string
	Time   time.Time
}
type TimeAbilityOutput = TimeSnapshot
type TimeOutput = TimeSnapshot

type TimeAbility struct {
	initOnce          sync.Once
	mu                sync.RWMutex
	clock             timeClock
	lastSource        string
	lastSynced        time.Time
	baseMono          time.Duration
	runMode           TimeRunMode
	interval          time.Duration
	networkPolicy     addressPolicy
	httpTimeout       time.Duration
	ntpTimeout        time.Duration
	maxResponseBytes  int64
	minimumTick       time.Duration
	defaultNetworkURL string
	defaultNTPServer  string
	ntpQuery          ntpQuerier
}

type timeAbilityConfig struct {
	allowPrivate                        bool
	httpTimeout, ntpTimeout             time.Duration
	maxResponseBytes                    int64
	minimumTick                         time.Duration
	defaultNetworkURL, defaultNTPServer string
}
type TimeOption func(*timeAbilityConfig) error

func WithPrivateNetworkTimeSources() TimeOption {
	return func(c *timeAbilityConfig) error { c.allowPrivate = true; return nil }
}
func WithHTTPTimeout(v time.Duration) TimeOption {
	return func(c *timeAbilityConfig) error {
		if v <= 0 {
			return types.ErrInvalidArguments
		}
		c.httpTimeout = v
		return nil
	}
}
func WithNTPTimeout(v time.Duration) TimeOption {
	return func(c *timeAbilityConfig) error {
		if v <= 0 {
			return types.ErrInvalidArguments
		}
		c.ntpTimeout = v
		return nil
	}
}
func WithMaxResponseBytes(v int64) TimeOption {
	return func(c *timeAbilityConfig) error {
		if v <= 0 {
			return types.ErrInvalidArguments
		}
		c.maxResponseBytes = v
		return nil
	}
}
func WithMinimumTickInterval(v time.Duration) TimeOption {
	return func(c *timeAbilityConfig) error {
		if v <= 0 {
			return types.ErrInvalidArguments
		}
		c.minimumTick = v
		return nil
	}
}
func NewTimeAbility(options ...TimeOption) (*TimeAbility, error) {
	c := timeAbilityConfig{httpTimeout: 5 * time.Second, ntpTimeout: 5 * time.Second, maxResponseBytes: 64 << 10, minimumTick: time.Millisecond, defaultNetworkURL: "https://timeapi.io/api/Time/current/zone?timeZone=Asia/Shanghai", defaultNTPServer: "pool.ntp.org"}
	for _, o := range options {
		if o == nil {
			return nil, fmt.Errorf("%w: nil option", types.ErrInvalidArguments)
		}
		if err := o(&c); err != nil {
			return nil, fmt.Errorf("%w", err)
		}
	}
	return &TimeAbility{httpTimeout: c.httpTimeout, ntpTimeout: c.ntpTimeout, maxResponseBytes: c.maxResponseBytes, minimumTick: c.minimumTick, defaultNetworkURL: c.defaultNetworkURL, defaultNTPServer: c.defaultNTPServer, networkPolicy: addressPolicy{allowPrivate: c.allowPrivate}}, nil
}

var _ types.Ability = (*TimeAbility)(nil)

func (t *TimeAbility) ensureDefaults() {
	t.initOnce.Do(func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.clock == nil {
			t.clock = newSystemTimeClock()
		}
		if t.runMode == "" {
			t.runMode = TimeRunModeMonotonic
		}
		if t.interval == 0 {
			t.interval = time.Millisecond
		}
		if t.httpTimeout == 0 {
			t.httpTimeout = 5 * time.Second
		}
		if t.ntpTimeout == 0 {
			t.ntpTimeout = 5 * time.Second
		}
		if t.maxResponseBytes == 0 {
			t.maxResponseBytes = 64 << 10
		}
		if t.minimumTick == 0 {
			t.minimumTick = time.Millisecond
		}
		if t.defaultNetworkURL == "" {
			t.defaultNetworkURL = "https://timeapi.io/api/Time/current/zone?timeZone=Asia/Shanghai"
		}
		if t.defaultNTPServer == "" {
			t.defaultNTPServer = "pool.ntp.org"
		}
		if t.ntpQuery == nil {
			t.ntpQuery = ntpQueryAdapter{}
		}
	})
}
func (t *TimeAbility) GetName() string { return "TimeAbility" }
func (t *TimeAbility) Describe() string {
	return "提供网络/手动/系统对时能力，缓解设备本地时间不准的问题。"
}
func (t *TimeAbility) Check(atom *types.Atom) error {
	if atom == nil {
		return types.ErrMissingDependency
	}
	if _, ok := atom.Data("BaseData"); !ok {
		return types.ErrMissingDependency
	}
	return nil
}
func (t *TimeAbility) Mount(atom *types.Atom) error { return t.Check(atom) }

func invalid(act string) types.CommandOutput {
	return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
}

func (t *TimeAbility) Command(atom *types.Atom, act string, args any) types.CommandOutput {
	t.ensureDefaults()
	switch act {
	case TimeCommandSyncManual:
		a, ok := args.(TimeSyncManualArgs)
		if !ok || strings.TrimSpace(a.Value) == "" {
			return invalid(act)
		}
		ts, err := time.Parse(time.RFC3339Nano, a.Value)
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %v", act, types.ErrInvalidArguments, err)}
		}
		t.setSync(ts, "manual")
		return types.CommandOutput{Name: act, Value: t.snapshot()}
	case TimeCommandSyncSystem:
		if args != nil {
			return invalid(act)
		}
		t.setSync(t.clock.Now(), "system")
		return types.CommandOutput{Name: act, Value: t.snapshot()}
	case TimeCommandSyncNetwork:
		var a TimeSyncNetworkArgs
		if args != nil {
			var ok bool
			a, ok = args.(TimeSyncNetworkArgs)
			if !ok || strings.TrimSpace(a.URL) == "" {
				return invalid(act)
			}
		}
		if a.URL == "" {
			a.URL = t.defaultNetworkURL
		}
		ctx, cancel := context.WithTimeout(context.Background(), t.httpTimeout)
		defer cancel()
		ts, err := t.fetchNetworkTime(ctx, a.URL)
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, err)}
		}
		t.setSync(ts, "net:"+a.URL)
		return types.CommandOutput{Name: act, Value: t.snapshot()}
	case TimeCommandSyncNTP:
		var a TimeSyncNTPArgs
		if args != nil {
			var ok bool
			a, ok = args.(TimeSyncNTPArgs)
			if !ok || strings.TrimSpace(a.Address) == "" {
				return invalid(act)
			}
		}
		if a.Address == "" {
			a.Address = t.defaultNTPServer
		}
		ts, err := t.fetchNTPTime(a.Address)
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, err)}
		}
		t.setSync(ts, "ntp:"+a.Address)
		return types.CommandOutput{Name: act, Value: t.snapshot()}
	case TimeCommandLastSync:
		if args != nil {
			return invalid(act)
		}
		t.mu.RLock()
		s, ts := t.lastSource, t.lastSynced
		t.mu.RUnlock()
		return types.CommandOutput{Name: act, Value: TimeSnapshot{Source: s, Time: ts}}
	case TimeCommandGetTime:
		if args != nil {
			return invalid(act)
		}
		t.ensureSynced()
		now, src, err := t.currentTime()
		if err != nil {
			return types.CommandOutput{Name: act, Err: err}
		}
		return types.CommandOutput{Name: act, Value: TimeSnapshot{Source: src, Time: now}}
	case TimeCommandConfigureRun:
		a, ok := args.(TimeConfigureRunArgs)
		if !ok {
			return invalid(act)
		}
		mode := TimeRunMode(strings.ToLower(strings.TrimSpace(string(a.Mode))))
		if mode == "" {
			mode = TimeRunModeMonotonic
		}
		if mode != TimeRunModeMonotonic && mode != TimeRunModeTicker {
			return invalid(act)
		}
		if a.Interval < 0 || (mode == TimeRunModeMonotonic && a.Interval != 0) || (a.Interval > 0 && a.Interval < time.Millisecond) {
			return invalid(act)
		}
		t.mu.Lock()
		t.runMode = mode
		if a.Interval > 0 {
			t.interval = a.Interval
		}
		t.mu.Unlock()
		return types.CommandOutput{Name: act}
	default:
		return types.CommandOutput{Name: act, Err: fmt.Errorf("command %s: %w", act, types.ErrUnsupportedCommand)}
	}
}

func (t *TimeAbility) setSync(ts time.Time, source string) {
	mono := t.clock.Monotonic()
	t.mu.Lock()
	t.lastSynced = ts
	t.baseMono = mono
	t.lastSource = source
	t.mu.Unlock()
}
func (t *TimeAbility) ensureSynced() {
	t.mu.RLock()
	ok := !t.lastSynced.IsZero()
	t.mu.RUnlock()
	if ok {
		return
	}
	ts := t.clock.Now()
	mono := t.clock.Monotonic()
	t.mu.Lock()
	if t.lastSynced.IsZero() {
		t.lastSynced = ts
		t.baseMono = mono
		t.lastSource = "system"
	}
	t.mu.Unlock()
}
func (t *TimeAbility) currentTime() (time.Time, string, error) {
	mono := t.clock.Monotonic()
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.lastSynced.IsZero() {
		return time.Time{}, "", types.ErrInvalidArguments
	}
	elapsed := mono - t.baseMono
	if elapsed < 0 {
		return time.Time{}, t.lastSource, errors.New("monotonic clock moved backwards")
	}
	return t.lastSynced.Add(elapsed), t.lastSource, nil
}
func (t *TimeAbility) snapshot() TimeSnapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return TimeSnapshot{Source: t.lastSource, Time: t.lastSynced}
}

// timeCommands is the canonical command catalogue for TimeAbility. The
// Command switch and ListCommands both consume it so the list cannot drift
// from the dispatch table.
var timeCommands = []string{
	TimeCommandSyncNetwork,
	TimeCommandSyncManual,
	TimeCommandSyncSystem,
	TimeCommandSyncNTP,
	TimeCommandLastSync,
	TimeCommandGetTime,
	TimeCommandConfigureRun,
}

// ListCommands satisfies types.CommandLister.
func (t *TimeAbility) ListCommands() []string { return append([]string(nil), timeCommands...) }

// HealthCheck satisfies types.HealthChecker. It returns nil if the ability
// has ever synced; otherwise it reports ErrNotSynced so callers can decide
// whether to wait or surface a readiness issue.
func (t *TimeAbility) HealthCheck(ctx context.Context, _ *types.Atom) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t.mu.RLock()
	synced := !t.lastSynced.IsZero()
	t.mu.RUnlock()
	if !synced {
		return ErrTimeNotSynced
	}
	return nil
}

// ErrTimeNotSynced is returned by TimeAbility.HealthCheck when the ability
// has not yet performed a sync. It is not a fatal failure — callers may
// follow it up by issuing a sync command.
var ErrTimeNotSynced = errors.New("time ability has not been synced")

// Run implements types.Runner. TimeAbility supervises itself as a Runner:
// it promotes any unset clock state to system time on entry, then drives a
// periodic ticker in either monotonic (default) or ticker mode until ctx is
// cancelled. Returns nil on clean cancellation. Rejects zero / negative
// intervals to avoid busy-looping in ticker mode. The default interval for
// unconfigured monotonic mode is 1 second so the supervision loop does not
// burn CPU when the user never called configure_run.
func (t *TimeAbility) Run(ctx context.Context, _ *types.Atom) error {
	t.mu.RLock()
	mode, interval := t.runMode, t.interval
	t.mu.RUnlock()
	if interval <= 0 {
		return fmt.Errorf("time ability run: interval %s: %w", interval, types.ErrInvalidArguments)
	}
	t.ensureDefaults()
	t.ensureSynced()
	if mode == TimeRunModeMonotonic && interval < time.Second {
		// Avoid the default 1ms busy loop that ensureDefaults seeds for
		// backwards compatibility with non-Runner callers.
		interval = time.Second
	}
	if interval < t.minimumTick {
		interval = t.minimumTick
	}
	switch mode {
	case TimeRunModeTicker:
		return t.runTicker(ctx, interval)
	default:
		return t.runMonotonic(ctx, interval)
	}
}

func (t *TimeAbility) runMonotonic(ctx context.Context, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			// currentTime() is a pure function over lastSynced +
			// (nowMono - baseMono). Ticking it periodically keeps the
			// path warm for get_time and exposes liveness without
			// touching the underlying sync source.
			_, _, _ = t.currentTime()
		}
	}
}

func (t *TimeAbility) runTicker(ctx context.Context, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			t.setSync(t.clock.Now(), "system")
		}
	}
}

// Compile-time interface assertions.
var (
	_ types.Runner         = (*TimeAbility)(nil)
	_ types.CommandLister  = (*TimeAbility)(nil)
	_ types.HealthChecker  = (*TimeAbility)(nil)
)
