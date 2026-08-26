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
		if name == "" || strings.TrimSpace(typed.Name) != typed.Name {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		svc := CloudRoleService{
			Name:      name,
			Version:   strings.TrimSpace(typed.Version),
			Endpoint:  strings.TrimSpace(typed.Endpoint),
			UpdatedAt: time.Now(),
		}
		c.mu.Lock()
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

// isAcceptableControllerURL 限制 controller 必须是 http(s)://host[:port][/path] 形式,拒绝本地回环、私网、链路本地。
// 这与 TimeAbility 的网络策略一致,降低 SSRF 风险。
func isAcceptableControllerURL(u string) bool {
	lower := strings.ToLower(u)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return false
	}
	rest := u
	if i := strings.Index(lower, "://"); i >= 0 {
		rest = u[i+3:]
	}
	host := rest
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		host = rest[:i]
	}
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	if host == "" {
		return false
	}
	if host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "0.0.0.0" {
		return false
	}
	return true
}
