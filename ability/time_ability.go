package ability

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/FasterEdge/FasterEdge/types"
)

// TimeAbilityArgs 定义时间能力相关命令的入参
type TimeAbilityArgs struct {
	URL   string
	Value string
}

// TimeAbilityOutput 描述时间能力命令的输出结果
type TimeAbilityOutput struct {
	Message string
	Success bool
	Error   string
}

// TimeAbility
type TimeAbility struct {
	mu            sync.RWMutex
	lastSource    string
	lastSynced    time.Time
	baseMonotonic time.Time
	current       time.Time
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
func (t *TimeAbility) Check(atmo types.Atom) bool {
	// 检查BaseData是否已经被挂载
	if _, ok := atmo.GetAllData()["BaseData"]; !ok {
		return false
	}
	return true
}

// 将能力挂载到原子上
func (t *TimeAbility) Mount(atmo types.Atom) bool {
	if !t.Check(atmo) {
		fmt.Errorf("[%s] 挂载失败: BaseData未挂载\n", t.GetName())
		return false
	}
	atmo.AddAbility(t)
	return true
}

// 指令入口
func (t *TimeAbility) Command(atmo types.Atom, act string, args any) types.AbilityOutput {
	typed, _ := args.(TimeAbilityArgs)
	switch act {
	case "sync_net":
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

	case "sync_manual":
		fmt.Printf("[%s] 正在执行 sync_manual\n", t.GetName())
		ts, err := time.Parse(time.RFC3339, typed.Value)
		if err != nil {
			return types.AbilityOutput{Name: act, Success: false, Error: "invalid time"}
		}
		t.setSync(ts, "manual")
		return types.AbilityOutput{Name: act, Success: true}

	case "sync_system":
		fmt.Printf("[%s] 正在执行 sync_system\n", t.GetName())
		now := time.Now()
		t.setSync(now, "system")
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
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for now := range ticker.C {
			t.advance(now)
		}
		return types.AbilityOutput{Name: act, Success: true}

	case "get_time":
		t.ensureSynced()
		fmt.Printf("[%s] %s\n", t.GetName(), t.now().Format(time.RFC3339Nano))
		return types.AbilityOutput{Name: act, Success: true}
	}

	_ = atmo
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
	t.baseMonotonic = time.Now()
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
	if t.current.IsZero() {
		t.mu.RUnlock()
		return time.Now()
	}
	cur := t.current
	t.mu.RUnlock()
	return cur
}
