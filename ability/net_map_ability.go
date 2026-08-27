package ability

import (
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/FasterEdge/FasterEdge/data"
	"github.com/FasterEdge/FasterEdge/types"
)

const (
	NetMapCommandRegisterPeer   = "register_peer"
	NetMapCommandUnregisterPeer = "unregister_peer"
	NetMapCommandUpdatePeer     = "update_peer"
	NetMapCommandListPeers      = "list_peers"
	NetMapCommandLookupPeer     = "lookup_peer"
	NetMapCommandGetTopology    = "get_topology"
)

// NetMapPeer 描述一个对等节点的网络拓扑条目。
type NetMapPeer struct {
	Name     string
	Address  string
	Role     string
	LastSeen time.Time
}

// NetMapTopology 是本节点 + 对等节点集合的快照。
type NetMapTopology struct {
	Self  data.NetMapLocalInfo `json:"self"`
	Peers []NetMapPeer         `json:"peers"`
}

// NetMapRegisterPeerArgs 是 register_peer 命令的参数。
type NetMapRegisterPeerArgs struct {
	Name    string
	Address string
	Role    string
}

// NetMapUpdatePeerArgs 是 update_peer 命令的参数。
// 零值字段不会被应用 —— 想清空字段请传带前导空格的标记或使用对应专用命令。
type NetMapUpdatePeerArgs struct {
	Name          string
	NewAddress    string
	NewRole       string
	TouchLastSeen bool
}

// NetMapLookupPeerArgs 是 lookup_peer 命令的参数。
// 传入 Name 或 Address 任一字段均可,优先按 Name 精确匹配。
type NetMapLookupPeerArgs struct {
	Name    string
	Address string
}

// NetMapAbility 管理对等节点拓扑表,依赖 BaseData 和 NetMapData。
type NetMapAbility struct {
	mu    sync.RWMutex
	peers map[string]NetMapPeer // key = peer name
}

func NewNetMapAbility() *NetMapAbility {
	return &NetMapAbility{peers: make(map[string]NetMapPeer)}
}

func (a *NetMapAbility) GetName() string { return "NetMapAbility" }

func (a *NetMapAbility) Describe() string {
	return "NetMapAbility提供对等节点拓扑管理能力:注册/更新/查询/移除对端节点,生成本节点 + 对等节点拓扑快照。"
}

// Dependencies declares the data components the ability requires at mount
// time. Mirrors the runtime Check below and lets the framework compute
// mount order topologically.
func (a *NetMapAbility) Dependencies() []types.Dependency {
	return []types.Dependency{
		{Kind: types.DependencyData, Name: "BaseData"},
		{Kind: types.DependencyData, Name: "NetMapData"},
	}
}

func (a *NetMapAbility) Check(atom *types.Atom) error {
	if atom == nil {
		return types.ErrMissingDependency
	}
	if _, ok := atom.Data("BaseData"); !ok {
		return types.ErrMissingDependency
	}
	if _, ok := atom.Data("NetMapData"); !ok {
		return types.ErrMissingDependency
	}
	return nil
}

func (a *NetMapAbility) Mount(atom *types.Atom) error { return a.Check(atom) }

func (a *NetMapAbility) Command(atom *types.Atom, act string, args any) types.CommandOutput {
	if err := a.Check(atom); err != nil {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, err)}
	}
	switch act {
	case NetMapCommandRegisterPeer:
		typed, ok := args.(NetMapRegisterPeerArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		name := strings.TrimSpace(typed.Name)
		addr := strings.TrimSpace(typed.Address)
		if name == "" || name != strings.TrimSpace(name) {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		if !isValidPeerAddress(addr) {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: address %q invalid: %w", act, addr, types.ErrInvalidArguments)}
		}
		role := strings.TrimSpace(typed.Role)
		entry := NetMapPeer{Name: name, Address: addr, Role: role, LastSeen: time.Now()}
		a.mu.Lock()
		if _, exists := a.peers[name]; exists {
			a.mu.Unlock()
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: peer %q already exists: %w", act, name, types.ErrInvalidArguments)}
		}
		a.peers[name] = entry
		a.mu.Unlock()
		return types.CommandOutput{Name: act, Value: entry}
	case NetMapCommandUnregisterPeer:
		typed, ok := args.(NetMapLookupPeerArgs)
		if !ok || strings.TrimSpace(typed.Name) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		name := strings.TrimSpace(typed.Name)
		a.mu.Lock()
		prev, ok := a.peers[name]
		if !ok {
			a.mu.Unlock()
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: peer %q not found: %w", act, name, types.ErrInvalidArguments)}
		}
		delete(a.peers, name)
		a.mu.Unlock()
		return types.CommandOutput{Name: act, Value: prev}
	case NetMapCommandUpdatePeer:
		typed, ok := args.(NetMapUpdatePeerArgs)
		if !ok || strings.TrimSpace(typed.Name) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		name := strings.TrimSpace(typed.Name)
		if typed.NewAddress != "" && !isValidPeerAddress(strings.TrimSpace(typed.NewAddress)) {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: address %q invalid: %w", act, typed.NewAddress, types.ErrInvalidArguments)}
		}
		a.mu.Lock()
		entry, ok := a.peers[name]
		if !ok {
			a.mu.Unlock()
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: peer %q not found: %w", act, name, types.ErrInvalidArguments)}
		}
		if strings.TrimSpace(typed.NewAddress) != "" {
			entry.Address = strings.TrimSpace(typed.NewAddress)
		}
		if typed.NewRole != "" {
			entry.Role = strings.TrimSpace(typed.NewRole)
		}
		if typed.TouchLastSeen {
			entry.LastSeen = time.Now()
		}
		a.peers[name] = entry
		a.mu.Unlock()
		return types.CommandOutput{Name: act, Value: entry}
	case NetMapCommandListPeers:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: a.snapshotPeers()}
	case NetMapCommandLookupPeer:
		typed, ok := args.(NetMapLookupPeerArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		name := strings.TrimSpace(typed.Name)
		addr := strings.TrimSpace(typed.Address)
		if name == "" && addr == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		entry, found, err := a.lookup(name, addr)
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, err)}
		}
		if !found {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: peer not found: %w", act, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: entry}
	case NetMapCommandGetTopology:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		nd, _ := atom.Data("NetMapData")
		var self data.NetMapLocalInfo
		if snapper, ok := nd.(interface{ Snapshot() data.NetMapLocalInfo }); ok {
			self = snapper.Snapshot()
		}
		return types.CommandOutput{Name: act, Value: NetMapTopology{Self: self, Peers: a.snapshotPeers()}}
	}
	return types.CommandOutput{Name: act, Err: fmt.Errorf("command %s: %w", act, types.ErrUnsupportedCommand)}
}

func (a *NetMapAbility) snapshotPeers() []NetMapPeer {
	a.mu.RLock()
	out := make([]NetMapPeer, 0, len(a.peers))
	for _, p := range a.peers {
		out = append(out, p)
	}
	a.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (a *NetMapAbility) lookup(name, addr string) (NetMapPeer, bool, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if name != "" {
		if p, ok := a.peers[name]; ok {
			return p, true, nil
		}
		if addr != "" {
			// name 优先,但若未命中可降级到地址匹配
		} else {
			return NetMapPeer{}, false, nil
		}
	}
	if addr != "" {
		for _, p := range a.peers {
			if p.Address == addr {
				return p, true, nil
			}
		}
	}
	return NetMapPeer{}, false, nil
}

// isValidPeerAddress 允许 host:port / 纯 IP / 主机名(不含协议前缀、不含路径)。
func isValidPeerAddress(addr string) bool {
	if addr == "" {
		return false
	}
	if strings.ContainsAny(addr, " /\\?#") {
		return false
	}
	// 若是 host:port 形式,要求端口可解析
	if strings.LastIndex(addr, ":") > 0 {
		_, _, err := net.SplitHostPort(addr)
		if err != nil {
			return false
		}
		return true
	}
	// 纯 IP
	if ip := net.ParseIP(addr); ip != nil {
		return true
	}
	// 主机名(仅允许字母数字、点、连字符、下划线)
	for _, r := range addr {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}
