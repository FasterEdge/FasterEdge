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

// TimeAbilityArgs 定义时间能力相关命令的入参
type TimeAbilityArgs struct {
	URL      string
	NTP      string
	Value    string
	Mode     string // run中的的定时依据 “CPU”：就是直接使用ticker、“System”:利用系统时钟的变化量（因为系统时间可能是不准或者错乱的，但是时间变化量是固定的）来调整当前时间，默认 "System"
	Accuracy string // 当指定的是 CPU模式时，精度要求，单位可以是 "ns"：、"ms"、"s"、"m"，默认 "ms"
}

// TimeAbilityOutput 描述时间能力命令的输出结果
type TimeAbilityOutput struct {
	Message string
	Success bool
	Error   string
	Time    time.Time
}

// TimeAbility 相关参数
type TimeAbility struct {
	mu            sync.RWMutex
	lastSource    string
	lastSynced    time.Time
	baseMonotonic time.Time
	baseWallClock time.Time // 记录上次同步时的系统时间
	current       time.Time
	runMode       string
}

// 能力名称
func (t *TimeAbility) GetName() string {
	return "TimeAbility"
}

// 能力描述
func (t *TimeAbility) Describe() string {
	return "提供网络/手动/系统对时能力，缓解设备本地时间不准的问题。"
}

// 验证是否满足挂载条件（需要BaseData）
func (t *TimeAbility) Check(atom types.Atom) bool {
	// 检查BaseData是否已经被挂载
	if _, ok := atom.GetAllData()["BaseData"]; !ok {
		return false
	}
	return true
}

// 将能力挂载到原子上
func (t *TimeAbility) Mount(atom types.Atom) bool {
	if !t.Check(atom) {
		fmt.Errorf("[%s] 挂载失败: BaseData未挂载\n", t.GetName())
		return false
	}
	atom.AddAbility(t)
	return true
}

// 指令入口
func (t *TimeAbility) Command(atom types.Atom, act string, args any) types.AbilityOutput {
	typed, _ := args.(TimeAbilityArgs)
	switch act {
	case "sync_net": // 通过网络地址请求获得时间并同步
		fmt.Printf("[%s] 正在执行 sync_net\n", t.GetName())
		url := typed.URL
		if url == "" {
			url = "https://timeapi.io/api/Time/current/zone?timeZone=Asia/Shanghai"
		}
		if ts, err := fetchNetworkTime(url); err == nil {
			t.setSync(ts, "net:"+url)
			return types.AbilityOutput{Name: act, Success: true}
		}
		return types.AbilityOutput{Name: act, Success: false, Error: "fetch failed"}

	case "sync_manual": // 通过手动输入的时间字符串进行同步
		fmt.Printf("[%s] 正在执行 sync_manual\n", t.GetName())
		ts, err := time.Parse(time.RFC3339, typed.Value)
		if err != nil {
			return types.AbilityOutput{Name: act, Success: false, Error: "invalid time"}
		}
		t.setSync(ts, "manual")
		return types.AbilityOutput{Name: act, Success: true}

	case "sync_system": // 直接使用系统时间进行同步
		fmt.Printf("[%s] 正在执行 sync_system\n", t.GetName())
		now := time.Now()
		t.setSync(now, "system")
		return types.AbilityOutput{Name: act, Success: true}

	case "sync_ntp": // 通过NTP服务器进行同步
		fmt.Printf("[%s] 正在执行 sync_ntp\n", t.GetName())
		url := typed.NTP
		if url == "" {
			url = "pool.ntp.org"
		}
		ts, err := ntp.Time(url)
		if err != nil {
			fmt.Printf("[%s] NTP同步失败: %v\n", t.GetName(), err)
			return types.AbilityOutput{Name: act, Success: false, Error: "ntp fetch failed"}
		}
		t.setSync(ts, "ntp:"+url)
		return types.AbilityOutput{Name: act, Success: true}

	case "last":
		fmt.Printf("[%s] 正在执行 last\n", t.GetName())
		src, ts := t.getLast()
		println(src, ts.String())
		return types.AbilityOutput{Name: act, Success: true, Value: TimeAbilityOutput{Message: ts.String(), Success: true}}

	case "runnable":
		fmt.Printf("[%s] 正在执行 runnable\n", t.GetName())
		return types.AbilityOutput{Name: act, Success: true}

	case "run":
		fmt.Printf("[%s] 正在执行 run\n", t.GetName())
		t.ensureSynced()
		mode := strings.ToLower(strings.TrimSpace(typed.Mode))
		if mode == "" {
			mode = "system"
		}
		accuracy := strings.ToLower(strings.TrimSpace(typed.Accuracy))
		if accuracy == "" {
			accuracy = "ms"
		}
		interval, err := parseAccuracy(accuracy)
		if err != nil {
			return types.AbilityOutput{Name: act, Success: false, Error: err.Error()}
		}

		t.mu.Lock()
		t.runMode = mode
		t.mu.Unlock()

		switch mode {
		case "system":
			// system 模式依赖 get_time 时使用系统时钟推算，不需要常驻 ticker
			return types.AbilityOutput{Name: act, Success: true}
		case "cpu":
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for now := range ticker.C {
				t.advance(now)
			}
		default:
			return types.AbilityOutput{Name: act, Success: false, Error: "unsupported run mode"}
		}
		return types.AbilityOutput{Name: act, Success: true}

	case "get_time":
		t.ensureSynced()
		now := t.now()
		msg := now.Format(time.RFC3339Nano)
		fmt.Printf("[%s] %s\n", t.GetName(), msg)
		return types.AbilityOutput{Name: act, Success: true, Value: TimeAbilityOutput{Message: msg, Success: true, Time: now}}

	}

	_ = atom
	return types.AbilityOutput{Name: act, Success: false, Error: "unsupported act"}
}

// 通过网络地址请求获得时间
func fetchNetworkTime(url string) (time.Time, error) {
	resp, err := http.Get(url)
	if err != nil {
		return time.Time{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return time.Time{}, err
	}
	var payload struct {
		DateTime_low string `json:"dateTime"`
		DateTime     string `json:"DateTime"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return time.Time{}, err
	}
	dt := payload.DateTime
	if dt == "" {
		dt = payload.DateTime_low
	}
	if dt == "" {
		return time.Time{}, errors.New("datetime not found")
	}
	return time.Parse(time.RFC3339Nano, dt)
}

// 设置同步时间和来源
func (t *TimeAbility) setSync(ts time.Time, source string) {
	t.mu.Lock()
	t.lastSynced = ts
	current := time.Now()
	t.baseMonotonic = current
	t.baseWallClock = current
	t.current = ts
	t.lastSource = source
	t.mu.Unlock()
}

// 根据当前系统时间和基准单调时间计算当前时间
func (t *TimeAbility) advance(now time.Time) {
	t.mu.Lock()
	if t.baseMonotonic.IsZero() || t.lastSynced.IsZero() {
		t.baseMonotonic = now
		t.lastSynced = now
		t.current = now
	} else {
		elapsed := now.Sub(t.baseMonotonic)
		t.current = t.lastSynced.Add(elapsed)
	}
	t.mu.Unlock()
}

// 确保至少有一个同步时间，如果没有则使用系统时间进行初始化
func (t *TimeAbility) ensureSynced() {
	t.mu.RLock()
	zero := t.lastSynced.IsZero()
	t.mu.RUnlock()
	if zero {
		t.setSync(time.Now(), "system")
	}
}

// 获取最后一次同步的时间和来源
func (t *TimeAbility) getLast() (string, time.Time) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.lastSource, t.lastSynced
}

// 获取当前时间，如果没有同步过则返回系统时间
func (t *TimeAbility) now() time.Time {
	t.mu.RLock()
	mode := t.runMode
	cur := t.current
	lastSynced := t.lastSynced
	wall := t.baseWallClock
	t.mu.RUnlock()

	if lastSynced.IsZero() {
		return time.Now()
	}

	// system 模式：按需用系统壁钟推算（跟随系统时间变动）
	if mode == "" || mode == "system" {
		if wall.IsZero() {
			return lastSynced
		}
		offset := lastSynced.Sub(wall)
		return time.Now().Add(offset)
	}

	if cur.IsZero() {
		return time.Now()
	}
	return cur
}

// 根据精度字符串解析时间间隔
func parseAccuracy(acc string) (time.Duration, error) {
	switch acc {
	case "ns":
		return time.Nanosecond, nil
	case "us", "µs":
		return time.Microsecond, nil
	case "ms":
		return time.Millisecond, nil
	case "s":
		return time.Second, nil
	case "m":
		return time.Minute, nil
	case "":
		return time.Millisecond, nil
	}
	if d, err := time.ParseDuration(acc); err == nil && d > 0 {
		return d, nil
	}
	return 0, fmt.Errorf("unsupported accuracy: %s", acc)
}
