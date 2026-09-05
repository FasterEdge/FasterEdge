// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package ability

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/FasterEdge/FasterEdge/types"
)

const (
	TSNCommandSetInterface     = "set_interface"
	TSNCommandGetInterface     = "get_interface"
	TSNCommandRegisterTalker   = "register_talker"
	TSNCommandRegisterListener = "register_listener"
	TSNCommandUnregister       = "unregister"
	TSNCommandListStreams      = "list_streams"
	TSNCommandSetPriority      = "set_priority_map"
	TSNCommandGetPriority      = "get_priority_map"
	TSNCommandSetTimeAware     = "set_time_aware"
	TSNCommandGetTimeAware     = "get_time_aware"
)

// TSNStreamDirection 标识 TSN 流方向。
type TSNStreamDirection string

const (
	TSNDirTalker   TSNStreamDirection = "talker"
	TSNDirListener TSNStreamDirection = "listener"
)

// TSNStream 表示一条 TSN 数据流(发送方/接收方注册条目)。
type TSNStream struct {
	ID         string
	Direction  TSNStreamDirection
	MAC        string
	DestMAC    string
	VLANID     uint16
	Priority   uint8
	PayloadLen uint16
	Interval   time.Duration
	CreatedAt  time.Time
}

// TSNStreamIDArg 是 unregister 的参数。
type TSNStreamIDArg struct {
	ID string
}

// TSNInterfaceArg 是 set_interface 的参数。
type TSNInterfaceArg struct {
	Interface string
}

// TSNRegisterTalkerArgs 是 register_talker 的参数。
type TSNRegisterTalkerArgs struct {
	ID         string
	MAC        string
	DestMAC    string
	VLANID     uint16
	Priority   uint8
	PayloadLen uint16
	Interval   time.Duration
}

// TSNRegisterListenerArgs 是 register_listener 的参数。
type TSNRegisterListenerArgs struct {
	ID       string
	MAC      string
	DestMAC  string
	VLANID   uint16
	Priority uint8
}

// TSNPriorityMapArgs 是 set_priority_map 的参数。
// Priority 0..7 → Queue 0..7;不在 Map 中的优先级默认映射到 Queue 0。
type TSNPriorityMapArgs struct {
	Mappings map[uint8]uint8
}

// TSNTimeAwareArgs 是 set_time_aware 的参数(Qbv gate control 状态)。
type TSNTimeAwareArgs struct {
	Enabled    bool
	BaseTime   time.Time
	CycleTime  time.Duration
	GateStates []byte // 每字节低 8 位对应 8 个队列的门状态
}

// TSNAbility 管理 TSN 接口、talker/listener 注册、优先级映射、Qbv 时间感知门控。
type TSNAbility struct {
	mu        sync.RWMutex
	iface     string
	streams   map[string]*TSNStream
	prioMap   map[uint8]uint8
	timeAware TSNTimeAwareArgs
}

func NewTSNAbility() *TSNAbility {
	m := make(map[uint8]uint8, 8)
	for i := uint8(0); i < 8; i++ {
		m[i] = i
	}
	return &TSNAbility{streams: make(map[string]*TSNStream), prioMap: m}
}

func (t *TSNAbility) GetName() string { return "TSNAbility" }

func (t *TSNAbility) Describe() string {
	return "TSNAbility提供时间敏感网络(TSN)能力:talker/listener 流注册、IEEE 802.1Q 优先级到队列映射、802.1Qbv 时间感知门控配置。"
}

func (t *TSNAbility) Check(atom *types.Atom) error {
	if atom == nil {
		return types.ErrMissingDependency
	}
	if _, ok := atom.Data("BaseData"); !ok {
		return types.ErrMissingDependency
	}
	return nil
}

func (t *TSNAbility) Mount(atom *types.Atom) error { return t.Check(atom) }

func (t *TSNAbility) Command(atom *types.Atom, act string, args any) types.CommandOutput {
	if err := t.Check(atom); err != nil {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, err)}
	}
	switch act {
	case TSNCommandSetInterface:
		typed, ok := args.(TSNInterfaceArg)
		if !ok || strings.TrimSpace(typed.Interface) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		t.mu.Lock()
		t.iface = strings.TrimSpace(typed.Interface)
		t.mu.Unlock()
		return types.CommandOutput{Name: act, Value: t.iface}
	case TSNCommandGetInterface:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		t.mu.RLock()
		defer t.mu.RUnlock()
		return types.CommandOutput{Name: act, Value: t.iface}
	case TSNCommandRegisterTalker:
		return t.registerStream(TSNDirTalker, args)
	case TSNCommandRegisterListener:
		return t.registerStream(TSNDirListener, args)
	case TSNCommandUnregister:
		typed, ok := args.(TSNStreamIDArg)
		if !ok || strings.TrimSpace(typed.ID) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		t.mu.Lock()
		stream, ok := t.streams[strings.TrimSpace(typed.ID)]
		if !ok {
			t.mu.Unlock()
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: stream %q not found: %w", act, typed.ID, types.ErrInvalidArguments)}
		}
		delete(t.streams, strings.TrimSpace(typed.ID))
		t.mu.Unlock()
		return types.CommandOutput{Name: act, Value: *stream}
	case TSNCommandListStreams:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		t.mu.RLock()
		out := make([]TSNStream, 0, len(t.streams))
		for _, s := range t.streams {
			out = append(out, *s)
		}
		t.mu.RUnlock()
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return types.CommandOutput{Name: act, Value: out}
	case TSNCommandSetPriority:
		typed, ok := args.(TSNPriorityMapArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		for p, q := range typed.Mappings {
			if p > 7 {
				return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: priority %d out of range: %w", act, p, types.ErrInvalidArguments)}
			}
			if q > 7 {
				return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: queue %d out of range: %w", act, q, types.ErrInvalidArguments)}
			}
		}
		t.mu.Lock()
		t.prioMap = make(map[uint8]uint8, len(typed.Mappings))
		for p, q := range typed.Mappings {
			t.prioMap[p] = q
		}
		t.mu.Unlock()
		return types.CommandOutput{Name: act, Value: typed.Mappings}
	case TSNCommandGetPriority:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		t.mu.RLock()
		defer t.mu.RUnlock()
		out := make(map[uint8]uint8, len(t.prioMap))
		for p, q := range t.prioMap {
			out[p] = q
		}
		return types.CommandOutput{Name: act, Value: out}
	case TSNCommandSetTimeAware:
		typed, ok := args.(TSNTimeAwareArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		if typed.CycleTime < 0 {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: cycle time must be non-negative: %w", act, types.ErrInvalidArguments)}
		}
		if len(typed.GateStates) == 0 || len(typed.GateStates) > 16 {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: gate states must be 1-16 bytes: %w", act, types.ErrInvalidArguments)}
		}
		// 深拷贝 GateStates: 旧实现直接存调用方 slice(别名), 调用方事后修改会与
		// 并发 GetTimeAware 的读取形成数据竞争。
		gs := append([]byte(nil), typed.GateStates...)
		t.mu.Lock()
		t.timeAware = TSNTimeAwareArgs{Enabled: typed.Enabled, BaseTime: typed.BaseTime, CycleTime: typed.CycleTime, GateStates: gs}
		t.mu.Unlock()
		// 返回值另拷贝: 旧实现返回与存储共享底层数组的 gs, 调用方修改
		// 返回值会污染状态。
		return types.CommandOutput{Name: act, Value: TSNTimeAwareArgs{Enabled: typed.Enabled, BaseTime: typed.BaseTime, CycleTime: typed.CycleTime, GateStates: append([]byte(nil), gs...)}}
	case TSNCommandGetTimeAware:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		t.mu.RLock()
		ta := t.timeAware
		t.mu.RUnlock()
		out := TSNTimeAwareArgs{Enabled: ta.Enabled, BaseTime: ta.BaseTime, CycleTime: ta.CycleTime, GateStates: append([]byte(nil), ta.GateStates...)}
		return types.CommandOutput{Name: act, Value: out}
	}
	return types.CommandOutput{Name: act, Err: fmt.Errorf("command %s: %w", act, types.ErrUnsupportedCommand)}
}

func (t *TSNAbility) registerStream(dir TSNStreamDirection, args any) types.CommandOutput {
	var id, mac, dest string
	var vlan uint16
	var priority uint8
	var payload uint16
	var interval time.Duration
	switch dir {
	case TSNDirTalker:
		typed, ok := args.(TSNRegisterTalkerArgs)
		if !ok {
			return types.CommandOutput{Name: TSNCommandRegisterTalker, Err: fmt.Errorf("%s: %w", TSNCommandRegisterTalker, types.ErrInvalidArguments)}
		}
		id, mac, dest = strings.TrimSpace(typed.ID), strings.TrimSpace(typed.MAC), strings.TrimSpace(typed.DestMAC)
		vlan, priority, payload, interval = typed.VLANID, typed.Priority, typed.PayloadLen, typed.Interval
		if interval <= 0 {
			return types.CommandOutput{Name: TSNCommandRegisterTalker, Err: fmt.Errorf("%s: interval must be positive: %w", TSNCommandRegisterTalker, types.ErrInvalidArguments)}
		}
	default:
		typed, ok := args.(TSNRegisterListenerArgs)
		if !ok {
			return types.CommandOutput{Name: TSNCommandRegisterListener, Err: fmt.Errorf("%s: %w", TSNCommandRegisterListener, types.ErrInvalidArguments)}
		}
		id, mac, dest = strings.TrimSpace(typed.ID), strings.TrimSpace(typed.MAC), strings.TrimSpace(typed.DestMAC)
		vlan, priority = typed.VLANID, typed.Priority
	}
	if id == "" || mac == "" {
		return types.CommandOutput{Name: fmt.Sprintf("register_%s", dir), Err: fmt.Errorf("register_%s: id and mac required: %w", dir, types.ErrInvalidArguments)}
	}
	if !isValidMAC(mac) {
		return types.CommandOutput{Name: fmt.Sprintf("register_%s", dir), Err: fmt.Errorf("register_%s: invalid mac %q: %w", dir, mac, types.ErrInvalidArguments)}
	}
	if dest != "" && !isValidMAC(dest) {
		return types.CommandOutput{Name: fmt.Sprintf("register_%s", dir), Err: fmt.Errorf("register_%s: invalid dest mac %q: %w", dir, dest, types.ErrInvalidArguments)}
	}
	if priority > 7 {
		return types.CommandOutput{Name: fmt.Sprintf("register_%s", dir), Err: fmt.Errorf("register_%s: priority %d out of range: %w", dir, priority, types.ErrInvalidArguments)}
	}
	stream := &TSNStream{
		ID:         id,
		Direction:  dir,
		MAC:        mac,
		DestMAC:    dest,
		VLANID:     vlan,
		Priority:   priority,
		PayloadLen: payload,
		Interval:   interval,
		CreatedAt:  time.Now(),
	}
	t.mu.Lock()
	if _, exists := t.streams[id]; exists {
		t.mu.Unlock()
		return types.CommandOutput{Name: fmt.Sprintf("register_%s", dir), Err: fmt.Errorf("register_%s: id %q already exists: %w", dir, id, types.ErrInvalidArguments)}
	}
	t.streams[id] = stream
	t.mu.Unlock()
	return types.CommandOutput{Name: fmt.Sprintf("register_%s", dir), Value: *stream}
}

func isValidMAC(mac string) bool {
	if mac == "" {
		return false
	}
	parts := strings.Split(mac, ":")
	if len(parts) != 6 {
		return false
	}
	for _, p := range parts {
		if len(p) != 2 {
			return false
		}
		for _, r := range p {
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
				return false
			}
		}
	}
	return true
}
