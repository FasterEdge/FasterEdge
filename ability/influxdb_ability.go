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
	InfluxCommandSetEndpoint  = "set_endpoint"
	InfluxCommandGetEndpoint  = "get_endpoint"
	InfluxCommandSetToken     = "set_token"
	InfluxCommandSetOrg       = "set_org"
	InfluxCommandSetBucket    = "set_bucket"
	InfluxCommandGetConfig    = "get_config"
	InfluxCommandWrite        = "write"
	InfluxCommandQuery        = "query"
	InfluxCommandPing         = "ping"
	InfluxCommandListSeries   = "list_series"
	InfluxCommandDeleteSeries = "delete_series"
)

// InfluxConfig 描述 InfluxDB 连接配置。
type InfluxConfig struct {
	Endpoint string
	Token    string
	Org      string
	Bucket   string
}

// InfluxConfigArgs 是 set_* 命令的通用参数。
type InfluxConfigArgs struct {
	Value string
}

// InfluxPoint 是单条时序数据点。
type InfluxPoint struct {
	Measurement string
	Tags        map[string]string
	Fields      map[string]any
	Time        time.Time
}

// InfluxWriteArgs 是 write 命令的参数。
type InfluxWriteArgs struct {
	Points []InfluxPoint
}

// InfluxQueryArgs 是 query 命令的参数。
type InfluxQueryArgs struct {
	Query string
}

// InfluxSeriesArgs 是 list_series / delete_series 的参数。
type InfluxSeriesArgs struct {
	Measurement string
}

// InfluxTransport 抽象出真实 InfluxDB 客户端。
type InfluxTransport interface {
	Ping() error
	Write(points []InfluxPoint) error
	Query(flux string) ([]map[string]any, error)
}

// InfluxAbility 在 Transport 之上提供 InfluxDB 客户端命令,并维护常用 series 元数据。
type InfluxAbility struct {
	mu        sync.RWMutex
	cfg       InfluxConfig
	series    map[string]struct{} // 已写入的 measurement 集合
	transport InfluxTransport
}

func NewInfluxAbility() *InfluxAbility {
	return &InfluxAbility{series: make(map[string]struct{})}
}

func (i *InfluxAbility) SetTransport(t InfluxTransport) {
	i.mu.Lock()
	i.transport = t
	i.mu.Unlock()
}

func (i *InfluxAbility) GetName() string { return "InfluxDBAbility" }

func (i *InfluxAbility) Describe() string {
	return "InfluxDBAbility提供 InfluxDB 客户端能力:连接配置/ping/写入时序点/Flux 查询/series 元数据维护,字节流通过注入的 Transport 完成。"
}

func (i *InfluxAbility) Check(atom *types.Atom) error {
	if atom == nil {
		return types.ErrMissingDependency
	}
	if _, ok := atom.Data("BaseData"); !ok {
		return types.ErrMissingDependency
	}
	return nil
}

func (i *InfluxAbility) Mount(atom *types.Atom) error { return i.Check(atom) }

func (i *InfluxAbility) Command(atom *types.Atom, act string, args any) types.CommandOutput {
	if err := i.Check(atom); err != nil {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, err)}
	}
	switch act {
	case InfluxCommandSetEndpoint:
		return i.setConfigField(act, args, func(c *InfluxConfig, v string) { c.Endpoint = v }, isAcceptableInfluxURL)
	case InfluxCommandSetToken:
		return i.setConfigField(act, args, func(c *InfluxConfig, v string) { c.Token = v }, isAcceptableSecret)
	case InfluxCommandSetOrg:
		return i.setConfigField(act, args, func(c *InfluxConfig, v string) { c.Org = v }, isAcceptableIdentifier)
	case InfluxCommandSetBucket:
		return i.setConfigField(act, args, func(c *InfluxConfig, v string) { c.Bucket = v }, isAcceptableIdentifier)
	case InfluxCommandGetConfig:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		i.mu.RLock()
		defer i.mu.RUnlock()
		return types.CommandOutput{Name: act, Value: i.cfg}
	case InfluxCommandPing:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		i.mu.RLock()
		transport := i.transport
		endpoint := i.cfg.Endpoint
		i.mu.RUnlock()
		if transport == nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no transport: %w", act, types.ErrInvalidArguments)}
		}
		if endpoint == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: endpoint not set: %w", act, types.ErrInvalidArguments)}
		}
		if err := transport.Ping(); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %v", act, types.ErrInvalidArguments, err)}
		}
		return types.CommandOutput{Name: act, Value: true}
	case InfluxCommandWrite:
		typed, ok := args.(InfluxWriteArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		if len(typed.Points) == 0 {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: empty points: %w", act, types.ErrInvalidArguments)}
		}
		for idx, p := range typed.Points {
			if !isAcceptableIdentifier(p.Measurement) {
				return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: point[%d] invalid measurement: %w", act, idx, types.ErrInvalidArguments)}
			}
			if len(p.Fields) == 0 {
				return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: point[%d] has no fields: %w", act, idx, types.ErrInvalidArguments)}
			}
		}
		i.mu.RLock()
		transport := i.transport
		bucket := i.cfg.Bucket
		i.mu.RUnlock()
		if transport == nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no transport: %w", act, types.ErrInvalidArguments)}
		}
		if bucket == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: bucket not set: %w", act, types.ErrInvalidArguments)}
		}
		if err := transport.Write(typed.Points); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %v", act, types.ErrInvalidArguments, err)}
		}
		i.mu.Lock()
		for _, p := range typed.Points {
			i.series[p.Measurement] = struct{}{}
		}
		i.mu.Unlock()
		return types.CommandOutput{Name: act, Value: len(typed.Points)}
	case InfluxCommandQuery:
		typed, ok := args.(InfluxQueryArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		q := strings.TrimSpace(typed.Query)
		if q == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: empty query: %w", act, types.ErrInvalidArguments)}
		}
		i.mu.RLock()
		transport := i.transport
		i.mu.RUnlock()
		if transport == nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no transport: %w", act, types.ErrInvalidArguments)}
		}
		rows, err := transport.Query(q)
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %v", act, types.ErrInvalidArguments, err)}
		}
		return types.CommandOutput{Name: act, Value: rows}
	case InfluxCommandListSeries:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		i.mu.RLock()
		out := make([]string, 0, len(i.series))
		for s := range i.series {
			out = append(out, s)
		}
		i.mu.RUnlock()
		sort.Strings(out)
		return types.CommandOutput{Name: act, Value: out}
	case InfluxCommandDeleteSeries:
		typed, ok := args.(InfluxSeriesArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		name := strings.TrimSpace(typed.Measurement)
		if !isAcceptableIdentifier(name) {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: invalid measurement: %w", act, types.ErrInvalidArguments)}
		}
		i.mu.Lock()
		_, ok = i.series[name]
		delete(i.series, name)
		i.mu.Unlock()
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %q not in local index: %w", act, name, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: name}
	}
	return types.CommandOutput{Name: act, Err: fmt.Errorf("command %s: %w", act, types.ErrUnsupportedCommand)}
}

func (i *InfluxAbility) setConfigField(act string, args any, setter func(*InfluxConfig, string), validate func(string) bool) types.CommandOutput {
	typed, ok := args.(InfluxConfigArgs)
	if !ok {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
	}
	v := strings.TrimSpace(typed.Value)
	if !validate(v) {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: invalid value: %w", act, types.ErrInvalidArguments)}
	}
	i.mu.Lock()
	setter(&i.cfg, v)
	i.mu.Unlock()
	return types.CommandOutput{Name: act, Value: v}
}

func isAcceptableInfluxURL(u string) bool {
	if u == "" {
		return false
	}
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
	if host == "" || host == "localhost" || host == "127.0.0.1" || host == "0.0.0.0" || host == "::1" {
		return false
	}
	return true
}

func isAcceptableSecret(s string) bool {
	return s != "" && len(s) >= 8
}

func isAcceptableIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
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
