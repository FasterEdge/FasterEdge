package data

import (
	"encoding/json"
	"github.com/FasterEdge/FasterEdge/types"
	"strings"
	"testing"
	"time"
)

// generic test harness for all database components
func TestDatabaseComponents(t *testing.T) {
	// define each component test case
	cases := []struct {
		name        string
		ctor        func() types.Component
		configure   any
		secretArgs  any // nil if no secret
		validSecret bool
	}{
		{
			name:        "MySQLData",
			ctor:        func() types.Component { return NewMySQLData() },
			configure:   SQLDatabaseConfigureArgs{Config: SQLDatabaseConfig{Host: "10.0.0.1", Port: 3306, Database: "testdb", Username: "user", TLSMode: DatabaseTLSDisable, ConnectTimeout: time.Second, MaxOpenConns: 10, MaxIdleConns: 5, ConnMaxLifetime: time.Minute}},
			secretArgs:  DatabaseSetSecretArgs{Secret: []byte("supersecret")},
			validSecret: true,
		},
		{
			name:        "PostgreSQLData",
			ctor:        func() types.Component { return NewPostgreSQLData() },
			configure:   SQLDatabaseConfigureArgs{Config: SQLDatabaseConfig{Host: "10.0.0.2", Port: 5432, Database: "pgdb", Username: "pguser", TLSMode: DatabaseTLSPrefer, ConnectTimeout: time.Second, MaxOpenConns: 20, MaxIdleConns: 10, ConnMaxLifetime: 2 * time.Minute}},
			secretArgs:  DatabaseSetSecretArgs{Secret: []byte("pgsecret")},
			validSecret: true,
		},
		{
			name:        "SQLiteData",
			ctor:        func() types.Component { return NewSQLiteData() },
			configure:   SQLiteConfigureArgs{Config: SQLiteConfig{Path: "test.db", Mode: "rw", BusyTimeout: time.Second, WAL: true, ForeignKeys: true, MaxOpenConns: 5}},
			secretArgs:  nil,
			validSecret: false,
		},
		{
			name:        "RedisData",
			ctor:        func() types.Component { return NewRedisData() },
			configure:   RedisConfigureArgs{Config: RedisConfig{Addresses: []string{"10.0.0.3:6379"}, DB: 0, TLS: false, DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second, PoolSize: 10}},
			secretArgs:  DatabaseSetSecretArgs{Secret: []byte("redisPwd")},
			validSecret: true,
		},
		{
			name:        "MongoDBData",
			ctor:        func() types.Component { return NewMongoDBData() },
			configure:   MongoDBConfigureArgs{Config: MongoDBConfig{Hosts: []string{"10.0.0.4:27017"}, Database: "mdb", AuthSource: "admin", TLS: false, ConnectTimeout: time.Second, ServerSelectionTimeout: time.Second}},
			secretArgs:  DatabaseSetSecretArgs{Secret: []byte("mongoPwd")},
			validSecret: true,
		},
		{
			name:        "InfluxDBData",
			ctor:        func() types.Component { return NewInfluxDBData() },
			configure:   InfluxDBConfigureArgs{Config: InfluxDBConfig{Endpoint: "https://influx.example.com:8086", Org: "myorg", Bucket: "mybucket", Timeout: time.Second}},
			secretArgs:  DatabaseSetSecretArgs{Secret: []byte("influxToken")},
			validSecret: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			comp := tc.ctor()
			// configure
			out := comp.Command(nil, DatabaseCommandConfigure, tc.configure)
			if out.Err != nil {
				t.Fatalf("configure error: %v", out.Err)
			}
			// set secret if applicable
			if tc.validSecret {
				out = comp.Command(nil, DatabaseCommandSetSecret, tc.secretArgs)
				if out.Err != nil {
					t.Fatalf("set secret error: %v", out.Err)
				}
			}
			// get config should not include secret(旧实现只查 Err——未来给配置结构体
			// 加 Secret 字段而忘脱敏时测试照常通过)
			out = comp.Command(nil, DatabaseCommandGetConfig, nil)
			if out.Err != nil {
				t.Fatalf("get config error: %v", out.Err)
			}
			if tc.validSecret {
				cfgJSON, jerr := json.Marshal(out.Value)
				if jerr != nil {
					t.Fatalf("marshal config: %v", jerr)
				}
				rawSecret := tc.secretArgs.(DatabaseSetSecretArgs).Secret
				if strings.Contains(string(cfgJSON), string(rawSecret)) {
					t.Fatalf("get_config leaked secret value: %s", string(cfgJSON))
				}
			}
			// status check
			out = comp.Command(nil, DatabaseCommandStatus, nil)
			if out.Err != nil {
				t.Fatalf("status error: %v", out.Err)
			}
			// snapshot deep copy test: 旧实现"重配置后快照不变"结构性恒真
			// (快照是值拷贝, 重配置走整体替换, 早先的快照副本不可能看到新值,
			// 且哨兵串 "changed" 从未出现在被改字段里)——深拷贝从未被验证。
			// 改为别名攻击: 改写快照返回结构内的 slice 元素, 组件内部状态
			// 必须不受影响。
			outSnap := comp.Command(nil, DatabaseCommandSnapshot, nil)
			if outSnap.Err != nil {
				t.Fatalf("snapshot error: %v", outSnap.Err)
			}
			switch snap := outSnap.Value.(type) {
			case RedisSnapshot:
				if len(snap.Config.Addresses) > 0 {
					snap.Config.Addresses[0] = "192.0.2.99:1"
				}
			case MongoDBSnapshot:
				if len(snap.Config.Hosts) > 0 {
					snap.Config.Hosts[0] = "192.0.2.99:1"
				}
			}
			outGet := comp.Command(nil, DatabaseCommandGetConfig, nil)
			if outGet.Err != nil {
				t.Fatalf("get config after snapshot mutation: %v", outGet.Err)
			}
			switch cfg := outGet.Value.(type) {
			case RedisConfig:
				for _, a := range cfg.Addresses {
					if a == "192.0.2.99:1" {
						t.Fatalf("snapshot mutation leaked into component state")
					}
				}
			case MongoDBConfig:
				for _, h := range cfg.Hosts {
					if h == "192.0.2.99:1" {
						t.Fatalf("snapshot mutation leaked into component state")
					}
				}
			}
			// JSONMarshal must not contain secret
			if tc.validSecret {
				marshaler, ok := comp.(interface{ JSONMarshal() ([]byte, error) })
				if !ok {
					t.Fatal("component does not implement JSONMarshal")
				}
				b, err := marshaler.JSONMarshal()
				if err != nil {
					t.Fatalf("JSONMarshal error: %v", err)
				}
				rawSecret := tc.secretArgs.(DatabaseSetSecretArgs).Secret
				if strings.Contains(string(b), string(rawSecret)) {
					t.Fatalf("JSONMarshal leaked secret value: %s", string(b))
				}
			}
		})
	}
}
