package data

import (
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/FasterEdge/FasterEdge/types"
)

const (
	NetMapCommandInfo            = "info"
	NetMapCommandSetNodeName     = "set_node_name"
	NetMapCommandInterfaces      = "interfaces"
	NetMapCommandSetDefaultIface = "set_default_iface"
)

// NetMapInterface 描述一个网络接口及其 IPv4 地址。
type NetMapInterface struct {
	Name string
	MAC  string
	IPv4 []string
}

// NetMapLocalInfo 描述本节点的网络拓扑信息快照。
type NetMapLocalInfo struct {
	NodeName      string
	DefaultIface  string
	Interfaces    []NetMapInterface
	HostAddresses []string
	ScannedAt     time.Time
}

// NetMapSetNodeNameArgs 是 set_node_name 命令的参数。
type NetMapSetNodeNameArgs struct{ Name string }

// NetMapSetDefaultIfaceArgs 是 set_default_iface 命令的参数。
type NetMapSetDefaultIfaceArgs struct{ Name string }

// NetMapData 存储本节点网络拓扑相关静态/准静态信息。
// 它不感知对端节点 —— 对等节点表由 NetMapAbility 维护。
type NetMapData struct {
	mu           sync.RWMutex
	nodeName     string
	defaultIface string
	interfaces   []NetMapInterface
	scannedAt    time.Time
}

func NewNetMapData() *NetMapData { return &NetMapData{} }

func (n *NetMapData) GetName() string { return "NetMapData" }

func (n *NetMapData) Describe() string {
	return "NetMapData存储本节点网络拓扑信息,包括节点名、网卡接口、默认出网接口。"
}

func (n *NetMapData) Check(_ *types.Atom) error { return nil }

func (n *NetMapData) Mount(_ *types.Atom) error {
	n.mu.Lock()
	n.refreshInterfacesLocked()
	n.mu.Unlock()
	return nil
}

// refreshInterfacesLocked 重新扫描本机网络接口,仅保留处于 up 状态且有 IPv4 地址的接口。
// 调用方需持有写锁。
func (n *NetMapData) refreshInterfacesLocked() {
	ifaces, err := net.Interfaces()
	if err != nil {
		n.interfaces = nil
		n.scannedAt = time.Now()
		return
	}
	seen := make(map[string]struct{}, len(ifaces))
	out := make([]NetMapInterface, 0, len(ifaces))
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		ips := make([]string, 0, len(addrs))
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			default:
				continue
			}
			if ip == nil {
				continue
			}
			v4 := ip.To4()
			if v4 == nil {
				continue
			}
			ips = append(ips, v4.String())
		}
		if len(ips) == 0 {
			continue
		}
		sort.Strings(ips)
		out = append(out, NetMapInterface{
			Name: iface.Name,
			MAC:  iface.HardwareAddr.String(),
			IPv4: ips,
		})
		seen[iface.Name] = struct{}{}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	n.interfaces = out
	n.scannedAt = time.Now()
	if n.defaultIface != "" {
		if _, ok := seen[n.defaultIface]; !ok {
			n.defaultIface = ""
		}
	}
}

func (n *NetMapData) Command(_ *types.Atom, act string, args any) types.CommandOutput {
	switch act {
	case NetMapCommandInfo:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: n.snapshot()}
	case NetMapCommandSetNodeName:
		typed, ok := args.(NetMapSetNodeNameArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		name := strings.TrimSpace(typed.Name)
		if name == "" || strings.TrimSpace(typed.Name) != typed.Name {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		n.mu.Lock()
		n.nodeName = name
		n.mu.Unlock()
		return types.CommandOutput{Name: act, Value: name}
	case NetMapCommandInterfaces:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		n.mu.Lock()
		n.refreshInterfacesLocked()
		// 旧实现浅拷贝 slice 头(append([]NetMapInterface(nil), ...)): IPv4
		// 与内部共享底层数组, 调用方改写返回值会与并发 Snapshot() 构成数据
		// 竞争——与 snapshot() 的深拷贝纪律保持一致。
		ifaces := make([]NetMapInterface, len(n.interfaces))
		for i, iface := range n.interfaces {
			ifaces[i] = NetMapInterface{
				Name: iface.Name,
				MAC:  iface.MAC,
				IPv4: append([]string(nil), iface.IPv4...),
			}
		}
		n.mu.Unlock()
		return types.CommandOutput{Name: act, Value: ifaces}
	case NetMapCommandSetDefaultIface:
		typed, ok := args.(NetMapSetDefaultIfaceArgs)
		if !ok || strings.TrimSpace(typed.Name) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		n.mu.Lock()
		defer n.mu.Unlock()
		found := false
		for _, iface := range n.interfaces {
			if iface.Name == typed.Name {
				found = true
				break
			}
		}
		if !found {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: interface %q not found: %w", act, typed.Name, types.ErrInvalidArguments)}
		}
		n.defaultIface = typed.Name
		return types.CommandOutput{Name: act, Value: typed.Name}
	}
	return types.CommandOutput{Name: act, Err: fmt.Errorf("command %s: %w", act, types.ErrUnsupportedCommand)}
}

// Snapshot 返回当前本节点信息的深拷贝副本。
func (n *NetMapData) Snapshot() NetMapLocalInfo { return n.snapshot() }

func (n *NetMapData) snapshot() NetMapLocalInfo {
	n.mu.RLock()
	defer n.mu.RUnlock()
	ifaces := make([]NetMapInterface, len(n.interfaces))
	for i, iface := range n.interfaces {
		ifaces[i] = NetMapInterface{
			Name: iface.Name,
			MAC:  iface.MAC,
			IPv4: append([]string(nil), iface.IPv4...),
		}
	}
	hosts := make([]string, 0)
	if n.defaultIface != "" {
		for _, iface := range n.interfaces {
			if iface.Name == n.defaultIface && len(iface.IPv4) > 0 {
				hosts = append(hosts, iface.IPv4...)
				break
			}
		}
	}
	return NetMapLocalInfo{
		NodeName:      n.nodeName,
		DefaultIface:  n.defaultIface,
		Interfaces:    ifaces,
		HostAddresses: hosts,
		ScannedAt:     n.scannedAt,
	}
}
