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
	EdgeRoleCommandDescribe      = "describe"
	EdgeRoleCommandSetZone       = "set_zone"
	EdgeRoleCommandGetZone       = "get_zone"
	EdgeRoleCommandAddCap        = "add_capability"
	EdgeRoleCommandRemoveCap     = "remove_capability"
	EdgeRoleCommandListCaps      = "list_capabilities"
	EdgeRoleCommandSetCaps       = "set_capabilities"
	EdgeRoleCommandRecordLatency = "record_latency"
	EdgeRoleCommandGetMetrics    = "get_metrics"
	EdgeRoleCommandSetOnline     = "set_online"
)

const (
	// edgeMaxZoneLen/edgeMaxCapLen/edgeMaxCapCount 是 zone/能力条目的资源上限:
	// 旧实现无界增长, 持有效令牌的对端(含被攻陷边缘设备)可反复
	// add_capability/set_capabilities 灌入任意大 map 与字符串导致 OOM。
	edgeMaxZoneLen  = 64
	edgeMaxCapLen   = 128
	edgeMaxCapCount = 256
)

// EdgeRoleMetrics 描述边缘节点的本地运行指标。
type EdgeRoleMetrics struct {
	Online         bool
	Zone           string
	Capabilities   []string
	AvgLatencyMs   float64
	SampleCount    int
	LastSeenAt     time.Time
	LastLatencyAt  time.Time
	LatencySamples int
	GeneratedAt    time.Time
}

// EdgeRoleDescription 是 describe 命令的返回结构。
type EdgeRoleDescription struct {
	Role    string
	Zone    string
	Online  bool
	Caps    []string
	Metrics EdgeRoleMetrics
}

// EdgeRoleSetZoneArgs 是 set_zone 命令的参数。
type EdgeRoleSetZoneArgs struct {
	Zone string
}

// EdgeRoleCapabilityArg 通用能力条目(add/remove/list 用)。
type EdgeRoleCapabilityArg struct {
	Name string
}

// EdgeRoleSetCapabilitiesArgs 是 set_capabilities 命令的参数,用于整体覆盖。
type EdgeRoleSetCapabilitiesArgs struct {
	Capabilities []string
}

// EdgeRoleRecordLatencyArgs 是 record_latency 命令的参数。
type EdgeRoleRecordLatencyArgs struct {
	LatencyMs float64
}

// EdgeRoleSetOnlineArgs 是 set_online 命令的参数。
type EdgeRoleSetOnlineArgs struct {
	Online bool
}

// EdgeRoleAbility 扩展 RoleAbility,提供边缘节点特有的"区域/能力/在线状态/延迟指标"语义。
type EdgeRoleAbility struct {
	mu sync.RWMutex

	zone         string
	capabilities map[string]struct{}
	online       bool

	latencySumMs  float64
	latencyCount  int
	lastLatencyMs float64
	lastLatencyAt time.Time
	lastSeenAt    time.Time
}

func NewEdgeRoleAbility() *EdgeRoleAbility {
	return &EdgeRoleAbility{
		capabilities: make(map[string]struct{}),
	}
}

func (e *EdgeRoleAbility) GetName() string { return "EdgeRoleAbility" }
func (e *EdgeRoleAbility) Dependencies() []types.Dependency {
	return []types.Dependency{{Kind: types.DependencyData, Name: "BaseData"}, {Kind: types.DependencyAbility, Name: "RoleAbility"}}
}

func (e *EdgeRoleAbility) Describe() string {
	return "EdgeRoleAbility扩展RoleAbility,提供边缘节点特有的区域(zone)、本地能力清单、在线状态与延迟指标管理能力。"
}

func (e *EdgeRoleAbility) Check(atom *types.Atom) error {
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
	} else if role, _ := out.Value.(string); role != "edge" {
		return fmt.Errorf("role check: expected %q, got %q: %w", "edge", role, types.ErrMissingDependency)
	}
	return nil
}

func (e *EdgeRoleAbility) Mount(atom *types.Atom) error { return e.Check(atom) }

func (e *EdgeRoleAbility) Command(atom *types.Atom, act string, args any) types.CommandOutput {
	if err := e.Check(atom); err != nil {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, err)}
	}
	switch act {
	case EdgeRoleCommandDescribe:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: e.describeWithRole(atom)}
	case EdgeRoleCommandSetZone:
		typed, ok := args.(EdgeRoleSetZoneArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		zone := strings.TrimSpace(typed.Zone)
		if zone == "" || len(zone) > edgeMaxZoneLen {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: zone must be 1-%d chars: %w", act, edgeMaxZoneLen, types.ErrInvalidArguments)}
		}
		e.mu.Lock()
		e.zone = zone
		e.lastSeenAt = time.Now()
		e.mu.Unlock()
		return types.CommandOutput{Name: act, Value: zone}
	case EdgeRoleCommandGetZone:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		e.mu.RLock()
		defer e.mu.RUnlock()
		return types.CommandOutput{Name: act, Value: e.zone}
	case EdgeRoleCommandAddCap:
		typed, ok := args.(EdgeRoleCapabilityArg)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		name := strings.TrimSpace(typed.Name)
		if name == "" || len(name) > edgeMaxCapLen {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: capability must be 1-%d chars: %w", act, edgeMaxCapLen, types.ErrInvalidArguments)}
		}
		e.mu.Lock()
		// 已达上限时仅允许更新已存在条目(去重插入不增长), 新条目拒绝。
		if len(e.capabilities) >= edgeMaxCapCount {
			if _, exists := e.capabilities[name]; !exists {
				e.mu.Unlock()
				return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: capability limit %d reached: %w", act, edgeMaxCapCount, types.ErrInvalidArguments)}
			}
		}
		e.capabilities[name] = struct{}{}
		e.lastSeenAt = time.Now()
		e.mu.Unlock()
		return types.CommandOutput{Name: act, Value: name}
	case EdgeRoleCommandRemoveCap:
		typed, ok := args.(EdgeRoleCapabilityArg)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		name := strings.TrimSpace(typed.Name)
		if name == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: empty capability: %w", act, types.ErrInvalidArguments)}
		}
		e.mu.Lock()
		if _, ok := e.capabilities[name]; !ok {
			e.mu.Unlock()
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: capability %q not found: %w", act, name, types.ErrInvalidArguments)}
		}
		delete(e.capabilities, name)
		e.lastSeenAt = time.Now()
		e.mu.Unlock()
		return types.CommandOutput{Name: act, Value: name}
	case EdgeRoleCommandListCaps:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: e.snapshotCaps()}
	case EdgeRoleCommandSetCaps:
		typed, ok := args.(EdgeRoleSetCapabilitiesArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		set := make(map[string]struct{}, len(typed.Capabilities))
		if len(typed.Capabilities) > edgeMaxCapCount {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: capability list exceeds %d: %w", act, edgeMaxCapCount, types.ErrInvalidArguments)}
		}
		for _, c := range typed.Capabilities {
			c = strings.TrimSpace(c)
			if c == "" || len(c) > edgeMaxCapLen {
				return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: capability must be 1-%d chars: %w", act, edgeMaxCapLen, types.ErrInvalidArguments)}
			}
			set[c] = struct{}{}
		}
		e.mu.Lock()
		e.capabilities = set
		e.lastSeenAt = time.Now()
		e.mu.Unlock()
		return types.CommandOutput{Name: act, Value: e.snapshotCaps()}
	case EdgeRoleCommandRecordLatency:
		typed, ok := args.(EdgeRoleRecordLatencyArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		if typed.LatencyMs < 0 {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: latency must be non-negative: %w", act, types.ErrInvalidArguments)}
		}
		now := time.Now()
		e.mu.Lock()
		e.latencySumMs += typed.LatencyMs
		e.latencyCount++
		e.lastLatencyMs = typed.LatencyMs
		e.lastLatencyAt = now
		e.lastSeenAt = now
		e.mu.Unlock()
		return types.CommandOutput{Name: act, Value: typed.LatencyMs}
	case EdgeRoleCommandGetMetrics:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: e.metrics()}
	case EdgeRoleCommandSetOnline:
		typed, ok := args.(EdgeRoleSetOnlineArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		e.mu.Lock()
		e.online = typed.Online
		e.lastSeenAt = time.Now()
		e.mu.Unlock()
		return types.CommandOutput{Name: act, Value: typed.Online}
	}
	return types.CommandOutput{Name: act, Err: fmt.Errorf("command %s: %w", act, types.ErrUnsupportedCommand)}
}

func (e *EdgeRoleAbility) snapshotCaps() []string {
	e.mu.RLock()
	out := make([]string, 0, len(e.capabilities))
	for c := range e.capabilities {
		out = append(out, c)
	}
	e.mu.RUnlock()
	sort.Strings(out)
	return out
}

func (e *EdgeRoleAbility) metrics() EdgeRoleMetrics {
	e.mu.RLock()
	defer e.mu.RUnlock()
	avg := 0.0
	if e.latencyCount > 0 {
		avg = e.latencySumMs / float64(e.latencyCount)
	}
	return EdgeRoleMetrics{
		Online:         e.online,
		Zone:           e.zone,
		Capabilities:   append([]string(nil), e.snapshotCapsLocked()...),
		AvgLatencyMs:   avg,
		SampleCount:    e.latencyCount,
		LastSeenAt:     e.lastSeenAt,
		LastLatencyAt:  e.lastLatencyAt,
		LatencySamples: e.latencyCount,
		GeneratedAt:    time.Now(),
	}
}

func (e *EdgeRoleAbility) snapshotCapsLocked() []string {
	out := make([]string, 0, len(e.capabilities))
	for c := range e.capabilities {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

func (e *EdgeRoleAbility) describeWithRole(atom *types.Atom) EdgeRoleDescription {
	m := e.metrics()
	role := ""
	if roleAb, ok := atom.Ability("RoleAbility"); ok {
		if out := roleAb.Command(atom, CommandGetRole, nil); out.Err == nil {
			role, _ = out.Value.(string)
		}
	}
	return EdgeRoleDescription{
		Role:    role,
		Zone:    m.Zone,
		Online:  m.Online,
		Caps:    m.Capabilities,
		Metrics: m,
	}
}
