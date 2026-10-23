// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
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

const (
	// influxMaxPoints/influxMaxSeries/influxMaxFields 是写路径的资源上限:
	// 旧实现 points/fields/measurement 均无约束, series map 无界增长——
	// 认证边界内的 DoS/内存膨胀面。
	influxMaxPoints         = 1024
	influxMaxSeries         = 1024
	influxMaxFieldsPerPoint = 256
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
	// lastRevision 是最近一次操作交付给 transport 的配置版本。set_token/
	// set_endpoint 后 revision 变化而 transport 仍持旧凭据——继续操作会把
	// 数据写往废弃 endpoint/旧 token(凭据轮换被静默架空)。检测到不一致时
	// 报错并要求重建 transport。
	lastRevision uint64
}

func NewInfluxAbility() *InfluxAbility { return &InfluxAbility{series: map[string]struct{}{}} }
func (i *InfluxAbility) SetTransport(t InfluxTransport) {
	i.mu.Lock()
	i.transport = t
	i.lastRevision = 0
	i.mu.Unlock()
}
func (i *InfluxAbility) GetName() string { return "InfluxDBAbility" }
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
		material, err := db.ConnectionMaterial()
		if err != nil {
			return invalidInflux(act)
		}
		if out := i.staleCredentials(act, material.Revision); out.Err != nil {
			return out
		}
		t := i.getTransport()
		if t == nil {
			return invalidInflux(act)
		}
		if err := t.Ping(); err != nil {
			return operationalInfluxError(act, err)
		}
		i.noteRevision(material.Revision)
		return types.CommandOutput{Name: act, Value: true}
	case InfluxCommandWrite:
		typed, ok := args.(InfluxWriteArgs)
		if !ok || len(typed.Points) == 0 || len(typed.Points) > influxMaxPoints {
			return invalidInflux(act)
		}
		material, err := db.ConnectionMaterial()
		if err != nil || material.Config.Bucket == "" {
			return invalidInflux(act)
		}
		if out := i.staleCredentials(act, material.Revision); out.Err != nil {
			return out
		}
		for _, p := range typed.Points {
			if !isAcceptableIdentifier(p.Measurement) || len(p.Measurement) > 128 ||
				len(p.Fields) == 0 || len(p.Fields) > influxMaxFieldsPerPoint {
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
			if _, exists := i.series[p.Measurement]; !exists && len(i.series) >= influxMaxSeries {
				continue // 超上限的新 measurement 不记入本地目录, 不影响写入
			}
			i.series[p.Measurement] = struct{}{}
		}
		i.lastRevision = material.Revision
		i.mu.Unlock()
		return types.CommandOutput{Name: act, Value: len(typed.Points)}
	case InfluxCommandQuery:
		typed, ok := args.(InfluxQueryArgs)
		if !ok || strings.TrimSpace(typed.Query) == "" {
			return invalidInflux(act)
		}
		material, err := db.ConnectionMaterial()
		if err != nil {
			return invalidInflux(act)
		}
		if out := i.staleCredentials(act, material.Revision); out.Err != nil {
			return out
		}
		t := i.getTransport()
		if t == nil {
			return invalidInflux(act)
		}
		rows, err := t.Query(strings.TrimSpace(typed.Query))
		if err != nil {
			return operationalInfluxError(act, err)
		}
		i.noteRevision(material.Revision)
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
		if !ok {
			return invalidInflux(act)
		}
		name := strings.TrimSpace(typed.Measurement)
		// 旧实现校验用 TrimSpace 后的名、map 查找用原文——" cpu " 永远
		// "not found" 且错误文本回显原文。统一用 trim 后的名。
		if !isAcceptableIdentifier(name) {
			return invalidInflux(act)
		}
		i.mu.Lock()
		_, ok = i.series[name]
		delete(i.series, name)
		i.mu.Unlock()
		if !ok {
			return invalidInflux(act)
		}
		return types.CommandOutput{Name: act, Value: name}
	default:
		return types.CommandOutput{Name: act, Err: fmt.Errorf("command %s: %w", act, types.ErrUnsupportedCommand)}
	}
}
func (i *InfluxAbility) getTransport() InfluxTransport {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.transport
}

// staleCredentials 检查 transport 持有的配置版本与当前是否一致
// (lastRevision==0 表示 SetTransport 后尚未操作——放行)。
func (i *InfluxAbility) staleCredentials(act string, revision uint64) types.CommandOutput {
	i.mu.RLock()
	last := i.lastRevision
	i.mu.RUnlock()
	if last != 0 && revision != last {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: transport holds stale credentials (revision %d != %d), rebuild transport", act, types.ErrOperationFailed, last, revision)}
	}
	return types.CommandOutput{}
}

// noteRevision 记录最近一次成功操作使用的配置版本。
func (i *InfluxAbility) noteRevision(rev uint64) {
	i.mu.Lock()
	i.lastRevision = rev
	i.mu.Unlock()
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
	// 运行期故障(网络/远端)用 ErrOperationFailed 哨兵并以 %w 保留底层错误链:
	// 旧实现包进 ErrInvalidArguments, 故障被误分类为参数错误, 底层链被 %v 截断。
	return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %w", act, types.ErrOperationFailed, err)}
}
func isAcceptableSecret(s string) bool { return len(s) >= 8 }
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
