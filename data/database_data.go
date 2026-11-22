package data

import (
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
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

// databaseAddressPolicy 是数据库类 Data 的出网地址策略等级: AllowPrivate
// —— 内网数据库(edge 场景的 MySQL/Redis/Mongo 多在 10.x/172.x/192.168.x)
// 是合法拓扑地址, 私网/CGNAT/文档保留段一并放行; 回环、未指定、组播、
// 链路本地(含云元数据 169.254.169.254)与十进制歧义写法(2130706433、127.1、
// 0x7f.0.0.1)仍一律拒绝。判定真理在 types/address_policy.go, 这里不再另写
// 一套 net.ParseIP 比较。
var databaseAddressPolicy = types.NewAddressGuard(types.AddressPolicyAllowPrivate)

// validateDatabaseHost 校验 SQL host + port。语法层(空串/畸形字符/端口 0)沿用
// 原有纪律, 地址语义统一委托 databaseAddressPolicy.CheckHost —— 旧实现只拒
// 回环/未指定/组播, 私网、链路本地(169.254.169.254)与 localhost 别名全部放行,
// 与 README 声称的"默认拒绝 localhost/127.0.0.1"不符。
func validateDatabaseHost(host string, port uint16) error {
	host = strings.TrimSpace(host)
	if host == "" || port == 0 {
		return types.ErrInvalidArguments
	}
	// host 形如 "host" / "host:port" 两种都不接受; 端口字段独立由 port 承载,
	// 因此含 "/"、"?"、"#"、反斜杠、空白的写法视为畸形。
	if strings.ContainsAny(host, " /\\?#@") {
		return types.ErrInvalidArguments
	}
	// 端口由 Port 字段独立承载, Host 里再带一段 ":port" 会拼出畸形 DSN。
	// 例外是 IPv6 字面量(本身含冒号), 因此只在"解析不成 IP 却含冒号"时拒绝。
	if strings.Contains(host, ":") {
		if _, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")); err != nil {
			return types.ErrInvalidArguments
		}
	}
	return wrapDatabaseAddress(host)
}

// validateAddress 校验 "host:port" 形式的节点地址(Redis / MongoDB)。
// 端口纪律(纯数字 1..65535)保持原有实现, 地址语义委托
// databaseAddressPolicy.CheckHost —— 旧实现只拒回环/未指定/组播。
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
	return wrapDatabaseAddress(host)
}

// wrapDatabaseAddress 把统一地址策略的拒绝转成 data 包统一的
// types.ErrInvalidArguments —— data 侧对外只暴露 ErrInvalidArguments 哨兵,
// 不把 ErrAddressDisallowed 泄漏给调用方(保持既有调用点的判定方式不变)。
func wrapDatabaseAddress(host string) error {
	if err := databaseAddressPolicy.CheckHost(host); err != nil {
		return fmt.Errorf("host %q: %w: %w", host, types.ErrInvalidArguments, err)
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

// influxEndpointPolicy 是 InfluxDB 端点的策略等级(PublicOnly): 与 ability 侧
// influxEndpointPolicy / ekuiperEndpointPolicy / dockerEndpointPolicy 同级
// (远端观测库, 私网一并拒绝)。域名在拨号层由 transport 经
// types.AddressGuard.DialContext 对实际 IP 二次校验(存储期不解析, 避免
// TOCTOU 与阻塞)。
var influxEndpointPolicy = types.NewAddressGuard(types.AddressPolicyPublicOnly)

func validateInfluxEndpoint(endpoint string) error {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.User != nil || parsed.Fragment != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return types.ErrInvalidArguments
	}
	// 地址语义统一委托 influxEndpointPolicy(IPv4-mapped、userinfo 已在 URL
	// 层被拒、尾点 FQDN、localhost 别名、十进制歧义写法、云元数据 169.254.169.254
	// 一并覆盖)。旧实现手写"localhost + net.ParseIP 五连比较", 私网放行、
	// 十进制写法放行, 与统一策略结论不一致。
	if err := influxEndpointPolicy.CheckHost(parsed.Hostname()); err != nil {
		return fmt.Errorf("endpoint host %q: %w: %w", parsed.Hostname(), types.ErrInvalidArguments, err)
	}
	return nil
}

func validateInfluxDBConfig(config InfluxDBConfig) error {
	if err := validateInfluxEndpoint(config.Endpoint); err != nil {
		return err
	}
	if validateName(config.Org) != nil || validateName(config.Bucket) != nil || config.Timeout < 0 {
		return types.ErrInvalidArguments
	}
	return nil
}
