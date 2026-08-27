package data

import (
	"strings"

	"github.com/FasterEdge/FasterEdge/types"
)

type InfluxDBData struct{ store databaseStore[InfluxDBConfig] }
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
	cfg, _ := d.store.public()
	cfg.Endpoint = value
	if err := validateInfluxEndpoint(value); err != nil {
		return err
	}
	d.store.configure(cfg)
	return nil
}
func (d *InfluxDBData) SetOrg(value string) error {
	if validateName(value) != nil {
		return types.ErrInvalidArguments
	}
	cfg, _ := d.store.public()
	cfg.Org = value
	d.store.configure(cfg)
	return nil
}
func (d *InfluxDBData) SetBucket(value string) error {
	if validateName(value) != nil {
		return types.ErrInvalidArguments
	}
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
