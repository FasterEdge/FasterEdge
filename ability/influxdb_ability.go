package ability

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/FasterEdge/FasterEdge/data"
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

type InfluxConfig struct {
	Endpoint string
	Token    string // always redacted when returned by get_config
	Org      string
	Bucket   string
}
type InfluxConfigArgs struct{ Value string }
type InfluxPoint struct {
	Measurement string
	Tags        map[string]string
	Fields      map[string]any
	Time        time.Time
}
type InfluxWriteArgs struct{ Points []InfluxPoint }
type InfluxQueryArgs struct{ Query string }
type InfluxSeriesArgs struct{ Measurement string }

type InfluxTransport interface {
	Ping() error
	Write([]InfluxPoint) error
	Query(string) ([]map[string]any, error)
}

type InfluxAbility struct {
	mu        sync.RWMutex
	series    map[string]struct{}
	transport InfluxTransport
}

func NewInfluxAbility() *InfluxAbility                  { return &InfluxAbility{series: map[string]struct{}{}} }
func (i *InfluxAbility) SetTransport(t InfluxTransport) { i.mu.Lock(); i.transport = t; i.mu.Unlock() }
func (i *InfluxAbility) GetName() string                { return "InfluxDBAbility" }
func (i *InfluxAbility) Describe() string {
	return "InfluxDBAbility通过InfluxDBData读取连接配置与Token，并执行时序数据库操作。"
}
func (i *InfluxAbility) Dependencies() []types.Dependency {
	return []types.Dependency{{Kind: types.DependencyData, Name: "BaseData"}, {Kind: types.DependencyData, Name: "InfluxDBData"}}
}
func (i *InfluxAbility) Check(atom *types.Atom) error { _, err := i.database(atom); return err }
func (i *InfluxAbility) Mount(atom *types.Atom) error { return i.Check(atom) }
func (i *InfluxAbility) database(atom *types.Atom) (*data.InfluxDBData, error) {
	if atom == nil {
		return nil, types.ErrMissingDependency
	}
	if _, ok := atom.Data("BaseData"); !ok {
		return nil, types.ErrMissingDependency
	}
	component, ok := atom.Data("InfluxDBData")
	if !ok {
		return nil, types.ErrMissingDependency
	}
	db, ok := component.(*data.InfluxDBData)
	if !ok {
		return nil, types.ErrWrongDependencyType
	}
	return db, nil
}
func (i *InfluxAbility) publicConfig(atom *types.Atom) (data.InfluxDBConfig, error) {
	db, err := i.database(atom)
	if err != nil {
		return data.InfluxDBConfig{}, err
	}
	return db.PublicConfig(), nil
}
func (i *InfluxAbility) Command(atom *types.Atom, act string, args any) types.CommandOutput {
	db, err := i.database(atom)
	if err != nil {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, err)}
	}
	switch act {
	case InfluxCommandSetEndpoint:
		v, ok := configValue(args)
		if !ok {
			return invalidInflux(act)
		}
		if err := db.SetEndpoint(v); err != nil {
			return invalidInflux(act)
		}
		return types.CommandOutput{Name: act, Value: v}
	case InfluxCommandSetToken:
		v, ok := configValue(args)
		if !ok || len(v) < 8 {
			return invalidInflux(act)
		}
		if err := db.SetToken([]byte(v)); err != nil {
			return invalidInflux(act)
		}
		return types.CommandOutput{Name: act, Value: true}
	case InfluxCommandSetOrg:
		v, ok := configValue(args)
		if !ok {
			return invalidInflux(act)
		}
		if err := db.SetOrg(v); err != nil {
			return invalidInflux(act)
		}
		return types.CommandOutput{Name: act, Value: v}
	case InfluxCommandSetBucket:
		v, ok := configValue(args)
		if !ok {
			return invalidInflux(act)
		}
		if err := db.SetBucket(v); err != nil {
			return invalidInflux(act)
		}
		return types.CommandOutput{Name: act, Value: v}
	case InfluxCommandGetEndpoint:
		if args != nil {
			return invalidInflux(act)
		}
		cfg := db.PublicConfig()
		return types.CommandOutput{Name: act, Value: cfg.Endpoint}
	case InfluxCommandGetConfig:
		if args != nil {
			return invalidInflux(act)
		}
		cfg := db.PublicConfig()
		return types.CommandOutput{Name: act, Value: InfluxConfig{Endpoint: cfg.Endpoint, Org: cfg.Org, Bucket: cfg.Bucket}}
	case InfluxCommandPing:
		if args != nil {
			return invalidInflux(act)
		}
		if _, err := db.ConnectionMaterial(); err != nil {
			return invalidInflux(act)
		}
		t := i.getTransport()
		if t == nil {
			return invalidInflux(act)
		}
		if err := t.Ping(); err != nil {
			return operationalInfluxError(act, err)
		}
		return types.CommandOutput{Name: act, Value: true}
	case InfluxCommandWrite:
		typed, ok := args.(InfluxWriteArgs)
		if !ok || len(typed.Points) == 0 {
			return invalidInflux(act)
		}
		material, err := db.ConnectionMaterial()
		if err != nil || material.Config.Bucket == "" {
			return invalidInflux(act)
		}
		for _, p := range typed.Points {
			if !isAcceptableIdentifier(p.Measurement) || len(p.Fields) == 0 {
				return invalidInflux(act)
			}
		}
		t := i.getTransport()
		if t == nil {
			return invalidInflux(act)
		}
		if err := t.Write(typed.Points); err != nil {
			return operationalInfluxError(act, err)
		}
		i.mu.Lock()
		for _, p := range typed.Points {
			i.series[p.Measurement] = struct{}{}
		}
		i.mu.Unlock()
		return types.CommandOutput{Name: act, Value: len(typed.Points)}
	case InfluxCommandQuery:
		typed, ok := args.(InfluxQueryArgs)
		if !ok || strings.TrimSpace(typed.Query) == "" {
			return invalidInflux(act)
		}
		if _, err := db.ConnectionMaterial(); err != nil {
			return invalidInflux(act)
		}
		t := i.getTransport()
		if t == nil {
			return invalidInflux(act)
		}
		rows, err := t.Query(strings.TrimSpace(typed.Query))
		if err != nil {
			return operationalInfluxError(act, err)
		}
		return types.CommandOutput{Name: act, Value: rows}
	case InfluxCommandListSeries:
		if args != nil {
			return invalidInflux(act)
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
		if !ok || !isAcceptableIdentifier(strings.TrimSpace(typed.Measurement)) {
			return invalidInflux(act)
		}
		i.mu.Lock()
		_, ok = i.series[typed.Measurement]
		delete(i.series, typed.Measurement)
		i.mu.Unlock()
		if !ok {
			return invalidInflux(act)
		}
		return types.CommandOutput{Name: act, Value: typed.Measurement}
	default:
		return types.CommandOutput{Name: act, Err: fmt.Errorf("command %s: %w", act, types.ErrUnsupportedCommand)}
	}
}
func (i *InfluxAbility) getTransport() InfluxTransport {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.transport
}
func configValue(args any) (string, bool) {
	typed, ok := args.(InfluxConfigArgs)
	if !ok {
		return "", false
	}
	v := strings.TrimSpace(typed.Value)
	return v, v != ""
}
func invalidInflux(act string) types.CommandOutput {
	return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
}
func operationalInfluxError(act string, err error) types.CommandOutput {
	return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %v", act, types.ErrInvalidArguments, err)}
}
func isAcceptableSecret(s string) bool { return len(s) >= 8 }
func isAcceptableInfluxURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}
func isAcceptableIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}
