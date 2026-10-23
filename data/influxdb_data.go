package data

import (
	"strings"
	"sync"

	"github.com/FasterEdge/FasterEdge/types"
)

// InfluxDBData 的 setter(set_endpoint/set_org/set_bucket)是 read-modify-write:
// 旧实现先 public() 读整份 config 再 configure() 整块覆盖, 并发配置命令会
// 互相覆盖(先落盘的字段被后落盘的整份替换)。mu 把字段级 RMW 串行化,
// store 内部锁在 mu 内嵌套(顺序固定, 无死锁)。
type InfluxDBData struct {
	store databaseStore[InfluxDBConfig]
	mu    sync.Mutex
}
type InfluxDBConfigureArgs struct{ Config InfluxDBConfig }
type InfluxDBSnapshot struct {
	Version string         `json:"version"`
	Config  InfluxDBConfig `json:"config"`
	Status  DatabaseStatus `json:"status"`
}
type InfluxDBConnectionMaterial struct {
	Config   InfluxDBConfig
	Token    []byte
	Revision uint64
}

func NewInfluxDBData() *InfluxDBData    { return &InfluxDBData{} }
func (d *InfluxDBData) GetName() string { return "InfluxDBData" }
func (d *InfluxDBData) Describe() string {
	return "InfluxDBData安全保存InfluxDB公开连接配置与独立Token。"
}
func (d *InfluxDBData) Check(*types.Atom) error { return nil }
func (d *InfluxDBData) Mount(*types.Atom) error { return nil }
func (d *InfluxDBData) ListCommands() []string  { return databaseSecretCommands() }
func (d *InfluxDBData) Command(_ *types.Atom, act string, args any) types.CommandOutput {
	d.mu.Lock()
	defer d.mu.Unlock()
	// mu 同时串行化 Command 路径与 Setter 路径: 旧实现只给 Setter 持 mu,
	// Command(configure) 直接走 store——setter 的 public()→configure()
	// 读改写区间可与并发 configure 命令互踩, 配置整体丢失(文件头注释
	// 声称修复的"互相覆盖"在混用路径下仍存在)。锁序与 setter 一致
	// (mu 先于 store 内部锁), 无死锁。
	switch act {
	case DatabaseCommandConfigure:
		typed, ok := args.(InfluxDBConfigureArgs)
		if !ok || validateInfluxDBConfig(typed.Config) != nil {
			return databaseError(act, types.ErrInvalidArguments)
		}
		return types.CommandOutput{Name: act, Value: d.store.configure(typed.Config)}
	case DatabaseCommandSetSecret:
		return setDatabaseSecret(&d.store, act, args)
	case DatabaseCommandClearSecret:
		return clearDatabaseSecret(&d.store, act, args)
	case DatabaseCommandGetConfig:
		if args != nil {
			return databaseError(act, types.ErrInvalidArguments)
		}
		cfg, _ := d.store.public()
		return types.CommandOutput{Name: act, Value: cfg}
	case DatabaseCommandStatus:
		if args != nil {
			return databaseError(act, types.ErrInvalidArguments)
		}
		_, status := d.store.public()
		return types.CommandOutput{Name: act, Value: status}
	case DatabaseCommandSnapshot:
		if args != nil {
			return databaseError(act, types.ErrInvalidArguments)
		}
		cfg, status := d.store.public()
		return types.CommandOutput{Name: act, Value: InfluxDBSnapshot{"1", cfg, status}}
	default:
		return databaseError(act, types.ErrUnsupportedCommand)
	}
}

// ConnectionMaterial is intentionally not exposed through Command. It gives a
// trusted Ability an owned token copy for an outbound database operation.
func (d *InfluxDBData) PublicConfig() InfluxDBConfig {
	cfg, _ := d.store.public()
	return cfg
}

func (d *InfluxDBData) SetEndpoint(value string) error {
	if err := validateInfluxEndpoint(value); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	cfg, _ := d.store.public()
	cfg.Endpoint = value
	d.store.configure(cfg)
	return nil
}
func (d *InfluxDBData) SetOrg(value string) error {
	if validateName(value) != nil {
		return types.ErrInvalidArguments
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	cfg, _ := d.store.public()
	cfg.Org = value
	d.store.configure(cfg)
	return nil
}
func (d *InfluxDBData) SetBucket(value string) error {
	if validateName(value) != nil {
		return types.ErrInvalidArguments
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	cfg, _ := d.store.public()
	cfg.Bucket = value
	d.store.configure(cfg)
	return nil
}
func (d *InfluxDBData) SetToken(value []byte) error {
	if len(value) < 8 {
		return types.ErrInvalidArguments
	}
	d.store.setSecret(value)
	return nil
}

func (d *InfluxDBData) ConnectionMaterial() (InfluxDBConnectionMaterial, error) {
	cfg, token, status := d.store.material()
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return InfluxDBConnectionMaterial{}, types.ErrInvalidArguments
	}
	return InfluxDBConnectionMaterial{Config: cfg, Token: token, Revision: status.Revision}, nil
}
func (d *InfluxDBData) JSONMarshal() ([]byte, error) {
	cfg, st := d.store.public()
	return databaseJSON(cfg, st)
}
