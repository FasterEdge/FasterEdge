// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package ability

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/FasterEdge/FasterEdge/types"
)

const (
	CloudRoleCommandDescribe      = "describe"
	CloudRoleCommandSetController = "set_controller"
	CloudRoleCommandGetController = "get_controller"
	CloudRoleCommandRegister      = "register_service"
	CloudRoleCommandUnregister    = "unregister_service"
	CloudRoleCommandListServices  = "list_services"
	CloudRoleCommandSetStatus     = "set_status"
	CloudRoleCommandGetStatus     = "get_status"
	CloudRoleCommandHeartbeat     = "heartbeat"
)

// CloudRoleStatus 描述云端节点的运行状态。
type CloudRoleStatus string

const (
	CloudRoleStatusUnknown  CloudRoleStatus = "unknown"
	CloudRoleStatusHealthy  CloudRoleStatus = "healthy"
	CloudRoleStatusDegraded CloudRoleStatus = "degraded"
	CloudRoleStatusOffline  CloudRoleStatus = "offline"
)

// 服务注册的资源上限(防持令牌对端无界灌入 map/字符串导致 OOM)。
const (
	cloudMaxServiceNameLen     = 128
	cloudMaxServiceVersionLen  = 64
	cloudMaxServiceEndpointLen = 512
	cloudMaxServiceCount       = 512
)

// CloudRoleService 描述本云端节点对外提供的服务。
type CloudRoleService struct {
	Name      string
	Version   string
	Endpoint  string
	UpdatedAt time.Time
}

// CloudRoleDescription 是 describe 命令的返回结构。
type CloudRoleDescription struct {
	Role        string
	Controller  string
	Status      CloudRoleStatus
	Services    []CloudRoleService
	LastBeatAt  time.Time
	Heartbeats  int
	GeneratedAt time.Time
}

// CloudRoleSetControllerArgs 是 set_controller 命令的参数。
type CloudRoleSetControllerArgs struct {
	URL string
}

// CloudRoleRegisterServiceArgs 是 register_service 命令的参数。
type CloudRoleRegisterServiceArgs struct {
	Name     string
	Version  string
	Endpoint string
}

// CloudRoleUnregisterServiceArgs 是 unregister_service 命令的参数。
type CloudRoleUnregisterServiceArgs struct {
	Name string
}

// CloudRoleSetStatusArgs 是 set_status 命令的参数。
type CloudRoleSetStatusArgs struct {
	Status CloudRoleStatus
}

// CloudRoleAbility 扩展 RoleAbility,提供云端节点特有的"控制器/服务/状态"语义。
// 它持有自己的状态,不直接改写 RoleAbility 中的 role 字符串(避免双向耦合);
// 通过 Check 要求 RoleAbility 必须存在并 role == "cloud"。
type CloudRoleAbility struct {
	mu sync.RWMutex

	controller string
	status     CloudRoleStatus
	services   map[string]CloudRoleService // key = service name
	lastBeatAt time.Time
	heartbeats int
}

func NewCloudRoleAbility() *CloudRoleAbility {
	return &CloudRoleAbility{
		status:   CloudRoleStatusUnknown,
		services: make(map[string]CloudRoleService),
	}
}

func (c *CloudRoleAbility) GetName() string { return "CloudRoleAbility" }
func (c *CloudRoleAbility) Dependencies() []types.Dependency {
	return []types.Dependency{{Kind: types.DependencyData, Name: "BaseData"}, {Kind: types.DependencyAbility, Name: "RoleAbility"}}
}

func (c *CloudRoleAbility) Describe() string {
	return "CloudRoleAbility扩展RoleAbility,提供云端节点特有的控制器注册、服务清单、心跳与状态管理能力。"
}

func (c *CloudRoleAbility) Check(atom *types.Atom) error {
	if atom == nil {
		return types.ErrMissingDependency
	}
	if _, ok := atom.Data("BaseData"); !ok {
		return types.ErrMissingDependency
	}
	roleAb, ok := atom.Ability("RoleAbility")
	if !ok {
		return types.ErrMissingDependency
	}
	if out := roleAb.Command(atom, CommandGetRole, nil); out.Err != nil {
		return fmt.Errorf("role check: %w", out.Err)
	} else if role, _ := out.Value.(string); role != "cloud" {
		return fmt.Errorf("role check: expected %q, got %q: %w", "cloud", role, types.ErrMissingDependency)
	}
	return nil
}

func (c *CloudRoleAbility) Mount(atom *types.Atom) error { return c.Check(atom) }

func (c *CloudRoleAbility) Command(atom *types.Atom, act string, args any) types.CommandOutput {
	if err := c.Check(atom); err != nil {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, err)}
	}
	switch act {
	case CloudRoleCommandDescribe:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: c.describeWithRole(atom)}
	case CloudRoleCommandSetController:
		typed, ok := args.(CloudRoleSetControllerArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		url := strings.TrimSpace(typed.URL)
		if url == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: empty url: %w", act, types.ErrInvalidArguments)}
		}
		if !isAcceptableControllerURL(url) {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: invalid controller url %q: %w", act, url, types.ErrInvalidArguments)}
		}
		c.mu.Lock()
		c.controller = url
		c.mu.Unlock()
		return types.CommandOutput{Name: act, Value: url}
	case CloudRoleCommandGetController:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		c.mu.RLock()
		defer c.mu.RUnlock()
		return types.CommandOutput{Name: act, Value: c.controller}
	case CloudRoleCommandRegister:
		typed, ok := args.(CloudRoleRegisterServiceArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		name := strings.TrimSpace(typed.Name)
		if name == "" || len(name) > cloudMaxServiceNameLen ||
			strings.TrimSpace(typed.Name) != typed.Name {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: service name must be 1-%d chars: %w", act, cloudMaxServiceNameLen, types.ErrInvalidArguments)}
		}
		version := strings.TrimSpace(typed.Version)
		endpoint := strings.TrimSpace(typed.Endpoint)
		// 与 edge 能力条目一致设硬上限: 旧实现无界增长, 持令牌对端可灌入
		// 任意大字符串/无限条目导致 OOM。
		if len(version) > cloudMaxServiceVersionLen || len(endpoint) > cloudMaxServiceEndpointLen {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: service version/endpoint too long: %w", act, types.ErrInvalidArguments)}
		}
		svc := CloudRoleService{
			Name:      name,
			Version:   version,
			Endpoint:  endpoint,
			UpdatedAt: time.Now(),
		}
		c.mu.Lock()
		if _, exists := c.services[name]; !exists && len(c.services) >= cloudMaxServiceCount {
			c.mu.Unlock()
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: service limit %d reached: %w", act, cloudMaxServiceCount, types.ErrInvalidArguments)}
		}
		c.services[name] = svc
		c.mu.Unlock()
		return types.CommandOutput{Name: act, Value: svc}
	case CloudRoleCommandUnregister:
		typed, ok := args.(CloudRoleUnregisterServiceArgs)
		if !ok || strings.TrimSpace(typed.Name) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		name := strings.TrimSpace(typed.Name)
		c.mu.Lock()
		prev, ok := c.services[name]
		if !ok {
			c.mu.Unlock()
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: service %q not found: %w", act, name, types.ErrInvalidArguments)}
		}
		delete(c.services, name)
		c.mu.Unlock()
		return types.CommandOutput{Name: act, Value: prev}
	case CloudRoleCommandListServices:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		c.mu.RLock()
		out := make([]CloudRoleService, 0, len(c.services))
		for _, s := range c.services {
			out = append(out, s)
		}
		c.mu.RUnlock()
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		return types.CommandOutput{Name: act, Value: out}
	case CloudRoleCommandSetStatus:
		typed, ok := args.(CloudRoleSetStatusArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		st := normalizeStatus(typed.Status)
		if st == CloudRoleStatusUnknown {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: invalid status %q: %w", act, typed.Status, types.ErrInvalidArguments)}
		}
		c.mu.Lock()
		c.status = st
		c.mu.Unlock()
		return types.CommandOutput{Name: act, Value: st}
	case CloudRoleCommandGetStatus:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		c.mu.RLock()
		defer c.mu.RUnlock()
		return types.CommandOutput{Name: act, Value: c.status}
	case CloudRoleCommandHeartbeat:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		now := time.Now()
		c.mu.Lock()
		c.lastBeatAt = now
		c.heartbeats++
		c.mu.Unlock()
		return types.CommandOutput{Name: act, Value: now}
	}
	return types.CommandOutput{Name: act, Err: fmt.Errorf("command %s: %w", act, types.ErrUnsupportedCommand)}
}

func (c *CloudRoleAbility) snapshot() CloudRoleDescription {
	c.mu.RLock()
	services := make([]CloudRoleService, 0, len(c.services))
	for _, s := range c.services {
		services = append(services, s)
	}
	sort.Slice(services, func(i, j int) bool { return services[i].Name < services[j].Name })
	snap := CloudRoleDescription{
		Controller:  c.controller,
		Status:      c.status,
		Services:    services,
		LastBeatAt:  c.lastBeatAt,
		Heartbeats:  c.heartbeats,
		GeneratedAt: time.Now(),
	}
	c.mu.RUnlock()
	return snap
}

// describeWithRole 在 snapshot 基础上叠加当前 RoleAbility 的 role 字符串,供 describe 命令使用。
// 它由 Command 在持有 atom 上下文时调用,避免 snapshot() 自身再去查 atom 造成递归。
func (c *CloudRoleAbility) describeWithRole(atom *types.Atom) CloudRoleDescription {
	desc := c.snapshot()
	if roleAb, ok := atom.Ability("RoleAbility"); ok {
		if out := roleAb.Command(atom, CommandGetRole, nil); out.Err == nil {
			if role, _ := out.Value.(string); role != "" {
				desc.Role = role
			}
		}
	}
	return desc
}

func normalizeStatus(s CloudRoleStatus) CloudRoleStatus {
	switch strings.ToLower(strings.TrimSpace(string(s))) {
	case "healthy", "ok", "up":
		return CloudRoleStatusHealthy
	case "degraded":
		return CloudRoleStatusDegraded
	case "offline", "down":
		return CloudRoleStatusOffline
	}
	return CloudRoleStatusUnknown
}

// isAcceptableControllerURL 限制 controller 必须是 http(s)://host[:port][/path] 形式,
// 拒绝本地回环、私网、链路本地(与 TimeAbility 网络策略一致, 降低 SSRF 风险)。
// 旧实现仅与 4 个字面量比较, 私网/链路本地/IPv4-mapped IPv6(userinfo 手法)
// 全部可绕过, 与注释宣称的策略完全不符。
func isAcceptableControllerURL(u string) bool {
	lower := strings.ToLower(u)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return false
	}
	parsed, err := url.Parse(u)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil {
		return false
	}
	host := parsed.Hostname()
	// IP 字面量: netip 规范化(IPv4-mapped IPv6)后复用 addressPolicy.validateIP
	// 拒绝回环/私网/链路本地/组播/未指定/CGNAT/特殊用段。
	if addr, aerr := netip.ParseAddr(host); aerr == nil {
		addr = addr.Unmap()
		ip := net.IP(addr.AsSlice())
		return (addressPolicy{allowPrivate: false}).validateIP(ip) == nil
	}
	// 主机名字面回环别名拒绝。其余主机名不在存储期解析: DNS rebinding 的 TOCTOU
	// 使存储期解析价值有限, 且会拒绝对离线/内网域名; 真正拨号时应像
	// TimeAbility 的 addressPolicy.dialContext 那样对解析结果二次校验。
	return strings.TrimSuffix(strings.ToLower(host), ".") != "localhost"
}
