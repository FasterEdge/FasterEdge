package ability

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/FasterEdge/FasterEdge/types"
	"github.com/beevik/ntp"
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
	initOnce   sync.Once
	mu         sync.RWMutex
	clock      timeClock
	lastSource string
	lastSynced time.Time
	baseMono   time.Duration
	current    time.Time
	runMode    TimeRunMode
	interval   time.Duration
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
			a.URL = "https://timeapi.io/api/Time/current/zone?timeZone=Asia/Shanghai"
		}
		ts, err := fetchNetworkTime(a.URL)
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
			a.Address = "pool.ntp.org"
		}
		ts, err := ntp.Time(a.Address)
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
	t.current = ts
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
		t.current = ts
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

func fetchNetworkTime(url string) (time.Time, error) {
	resp, err := http.Get(url)
	if err != nil {
		return time.Time{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return time.Time{}, err
	}
	var p struct {
		DateTime      string `json:"dateTime"`
		DateTimeUpper string `json:"DateTime"`
	}
	if err = json.Unmarshal(body, &p); err != nil {
		return time.Time{}, err
	}
	if p.DateTime == "" {
		p.DateTime = p.DateTimeUpper
	}
	if p.DateTime == "" {
		return time.Time{}, errors.New("datetime not found")
	}
	return time.Parse(time.RFC3339Nano, p.DateTime)
}
