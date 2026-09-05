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
	EKuiperCommandSetEndpoint   = "set_endpoint"
	EKuiperCommandGetEndpoint   = "get_endpoint"
	EKuiperCommandCreateStream  = "create_stream"
	EKuiperCommandDropStream    = "drop_stream"
	EKuiperCommandListStreams   = "list_streams"
	EKuiperCommandGetStream     = "get_stream"
	EKuiperCommandCreateRule    = "create_rule"
	EKuiperCommandDropRule      = "drop_rule"
	EKuiperCommandStartRule     = "start_rule"
	EKuiperCommandStopRule      = "stop_rule"
	EKuiperCommandShowRules     = "show_rules"
	EKuiperCommandGetRuleStatus = "get_rule_status"
)

// EKuiperStream 描述一个流定义。
type EKuiperStream struct {
	Name      string
	SQL       string
	CreatedAt time.Time
}

// EKuiperRuleStatus 标识规则运行状态。
type EKuiperRuleStatus string

const (
	EKuiperStatusStopped EKuiperRuleStatus = "stopped"
	EKuiperStatusRunning EKuiperRuleStatus = "running"
	EKuiperStatusFailed  EKuiperRuleStatus = "failed"
)

// EKuiperRule 描述一条流处理规则。
type EKuiperRule struct {
	ID        string
	SQL       string
	Actions   []string
	Status    EKuiperRuleStatus
	CreatedAt time.Time
	StartedAt time.Time
}

// EKuiperEndpointArgs 是 set_endpoint 的参数。
type EKuiperEndpointArgs struct {
	URL string
}

// EKuiperStreamArgs 是 create_stream 的参数。
type EKuiperStreamArgs struct {
	Name string
	SQL  string
}

// EKuiperStreamRef 是 drop_stream / get_stream 的参数。
type EKuiperStreamRef struct {
	Name string
}

// EKuiperCreateRuleArgs 是 create_rule 的参数。
type EKuiperCreateRuleArgs struct {
	ID      string
	SQL     string
	Actions []string
}

// EKuiperRuleIDArg 是 drop / start / stop / get_status 的参数。
type EKuiperRuleIDArg struct {
	ID string
}

// EKuiperTransport 抽象出 eKuiper REST API 客户端。
type EKuiperTransport interface {
	CreateStream(name, sql string) error
	DropStream(name string) error
	CreateRule(id, sql string, actions []string) error
	DropRule(id string) error
	StartRule(id string) error
	StopRule(id string) error
	Ping() error
}

// EKuiperAbility 在 Transport 之上提供 eKuiper 流/规则管理。
type EKuiperAbility struct {
	mu        sync.RWMutex
	endpoint  string
	streams   map[string]*EKuiperStream
	rules     map[string]*EKuiperRule
	transport EKuiperTransport
}

func NewEKuiperAbility() *EKuiperAbility {
	return &EKuiperAbility{
		streams: make(map[string]*EKuiperStream),
		rules:   make(map[string]*EKuiperRule),
	}
}

func (e *EKuiperAbility) SetTransport(t EKuiperTransport) {
	e.mu.Lock()
	e.transport = t
	e.mu.Unlock()
}

func (e *EKuiperAbility) GetName() string { return "EKuiperAbility" }

func (e *EKuiperAbility) Describe() string {
	return "EKuiperAbility提供 eKuiper 流处理引擎管理能力:stream/rule CRUD + start/stop,通过注入的 Transport 桥接真实 eKuiper REST API。"
}

func (e *EKuiperAbility) Check(atom *types.Atom) error {
	if atom == nil {
		return types.ErrMissingDependency
	}
	if _, ok := atom.Data("BaseData"); !ok {
		return types.ErrMissingDependency
	}
	return nil
}

func (e *EKuiperAbility) Mount(atom *types.Atom) error { return e.Check(atom) }

func (e *EKuiperAbility) Command(atom *types.Atom, act string, args any) types.CommandOutput {
	if err := e.Check(atom); err != nil {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, err)}
	}
	switch act {
	case EKuiperCommandSetEndpoint:
		typed, ok := args.(EKuiperEndpointArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		endpoint := strings.TrimSpace(typed.URL)
		// URL 校验与数据层 validateInfluxEndpoint 对齐(旧实现仅查 http/https
		// 前缀——"http://"(无 host)/内嵌 userinfo/回环 IPv6/畸形端口全放行,
		// 凭据随每次 REST 调用重放且 get_endpoint 原样回显)。
		parsed, perr := url.Parse(endpoint)
		if perr != nil || parsed.User != nil || parsed.Fragment != "" ||
			(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: invalid url: %w", act, types.ErrInvalidArguments)}
		}
		if ip := net.ParseIP(parsed.Hostname()); ip != nil {
			addr, ok := netip.AddrFromSlice(ip)
			if !ok {
				return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: invalid url host: %w", act, types.ErrInvalidArguments)}
			}
			addr = addr.Unmap()
			// 与 InfluxDBData 一致拒绝回环/未指定/私网/链路本地/组播;
			// 域名留待拨号层二次校验(存储期不解析, TOCTOU)。
			if addr.IsLoopback() || addr.IsUnspecified() || addr.IsPrivate() ||
				addr.IsLinkLocalUnicast() || addr.IsMulticast() {
				return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: url host not allowed: %w", act, types.ErrInvalidArguments)}
			}
		}
		e.mu.Lock()
		e.endpoint = endpoint
		e.mu.Unlock()
		return types.CommandOutput{Name: act, Value: endpoint}
	case EKuiperCommandGetEndpoint:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		e.mu.RLock()
		defer e.mu.RUnlock()
		return types.CommandOutput{Name: act, Value: e.endpoint}
	case EKuiperCommandCreateStream:
		typed, ok := args.(EKuiperStreamArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		name := strings.TrimSpace(typed.Name)
		sql := strings.TrimSpace(typed.SQL)
		if !isAcceptableIdentifier(name) || sql == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: invalid name/sql: %w", act, types.ErrInvalidArguments)}
		}
		e.mu.RLock()
		transport := e.transport
		_, exists := e.streams[name]
		e.mu.RUnlock()
		if exists {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %q exists: %w", act, name, types.ErrInvalidArguments)}
		}
		if transport == nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no transport: %w", act, types.ErrInvalidArguments)}
		}
		if err := transport.CreateStream(name, sql); err != nil {
			return ekuperOpError(act, err)
		}
		e.mu.Lock()
		e.streams[name] = &EKuiperStream{Name: name, SQL: sql, CreatedAt: time.Now()}
		e.mu.Unlock()
		return types.CommandOutput{Name: act, Value: name}
	case EKuiperCommandDropStream:
		typed, ok := args.(EKuiperStreamRef)
		if !ok || strings.TrimSpace(typed.Name) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		name := strings.TrimSpace(typed.Name)
		e.mu.RLock()
		_, ok = e.streams[name]
		transport := e.transport
		e.mu.RUnlock()
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %q not found: %w", act, name, types.ErrInvalidArguments)}
		}
		// transport 为 nil 时不得静默"成功": 旧实现跳过远端调用直接删本地 map
		// 返回成功——远端 eKuiper 的 stream/rule 原样保留, 调用方收到假"删除成功"。
		if transport == nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no transport: %w", act, types.ErrInvalidArguments)}
		}
		if err := transport.DropStream(name); err != nil {
			return ekuperOpError(act, err)
		}
		e.mu.Lock()
		delete(e.streams, name)
		e.mu.Unlock()
		return types.CommandOutput{Name: act, Value: name}
	case EKuiperCommandListStreams:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		e.mu.RLock()
		out := make([]EKuiperStream, 0, len(e.streams))
		for _, s := range e.streams {
			out = append(out, *s)
		}
		e.mu.RUnlock()
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		return types.CommandOutput{Name: act, Value: out}
	case EKuiperCommandGetStream:
		typed, ok := args.(EKuiperStreamRef)
		if !ok || strings.TrimSpace(typed.Name) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		e.mu.RLock()
		s, ok := e.streams[strings.TrimSpace(typed.Name)]
		e.mu.RUnlock()
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %q not found: %w", act, typed.Name, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: *s}
	case EKuiperCommandCreateRule:
		typed, ok := args.(EKuiperCreateRuleArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		id := strings.TrimSpace(typed.ID)
		sql := strings.TrimSpace(typed.SQL)
		if !isAcceptableIdentifier(id) || sql == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: invalid id/sql: %w", act, types.ErrInvalidArguments)}
		}
		e.mu.RLock()
		_, exists := e.rules[id]
		transport := e.transport
		e.mu.RUnlock()
		if exists {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %q exists: %w", act, id, types.ErrInvalidArguments)}
		}
		if transport == nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no transport: %w", act, types.ErrInvalidArguments)}
		}
		if err := transport.CreateRule(id, sql, typed.Actions); err != nil {
			return ekuperOpError(act, err)
		}
		e.mu.Lock()
		e.rules[id] = &EKuiperRule{
			ID: id, SQL: sql, Actions: append([]string(nil), typed.Actions...),
			Status: EKuiperStatusStopped, CreatedAt: time.Now(),
		}
		e.mu.Unlock()
		return types.CommandOutput{Name: act, Value: id}
	case EKuiperCommandDropRule:
		typed, ok := args.(EKuiperRuleIDArg)
		if !ok || strings.TrimSpace(typed.ID) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		id := strings.TrimSpace(typed.ID)
		e.mu.RLock()
		_, ok = e.rules[id]
		transport := e.transport
		e.mu.RUnlock()
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %q not found: %w", act, id, types.ErrInvalidArguments)}
		}
		if transport == nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no transport: %w", act, types.ErrInvalidArguments)}
		}
		if err := transport.DropRule(id); err != nil {
			return ekuperOpError(act, err)
		}
		e.mu.Lock()
		delete(e.rules, id)
		e.mu.Unlock()
		return types.CommandOutput{Name: act, Value: id}
	case EKuiperCommandStartRule, EKuiperCommandStopRule:
		typed, ok := args.(EKuiperRuleIDArg)
		if !ok || strings.TrimSpace(typed.ID) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		id := strings.TrimSpace(typed.ID)
		e.mu.RLock()
		rule, ok := e.rules[id]
		transport := e.transport
		e.mu.RUnlock()
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %q not found: %w", act, id, types.ErrInvalidArguments)}
		}
		if transport == nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no transport: %w", act, types.ErrInvalidArguments)}
		}
		var runErr error
		if act == EKuiperCommandStartRule {
			runErr = transport.StartRule(id)
		} else {
			runErr = transport.StopRule(id)
		}
		if runErr != nil {
			return ekuperOpError(act, runErr)
		}
		e.mu.Lock()
		if act == EKuiperCommandStartRule {
			rule.Status = EKuiperStatusRunning
			rule.StartedAt = time.Now()
		} else {
			rule.Status = EKuiperStatusStopped
		}
		e.mu.Unlock()
		return types.CommandOutput{Name: act, Value: id}
	case EKuiperCommandShowRules:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		e.mu.RLock()
		out := make([]EKuiperRule, 0, len(e.rules))
		for _, r := range e.rules {
			out = append(out, *r)
		}
		e.mu.RUnlock()
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return types.CommandOutput{Name: act, Value: out}
	case EKuiperCommandGetRuleStatus:
		typed, ok := args.(EKuiperRuleIDArg)
		if !ok || strings.TrimSpace(typed.ID) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		e.mu.RLock()
		rule, ok := e.rules[strings.TrimSpace(typed.ID)]
		e.mu.RUnlock()
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %q not found: %w", act, typed.ID, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: rule.Status}
	}
	return types.CommandOutput{Name: act, Err: fmt.Errorf("command %s: %w", act, types.ErrUnsupportedCommand)}
}

func ekuperOpError(act string, err error) types.CommandOutput {
	// 运行期故障(远端 REST 调用失败)用 ErrOperationFailed 哨兵并以 %w
	// 保留底层链——旧实现包进 ErrInvalidArguments 且用 %v 截断链。
	return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %w", act, types.ErrOperationFailed, err)}
}
