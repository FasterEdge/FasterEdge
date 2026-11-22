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

// maxTSNInterfaceNameLen 对齐 Linux IFNAMSIZ(16, 含结尾 NUL): 接口名是会被
// 注入实现拼进 tc/ip 参数的字符串, 超长/含特殊字符的名字必须在框架层拒绝。
const maxTSNInterfaceNameLen = 15

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

// TSNTransport 抽象出对真实网卡的 TSN 查询与门控下发。
// FasterEdge 自身不调用 netlink/tc/taprio: 接口探测、流规则下发/撤销、
// PCP→队列映射、Qbv 门控全部委托给注入的实现(参考 Linux 上 tc prio/priomap、
// tc taprio、tc flower 等命令)。未注入时所有设备类命令返回错误,不假装成功。
type TSNTransport interface {
	// ProbeInterface 查询网卡是否存在且具备 TSN 门控能力(不支持时应返回错误)。
	ProbeInterface(iface string) error
	// AddStream 把 talker/listener 流下发为网卡侧规则。
	AddStream(iface string, stream TSNStream) error
	// RemoveStream 撤销 AddStream 下发的规则。
	RemoveStream(iface string, stream TSNStream) error
	// ApplyPriorityMap 下发 802.1Q PCP→队列映射(tc prio priomap)。
	// mappings 是本次调用独占的副本, 实现可保留。
	ApplyPriorityMap(iface string, mappings map[uint8]uint8) error
	// ApplyGateControl 下发 802.1Qbv 时间感知门控(tc taprio);Enabled=false 表示
	// 拆除门控。cfg 是本次调用独占的副本。
	ApplyGateControl(iface string, cfg TSNTimeAwareArgs) error
}

// TSNAbility 管理 TSN 接口、talker/listener 注册、优先级映射、Qbv 时间感知门控。
// 设备操作经注入的 TSNTransport 完成;本地只保留"最近一次成功下发的配置"用于回读。
type TSNAbility struct {
	// mu 保护下列字段(仅内存状态, 不在持锁时调用 transport)。
	mu      sync.RWMutex
	iface   string
	streams map[string]*TSNStream
	prioMap map[uint8]uint8
	// timeAware 是最近一次成功下发到设备的门控配置。
	timeAware TSNTimeAwareArgs
	transport TSNTransport

	// applyMu 串行化"下发设备 + 提交本地状态", 避免同一网卡上的 tc 操作并发交叉,
	// 也避免两次注册之间互相看到半提交状态。锁序: applyMu → mu。
	applyMu sync.Mutex
}

// Compile-time guarantee that TSNAbility satisfies CommandLister.
var _ types.CommandLister = (*TSNAbility)(nil)

// tsnAbilityCommands 是 TSNAbility 的命令目录, 与 Command 中 act 的
// switch 分支逐一对应(由 command_contract_test.go 解析源码交叉核对):
// 接口 2 条, 流注册 3 条, 优先级映射 2 条, Qbv 门控 2 条。
var tsnAbilityCommands = []string{
	TSNCommandSetInterface,
	TSNCommandGetInterface,
	TSNCommandRegisterTalker,
	TSNCommandRegisterListener,
	TSNCommandUnregister,
	TSNCommandListStreams,
	TSNCommandSetPriority,
	TSNCommandGetPriority,
	TSNCommandSetTimeAware,
	TSNCommandGetTimeAware,
}

// ListCommands 实现 types.CommandLister, 返回命令目录的副本(调用方持有所有权)。
func (t *TSNAbility) ListCommands() []string { return append([]string(nil), tsnAbilityCommands...) }

func NewTSNAbility() *TSNAbility {
	m := make(map[uint8]uint8, 8)
	for i := uint8(0); i < 8; i++ {
		m[i] = i
	}
	return &TSNAbility{streams: make(map[string]*TSNStream), prioMap: m}
}

// SetTransport 注入网卡查询/门控下发的实现。
// 与 Modbus/Serial/MQTT 等能力一致: 不注入时设备类命令返回错误而非静默成功。
func (t *TSNAbility) SetTransport(tr TSNTransport) {
	t.mu.Lock()
	t.transport = tr
	t.mu.Unlock()
}

func (t *TSNAbility) GetName() string { return "TSNAbility" }

func (t *TSNAbility) Describe() string {
	return "TSNAbility提供 TSN 配置能力:talker/listener 流注册、802.1Q 优先级到队列映射、802.1Qbv 时间感知门控;网卡探测与 tc/taprio 下发通过注入的 TSNTransport 完成,未注入时设备类命令返回错误。"
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
		iface := strings.TrimSpace(typed.Interface)
		if !isValidTSNInterfaceName(iface) {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: invalid interface name %q: %w", act, iface, types.ErrInvalidArguments)}
		}
		t.applyMu.Lock()
		defer t.applyMu.Unlock()
		t.mu.RLock()
		transport, current, registered := t.transport, t.iface, len(t.streams)
		t.mu.RUnlock()
		if transport == nil {
			return types.CommandOutput{Name: act, Err: errNoTSNTransport(act)}
		}
		// 切换接口而旧流仍在设备上注册会让本地记录与网卡实际状态漂移。
		if registered > 0 && iface != current {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %d stream(s) still registered on %q, unregister them before switching interface: %w", act, registered, current, types.ErrInvalidArguments)}
		}
		// 接口必须由 transport 真实探测: 名字存在不等于网卡存在, 更不等于支持 Qbv。
		if err := transport.ProbeInterface(iface); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: probe interface %q: %w: %w", act, iface, types.ErrOperationFailed, err)}
		}
		t.mu.Lock()
		t.iface = iface
		t.mu.Unlock()
		return types.CommandOutput{Name: act, Value: iface}
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
		id := strings.TrimSpace(typed.ID)
		t.applyMu.Lock()
		defer t.applyMu.Unlock()
		t.mu.RLock()
		stream, ok := t.streams[id]
		t.mu.RUnlock()
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: stream %q not found: %w", act, id, types.ErrInvalidArguments)}
		}
		iface, transport, err := t.requireDevice(act)
		if err != nil {
			return types.CommandOutput{Name: act, Err: err}
		}
		// 设备侧撤销失败时保留本地条目, 调用方可重试;不能先删记录造成"本地已删、
		// 网卡仍在转发"的假成功。
		if err := transport.RemoveStream(iface, *stream); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: remove stream %q: %w: %w", act, id, types.ErrOperationFailed, err)}
		}
		t.mu.Lock()
		delete(t.streams, id)
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
		next := cloneTSNPriorityMap(typed.Mappings)
		t.applyMu.Lock()
		defer t.applyMu.Unlock()
		iface, transport, err := t.requireDevice(act)
		if err != nil {
			return types.CommandOutput{Name: act, Err: err}
		}
		// 映射必须真正下发到网卡; 只在内存改 map 时 tc priomap 仍是旧值, 属假成功。
		if err := transport.ApplyPriorityMap(iface, next); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: apply priority map: %w: %w", act, types.ErrOperationFailed, err)}
		}
		t.mu.Lock()
		t.prioMap = cloneTSNPriorityMap(next)
		t.mu.Unlock()
		return types.CommandOutput{Name: act, Value: cloneTSNPriorityMap(next)}
	case TSNCommandGetPriority:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		t.mu.RLock()
		out := cloneTSNPriorityMap(t.prioMap)
		t.mu.RUnlock()
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
		cfg := TSNTimeAwareArgs{Enabled: typed.Enabled, BaseTime: typed.BaseTime, CycleTime: typed.CycleTime, GateStates: gs}
		t.applyMu.Lock()
		defer t.applyMu.Unlock()
		iface, transport, err := t.requireDevice(act)
		if err != nil {
			return types.CommandOutput{Name: act, Err: err}
		}
		// 门控必须真正下发(tc taprio); 下发失败就不得记录为已启用。
		if err := transport.ApplyGateControl(iface, cloneTSNTimeAware(cfg)); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: apply gate control: %w: %w", act, types.ErrOperationFailed, err)}
		}
		t.mu.Lock()
		t.timeAware = cfg
		t.mu.Unlock()
		// 返回值另拷贝: 旧实现返回与存储共享底层数组的 gs, 调用方修改
		// 返回值会污染状态。
		return types.CommandOutput{Name: act, Value: cloneTSNTimeAware(cfg)}
	case TSNCommandGetTimeAware:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		t.mu.RLock()
		ta := t.timeAware
		t.mu.RUnlock()
		return types.CommandOutput{Name: act, Value: cloneTSNTimeAware(ta)}
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
	act := fmt.Sprintf("register_%s", dir)
	if id == "" || mac == "" {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: id and mac required: %w", act, types.ErrInvalidArguments)}
	}
	if !isValidMAC(mac) {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: invalid mac %q: %w", act, mac, types.ErrInvalidArguments)}
	}
	if dest != "" && !isValidMAC(dest) {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: invalid dest mac %q: %w", act, dest, types.ErrInvalidArguments)}
	}
	if priority > 7 {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: priority %d out of range: %w", act, priority, types.ErrInvalidArguments)}
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
	t.applyMu.Lock()
	defer t.applyMu.Unlock()
	iface, transport, err := t.requireDevice(act)
	if err != nil {
		return types.CommandOutput{Name: act, Err: err}
	}
	t.mu.RLock()
	_, exists := t.streams[id]
	t.mu.RUnlock()
	if exists {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: id %q already exists: %w", act, id, types.ErrInvalidArguments)}
	}
	// 流必须在设备侧落地; 只写内存 map 时网卡没有对应规则, 属骨架假成功。
	if err := transport.AddStream(iface, *stream); err != nil {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: add stream %q: %w: %w", act, id, types.ErrOperationFailed, err)}
	}
	t.mu.Lock()
	t.streams[id] = stream
	t.mu.Unlock()
	return types.CommandOutput{Name: act, Value: *stream}
}

// requireDevice 返回已选网卡与已注入的 transport; 任一缺失即返回明确错误。
// 语义对齐 Modbus/Serial/Docker: 没有设备通道时绝不假装成功。
func (t *TSNAbility) requireDevice(act string) (string, TSNTransport, error) {
	t.mu.RLock()
	iface, transport := t.iface, t.transport
	t.mu.RUnlock()
	if transport == nil {
		return "", nil, errNoTSNTransport(act)
	}
	if strings.TrimSpace(iface) == "" {
		return "", nil, fmt.Errorf("%s: no interface selected, set_interface first: %w", act, types.ErrInvalidArguments)
	}
	return iface, transport, nil
}

func errNoTSNTransport(act string) error {
	return fmt.Errorf("%s: no TSNTransport injected: %w", act, types.ErrInvalidArguments)
}

func cloneTSNPriorityMap(m map[uint8]uint8) map[uint8]uint8 {
	out := make(map[uint8]uint8, len(m))
	for p, q := range m {
		out[p] = q
	}
	return out
}

func cloneTSNTimeAware(cfg TSNTimeAwareArgs) TSNTimeAwareArgs {
	return TSNTimeAwareArgs{
		Enabled:    cfg.Enabled,
		BaseTime:   cfg.BaseTime,
		CycleTime:  cfg.CycleTime,
		GateStates: append([]byte(nil), cfg.GateStates...),
	}
}

// isValidTSNInterfaceName 接受 Linux 风格接口名(字母/数字/_/-/., 1..15 字节)。
func isValidTSNInterfaceName(name string) bool {
	if name == "" || len(name) > maxTSNInterfaceNameLen {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == '.':
		default:
			return false
		}
	}
	return true
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
