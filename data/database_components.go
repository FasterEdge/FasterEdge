package data

import (
	"fmt"

	"github.com/FasterEdge/FasterEdge/types"
)

// SQL-backed database components.
type MySQLData struct {
	store databaseStore[SQLDatabaseConfig]
}
type PostgreSQLData struct {
	store databaseStore[SQLDatabaseConfig]
}

type SQLDatabaseConfigureArgs struct{ Config SQLDatabaseConfig }
type SQLDatabaseSnapshot struct {
	Version string            `json:"version"`
	Config  SQLDatabaseConfig `json:"config"`
	Status  DatabaseStatus    `json:"status"`
}

func NewMySQLData() *MySQLData            { return &MySQLData{} }
func NewPostgreSQLData() *PostgreSQLData  { return &PostgreSQLData{} }
func (d *MySQLData) GetName() string      { return "MySQLData" }
func (d *PostgreSQLData) GetName() string { return "PostgreSQLData" }
func (d *MySQLData) Describe() string {
	return "MySQLData安全保存MySQL公开连接配置与独立密码。"
}
func (d *PostgreSQLData) Describe() string {
	return "PostgreSQLData安全保存PostgreSQL公开连接配置与独立密码。"
}
func (d *MySQLData) Check(*types.Atom) error      { return nil }
func (d *PostgreSQLData) Check(*types.Atom) error { return nil }
func (d *MySQLData) Mount(*types.Atom) error      { return nil }
func (d *PostgreSQLData) Mount(*types.Atom) error { return nil }
func (d *MySQLData) ListCommands() []string       { return databaseSecretCommands() }
func (d *PostgreSQLData) ListCommands() []string  { return databaseSecretCommands() }
func (d *MySQLData) Command(_ *types.Atom, act string, args any) types.CommandOutput {
	return sqlCommand(&d.store, act, args)
}
func (d *PostgreSQLData) Command(_ *types.Atom, act string, args any) types.CommandOutput {
	return sqlCommand(&d.store, act, args)
}
func sqlCommand(store *databaseStore[SQLDatabaseConfig], act string, args any) types.CommandOutput {
	switch act {
	case DatabaseCommandConfigure:
		typed, ok := args.(SQLDatabaseConfigureArgs)
		if !ok || validateSQLConfig(typed.Config) != nil {
			return databaseError(act, types.ErrInvalidArguments)
		}
		return types.CommandOutput{Name: act, Value: store.configure(typed.Config)}
	case DatabaseCommandSetSecret:
		return setDatabaseSecret(store, act, args)
	case DatabaseCommandClearSecret:
		return clearDatabaseSecret(store, act, args)
	case DatabaseCommandGetConfig:
		if args != nil {
			return databaseError(act, types.ErrInvalidArguments)
		}
		cfg, _ := store.public()
		return types.CommandOutput{Name: act, Value: cfg}
	case DatabaseCommandStatus:
		if args != nil {
			return databaseError(act, types.ErrInvalidArguments)
		}
		_, status := store.public()
		return types.CommandOutput{Name: act, Value: status}
	case DatabaseCommandSnapshot:
		if args != nil {
			return databaseError(act, types.ErrInvalidArguments)
		}
		cfg, status := store.public()
		return types.CommandOutput{Name: act, Value: SQLDatabaseSnapshot{"1", cfg, status}}
	default:
		return databaseError(act, types.ErrUnsupportedCommand)
	}
}

// SQLiteData stores local SQLite configuration and has no secret.
type SQLiteData struct{ store databaseStore[SQLiteConfig] }
type SQLiteConfigureArgs struct{ Config SQLiteConfig }
type SQLiteSnapshot struct {
	Version string         `json:"version"`
	Config  SQLiteConfig   `json:"config"`
	Status  DatabaseStatus `json:"status"`
}

func NewSQLiteData() *SQLiteData              { return &SQLiteData{} }
func (d *SQLiteData) GetName() string         { return "SQLiteData" }
func (d *SQLiteData) Describe() string        { return "SQLiteData保存SQLite文件和运行参数。" }
func (d *SQLiteData) Check(*types.Atom) error { return nil }
func (d *SQLiteData) Mount(*types.Atom) error { return nil }
func (d *SQLiteData) ListCommands() []string {
	return []string{DatabaseCommandConfigure, DatabaseCommandGetConfig, DatabaseCommandStatus, DatabaseCommandSnapshot}
}
func (d *SQLiteData) Command(_ *types.Atom, act string, args any) types.CommandOutput {
	switch act {
	case DatabaseCommandConfigure:
		typed, ok := args.(SQLiteConfigureArgs)
		if !ok || validateSQLiteConfig(typed.Config) != nil {
			return databaseError(act, types.ErrInvalidArguments)
		}
		return types.CommandOutput{Name: act, Value: d.store.configure(typed.Config)}
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
		return types.CommandOutput{Name: act, Value: SQLiteSnapshot{"1", cfg, status}}
	default:
		return databaseError(act, types.ErrUnsupportedCommand)
	}
}

// RedisData stores Redis connection configuration and a private password.
type RedisData struct{ store databaseStore[RedisConfig] }
type RedisConfigureArgs struct{ Config RedisConfig }
type RedisSnapshot struct {
	Version string         `json:"version"`
	Config  RedisConfig    `json:"config"`
	Status  DatabaseStatus `json:"status"`
}

func NewRedisData() *RedisData       { return &RedisData{} }
func (d *RedisData) GetName() string { return "RedisData" }
func (d *RedisData) Describe() string {
	return "RedisData安全保存Redis公开连接配置与独立密码。"
}
func (d *RedisData) Check(*types.Atom) error { return nil }
func (d *RedisData) Mount(*types.Atom) error { return nil }
func (d *RedisData) ListCommands() []string  { return databaseSecretCommands() }
func (d *RedisData) Command(_ *types.Atom, act string, args any) types.CommandOutput {
	switch act {
	case DatabaseCommandConfigure:
		typed, ok := args.(RedisConfigureArgs)
		if !ok || validateRedisConfig(typed.Config) != nil {
			return databaseError(act, types.ErrInvalidArguments)
		}
		cfg := cloneRedisConfig(typed.Config)
		return types.CommandOutput{Name: act, Value: d.store.configure(cfg)}
	case DatabaseCommandSetSecret:
		return setDatabaseSecret(&d.store, act, args)
	case DatabaseCommandClearSecret:
		return clearDatabaseSecret(&d.store, act, args)
	case DatabaseCommandGetConfig:
		if args != nil {
			return databaseError(act, types.ErrInvalidArguments)
		}
		cfg, _ := d.store.public()
		return types.CommandOutput{Name: act, Value: cloneRedisConfig(cfg)}
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
		return types.CommandOutput{Name: act, Value: RedisSnapshot{"1", cloneRedisConfig(cfg), status}}
	default:
		return databaseError(act, types.ErrUnsupportedCommand)
	}
}

// MongoDBData stores MongoDB connection configuration and a private password.
type MongoDBData struct{ store databaseStore[MongoDBConfig] }
type MongoDBConfigureArgs struct{ Config MongoDBConfig }
type MongoDBSnapshot struct {
	Version string         `json:"version"`
	Config  MongoDBConfig  `json:"config"`
	Status  DatabaseStatus `json:"status"`
}

func NewMongoDBData() *MongoDBData     { return &MongoDBData{} }
func (d *MongoDBData) GetName() string { return "MongoDBData" }
func (d *MongoDBData) Describe() string {
	return "MongoDBData安全保存MongoDB公开连接配置与独立密码。"
}
func (d *MongoDBData) Check(*types.Atom) error { return nil }
func (d *MongoDBData) Mount(*types.Atom) error { return nil }
func (d *MongoDBData) ListCommands() []string  { return databaseSecretCommands() }
func (d *MongoDBData) Command(_ *types.Atom, act string, args any) types.CommandOutput {
	switch act {
	case DatabaseCommandConfigure:
		typed, ok := args.(MongoDBConfigureArgs)
		if !ok || validateMongoDBConfig(typed.Config) != nil {
			return databaseError(act, types.ErrInvalidArguments)
		}
		cfg := cloneMongoDBConfig(typed.Config)
		return types.CommandOutput{Name: act, Value: d.store.configure(cfg)}
	case DatabaseCommandSetSecret:
		return setDatabaseSecret(&d.store, act, args)
	case DatabaseCommandClearSecret:
		return clearDatabaseSecret(&d.store, act, args)
	case DatabaseCommandGetConfig:
		if args != nil {
			return databaseError(act, types.ErrInvalidArguments)
		}
		cfg, _ := d.store.public()
		return types.CommandOutput{Name: act, Value: cloneMongoDBConfig(cfg)}
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
		return types.CommandOutput{Name: act, Value: MongoDBSnapshot{"1", cloneMongoDBConfig(cfg), status}}
	default:
		return databaseError(act, types.ErrUnsupportedCommand)
	}
}

func databaseSecretCommands() []string {
	return []string{DatabaseCommandConfigure, DatabaseCommandSetSecret, DatabaseCommandClearSecret, DatabaseCommandGetConfig, DatabaseCommandStatus, DatabaseCommandSnapshot}
}
func setDatabaseSecret[T any](store *databaseStore[T], act string, args any) types.CommandOutput {
	typed, ok := args.(DatabaseSetSecretArgs)
	if !ok || len(typed.Secret) < 8 {
		return databaseError(act, types.ErrInvalidArguments)
	}
	return types.CommandOutput{Name: act, Value: store.setSecret(typed.Secret)}
}
func clearDatabaseSecret[T any](store *databaseStore[T], act string, args any) types.CommandOutput {
	if args != nil {
		return databaseError(act, types.ErrInvalidArguments)
	}
	return types.CommandOutput{Name: act, Value: store.clearSecret()}
}

func databaseJSON(config any, status DatabaseStatus) ([]byte, error) {
	return databaseSnapshotJSON(struct {
		Version string         `json:"version"`
		Config  any            `json:"config"`
		Status  DatabaseStatus `json:"status"`
	}{"1", config, status})
}
func (d *MySQLData) JSONMarshal() ([]byte, error) {
	cfg, st := d.store.public()
	return databaseJSON(cfg, st)
}
func (d *PostgreSQLData) JSONMarshal() ([]byte, error) {
	cfg, st := d.store.public()
	return databaseJSON(cfg, st)
}
func (d *SQLiteData) JSONMarshal() ([]byte, error) {
	cfg, st := d.store.public()
	return databaseJSON(cfg, st)
}
func (d *RedisData) JSONMarshal() ([]byte, error) {
	cfg, st := d.store.public()
	return databaseJSON(cloneRedisConfig(cfg), st)
}
func (d *MongoDBData) JSONMarshal() ([]byte, error) {
	cfg, st := d.store.public()
	return databaseJSON(cloneMongoDBConfig(cfg), st)
}

var _ = fmt.Sprintf
