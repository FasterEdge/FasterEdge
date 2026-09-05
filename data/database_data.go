package data

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/FasterEdge/FasterEdge/types"
)

const (
	DatabaseCommandConfigure   = "configure"
	DatabaseCommandSetSecret   = "set_secret"
	DatabaseCommandClearSecret = "clear_secret"
	DatabaseCommandGetConfig   = "get_config"
	DatabaseCommandStatus      = "status"
	DatabaseCommandSnapshot    = "snapshot"
)

type DatabaseTLSMode string

const (
	DatabaseTLSDisable   DatabaseTLSMode = "disable"
	DatabaseTLSPrefer    DatabaseTLSMode = "prefer"
	DatabaseTLSRequire   DatabaseTLSMode = "require"
	DatabaseTLSVerifyCA  DatabaseTLSMode = "verify-ca"
	DatabaseTLSVerifyAll DatabaseTLSMode = "verify-full"
)

type DatabaseStatus struct {
	Configured       bool      `json:"configured"`
	SecretConfigured bool      `json:"secretConfigured"`
	Revision         uint64    `json:"revision"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

type DatabaseSetSecretArgs struct{ Secret []byte }

type databaseStore[T any] struct {
	mu     sync.RWMutex
	config T
	secret []byte
	status DatabaseStatus
}

func (s *databaseStore[T]) configure(config T) DatabaseStatus {
	s.mu.Lock()
	s.config = config
	s.status.Configured = true
	s.status.Revision++
	s.status.UpdatedAt = time.Now()
	status := s.status
	s.mu.Unlock()
	return status
}

func (s *databaseStore[T]) setSecret(secret []byte) DatabaseStatus {
	copySecret := append([]byte(nil), secret...)
	s.mu.Lock()
	zeroBytes(s.secret)
	s.secret = copySecret
	s.status.SecretConfigured = true
	s.status.Revision++
	s.status.UpdatedAt = time.Now()
	status := s.status
	s.mu.Unlock()
	return status
}

func (s *databaseStore[T]) clearSecret() DatabaseStatus {
	s.mu.Lock()
	zeroBytes(s.secret)
	s.secret = nil
	s.status.SecretConfigured = false
	s.status.Revision++
	s.status.UpdatedAt = time.Now()
	status := s.status
	s.mu.Unlock()
	return status
}

func (s *databaseStore[T]) public() (T, DatabaseStatus) {
	s.mu.RLock()
	config, status := s.config, s.status
	s.mu.RUnlock()
	return config, status
}

func (s *databaseStore[T]) material() (T, []byte, DatabaseStatus) {
	s.mu.RLock()
	config, status := s.config, s.status
	secret := append([]byte(nil), s.secret...)
	s.mu.RUnlock()
	return config, secret, status
}

func zeroBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}

func validTLS(mode DatabaseTLSMode) bool {
	switch mode {
	case DatabaseTLSDisable, DatabaseTLSPrefer, DatabaseTLSRequire, DatabaseTLSVerifyCA, DatabaseTLSVerifyAll:
		return true
	default:
		return false
	}
}

func validateDatabaseHost(host string, port uint16) error {
	host = strings.TrimSpace(host)
	if host == "" || strings.ContainsAny(host, " /?#@") || port == 0 {
		return types.ErrInvalidArguments
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast()) {
		return types.ErrInvalidArguments
	}
	return nil
}

func validateAddress(address string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil || port == "" {
		return types.ErrInvalidArguments
	}
	// 与 validateDatabaseHost 对齐: 拒绝反斜杠/空白(连接串畸形面——
	// SplitHostPort 只按最后一个冒号切,"evil host:3306" 会解析出带空格的
	// host 且不报错)。
	if strings.ContainsAny(host, " \\") {
		return types.ErrInvalidArguments
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast()) {
		return types.ErrInvalidArguments
	}
	// 端口必须为 1..65535 的纯数字(旧实现只交 SplitHostPort——
	// "host:abc"/"host:0"/"host:99999" 均通过, 晚到 dial 才失败)。
	if len(port) > 5 {
		return types.ErrInvalidArguments
	}
	var portNum uint32
	for _, r := range port {
		if r < '0' || r > '9' {
			return types.ErrInvalidArguments
		}
		portNum = portNum*10 + uint32(r-'0')
	}
	if portNum == 0 || portNum > 65535 {
		return types.ErrInvalidArguments
	}
	return nil
}

func validateName(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 255 {
		return types.ErrInvalidArguments
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return types.ErrInvalidArguments
		}
	}
	return nil
}

func databaseError(act string, err error) types.CommandOutput {
	return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, err)}
}

func databaseSnapshotJSON(value any) ([]byte, error) { return json.MarshalIndent(value, "", "  ") }

// SQL configuration shared by MySQL and PostgreSQL.
type SQLDatabaseConfig struct {
	Host            string          `json:"host"`
	Port            uint16          `json:"port"`
	Database        string          `json:"database"`
	Username        string          `json:"username"`
	TLSMode         DatabaseTLSMode `json:"tlsMode"`
	ConnectTimeout  time.Duration   `json:"connectTimeout"`
	MaxOpenConns    int             `json:"maxOpenConns"`
	MaxIdleConns    int             `json:"maxIdleConns"`
	ConnMaxLifetime time.Duration   `json:"connMaxLifetime"`
}

func validateSQLConfig(config SQLDatabaseConfig) error {
	if err := validateDatabaseHost(config.Host, config.Port); err != nil {
		return err
	}
	if validateName(config.Database) != nil || validateName(config.Username) != nil || !validTLS(config.TLSMode) {
		return types.ErrInvalidArguments
	}
	if config.ConnectTimeout < 0 || config.MaxOpenConns < 0 || config.MaxIdleConns < 0 || config.MaxIdleConns > config.MaxOpenConns || config.ConnMaxLifetime < 0 {
		return types.ErrInvalidArguments
	}
	return nil
}

// SQLite configuration contains no secret.
type SQLiteConfig struct {
	Path         string        `json:"path"`
	Mode         string        `json:"mode"`
	BusyTimeout  time.Duration `json:"busyTimeout"`
	WAL          bool          `json:"wal"`
	ForeignKeys  bool          `json:"foreignKeys"`
	MaxOpenConns int           `json:"maxOpenConns"`
}

func validateSQLiteConfig(config SQLiteConfig) error {
	if strings.ContainsRune(config.Path, '\x00') || strings.TrimSpace(config.Path) == "" || config.BusyTimeout < 0 || config.MaxOpenConns < 0 {
		return types.ErrInvalidArguments
	}
	if config.Mode != "ro" && config.Mode != "rw" && config.Mode != "rwc" && config.Mode != "memory" {
		return types.ErrInvalidArguments
	}
	if config.Mode == "memory" {
		if config.Path != ":memory:" {
			return types.ErrInvalidArguments
		}
	} else {
		clean := filepath.Clean(config.Path)
		if clean == "." {
			return types.ErrInvalidArguments
		}
		// 拒绝 ".." 段: ".."/"../x.db"/"a/../../x.db" 会读写配置目录之外
		// 的任意 SQLite 文件(rwc 模式可覆写其他应用数据库)——穿越面。
		// 绝对路径保留(用户配置目标, 与 Redis/Mongo host 同原则)。
		rest := strings.TrimPrefix(clean, filepath.VolumeName(clean))
		for _, seg := range strings.Split(rest, string(filepath.Separator)) {
			if seg == ".." {
				return types.ErrInvalidArguments
			}
		}
	}
	return nil
}

type RedisConfig struct {
	Addresses    []string      `json:"addresses"`
	Username     string        `json:"username,omitempty"`
	DB           int           `json:"db"`
	TLS          bool          `json:"tls"`
	DialTimeout  time.Duration `json:"dialTimeout"`
	ReadTimeout  time.Duration `json:"readTimeout"`
	WriteTimeout time.Duration `json:"writeTimeout"`
	PoolSize     int           `json:"poolSize"`
}

func cloneRedisConfig(config RedisConfig) RedisConfig {
	config.Addresses = append([]string(nil), config.Addresses...)
	return config
}

func validateRedisConfig(config RedisConfig) error {
	if len(config.Addresses) == 0 || len(config.Addresses) > 64 || config.DB < 0 || config.DialTimeout < 0 || config.ReadTimeout < 0 || config.WriteTimeout < 0 || config.PoolSize < 0 {
		return types.ErrInvalidArguments
	}
	// Username 与 SQL 侧一致强制字符集/长度(旧实现零校验——CRLF/@/冒号
	// 可改写 URI authority 或注入协议字节)。
	if config.Username != "" && validateName(config.Username) != nil {
		return types.ErrInvalidArguments
	}
	for _, address := range config.Addresses {
		if validateAddress(address) != nil {
			return types.ErrInvalidArguments
		}
	}
	return nil
}

type MongoDBConfig struct {
	Hosts                  []string      `json:"hosts"`
	Database               string        `json:"database"`
	Username               string        `json:"username,omitempty"`
	AuthSource             string        `json:"authSource,omitempty"`
	ReplicaSet             string        `json:"replicaSet,omitempty"`
	TLS                    bool          `json:"tls"`
	Direct                 bool          `json:"direct"`
	ConnectTimeout         time.Duration `json:"connectTimeout"`
	ServerSelectionTimeout time.Duration `json:"serverSelectionTimeout"`
}

func cloneMongoDBConfig(config MongoDBConfig) MongoDBConfig {
	config.Hosts = append([]string(nil), config.Hosts...)
	return config
}

func validateMongoDBConfig(config MongoDBConfig) error {
	if len(config.Hosts) == 0 || len(config.Hosts) > 64 || validateName(config.Database) != nil || config.ConnectTimeout < 0 || config.ServerSelectionTimeout < 0 {
		return types.ErrInvalidArguments
	}
	// Username/AuthSource/ReplicaSet 非空时强制 validateName(同 Redis 修复)。
	if config.Username != "" && validateName(config.Username) != nil {
		return types.ErrInvalidArguments
	}
	if config.AuthSource != "" && validateName(config.AuthSource) != nil {
		return types.ErrInvalidArguments
	}
	if config.ReplicaSet != "" && validateName(config.ReplicaSet) != nil {
		return types.ErrInvalidArguments
	}
	for _, host := range config.Hosts {
		if validateAddress(host) != nil {
			return types.ErrInvalidArguments
		}
	}
	return nil
}

type InfluxDBConfig struct {
	Endpoint string        `json:"endpoint"`
	Org      string        `json:"org"`
	Bucket   string        `json:"bucket"`
	Timeout  time.Duration `json:"timeout"`
}

func validateInfluxEndpoint(endpoint string) error {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.User != nil || parsed.Fragment != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return types.ErrInvalidArguments
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" {
		return types.ErrInvalidArguments
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsUnspecified() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsMulticast()) {
		return types.ErrInvalidArguments
	}
	return nil
}

func validateInfluxDBConfig(config InfluxDBConfig) error {
	if err := validateInfluxEndpoint(config.Endpoint); err != nil {
		return err
	}
	parsed, err := url.Parse(strings.TrimSpace(config.Endpoint))
	if err != nil || parsed.User != nil || parsed.Fragment != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return types.ErrInvalidArguments
	}
	if ip := net.ParseIP(parsed.Hostname()); ip != nil && (ip.IsLoopback() || ip.IsUnspecified() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsMulticast()) {
		return types.ErrInvalidArguments
	}
	if validateName(config.Org) != nil || validateName(config.Bucket) != nil || config.Timeout < 0 {
		return types.ErrInvalidArguments
	}
	return nil
}
