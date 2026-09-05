package FasterEdge

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FasterEdge/FasterEdge/ability"
	"github.com/FasterEdge/FasterEdge/data"
	"github.com/FasterEdge/FasterEdge/types"
)

func mustDataCommand(t *testing.T, component types.Data, atom *types.Atom, command string, args any) types.CommandOutput {
	t.Helper()
	out := component.Command(atom, command, args)
	if out.Err != nil {
		t.Fatalf("%s %s: %v", component.GetName(), command, out.Err)
	}
	return out
}

func TestCombinationAllDatabaseDataLifecycle(t *testing.T) {
	atom := InitStandardAtom()
	if err := atom.PreRun(); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name      string
		configure any
		secret    string
	}{
		{"MySQLData", data.SQLDatabaseConfigureArgs{Config: data.SQLDatabaseConfig{Host: "10.10.0.1", Port: 3306, Database: "edge", Username: "node", TLSMode: data.DatabaseTLSRequire, MaxOpenConns: 8, MaxIdleConns: 4}}, "mysql-combination-secret"},
		{"PostgreSQLData", data.SQLDatabaseConfigureArgs{Config: data.SQLDatabaseConfig{Host: "10.10.0.2", Port: 5432, Database: "edge", Username: "node", TLSMode: data.DatabaseTLSVerifyAll, MaxOpenConns: 8, MaxIdleConns: 4}}, "postgres-combination-secret"},
		{"SQLiteData", data.SQLiteConfigureArgs{Config: data.SQLiteConfig{Path: "edge-combination.db", Mode: "rwc", BusyTimeout: time.Second, WAL: true, ForeignKeys: true, MaxOpenConns: 1}}, ""},
		{"RedisData", data.RedisConfigureArgs{Config: data.RedisConfig{Addresses: []string{"10.10.0.3:6379", "10.10.0.4:6379"}, DB: 3, TLS: true, PoolSize: 8}}, "redis-combination-secret"},
		{"MongoDBData", data.MongoDBConfigureArgs{Config: data.MongoDBConfig{Hosts: []string{"10.10.0.5:27017", "10.10.0.6:27017"}, Database: "edge", Username: "node", AuthSource: "admin", ReplicaSet: "rs0", TLS: true}}, "mongo-combination-secret"},
		{"InfluxDBData", data.InfluxDBConfigureArgs{Config: data.InfluxDBConfig{Endpoint: "https://influx.example.com:8086", Org: "edge", Bucket: "telemetry", Timeout: 3 * time.Second}}, "influx-combination-token"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			component, ok := atom.Data(tc.name)
			if !ok {
				t.Fatalf("missing %s", tc.name)
			}
			configured := mustDataCommand(t, component, atom, data.DatabaseCommandConfigure, tc.configure).Value.(data.DatabaseStatus)
			if !configured.Configured || configured.Revision != 1 {
				t.Fatalf("configured status = %+v", configured)
			}
			if tc.secret != "" {
				status := mustDataCommand(t, component, atom, data.DatabaseCommandSetSecret, data.DatabaseSetSecretArgs{Secret: []byte(tc.secret)}).Value.(data.DatabaseStatus)
				if !status.SecretConfigured || status.Revision != 2 {
					t.Fatalf("secret status = %+v", status)
				}
			}
			// get_config 字段级往返断言(旧实现只查 Configured/Revision——
			// configure 存错字段时测试照常通过)
			gotCfg := mustDataCommand(t, component, atom, data.DatabaseCommandGetConfig, nil).Value
			switch cfg := gotCfg.(type) {
			case data.SQLDatabaseConfig:
				want := tc.configure.(data.SQLDatabaseConfigureArgs).Config
				if cfg.Host != want.Host || cfg.Database != want.Database || cfg.Username != want.Username || cfg.MaxOpenConns != want.MaxOpenConns {
					t.Fatalf("SQL config round-trip mismatch: got %+v want %+v", cfg, want)
				}
			case data.SQLiteConfig:
				want := tc.configure.(data.SQLiteConfigureArgs).Config
				if cfg.Path != want.Path || cfg.Mode != want.Mode || cfg.WAL != want.WAL {
					t.Fatalf("SQLite config round-trip mismatch: got %+v want %+v", cfg, want)
				}
			case data.RedisConfig:
				want := tc.configure.(data.RedisConfigureArgs).Config
				if !reflect.DeepEqual(cfg.Addresses, want.Addresses) || cfg.DB != want.DB {
					t.Fatalf("Redis config round-trip mismatch: got %+v want %+v", cfg, want)
				}
			case data.MongoDBConfig:
				want := tc.configure.(data.MongoDBConfigureArgs).Config
				if !reflect.DeepEqual(cfg.Hosts, want.Hosts) || cfg.Database != want.Database || cfg.AuthSource != want.AuthSource {
					t.Fatalf("Mongo config round-trip mismatch: got %+v want %+v", cfg, want)
				}
			case data.InfluxDBConfig:
				want := tc.configure.(data.InfluxDBConfigureArgs).Config
				if cfg.Endpoint != want.Endpoint || cfg.Org != want.Org || cfg.Bucket != want.Bucket {
					t.Fatalf("Influx config round-trip mismatch: got %+v want %+v", cfg, want)
				}
			default:
				t.Fatalf("unexpected get_config type %T", gotCfg)
			}
			// JSONMarshal 泄露检查覆盖全部含 secret 的组件(旧实现只查
			// Redis/Influx 两处, 其余四组件带 secret 场景零覆盖)
			if tc.secret != "" {
				marshaler, ok := component.(interface{ JSONMarshal() ([]byte, error) })
				if !ok {
					t.Fatal("component does not implement JSONMarshal")
				}
				jb, jerr := marshaler.JSONMarshal()
				if jerr != nil {
					t.Fatalf("JSONMarshal: %v", jerr)
				}
				if strings.Contains(string(jb), tc.secret) {
					t.Fatalf("JSONMarshal leaked secret: %s", jb)
				}
			}
			snapshot := mustDataCommand(t, component, atom, data.DatabaseCommandSnapshot, nil).Value
			encoded, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if tc.secret != "" && strings.Contains(string(encoded), tc.secret) {
				t.Fatalf("snapshot leaked secret: %s", encoded)
			}
		})
	}
}

func TestCombinationDatabaseInvalidUpdateIsAtomic(t *testing.T) {
	mysql := data.NewMySQLData()
	valid := data.SQLDatabaseConfigureArgs{Config: data.SQLDatabaseConfig{Host: "10.20.0.1", Port: 3306, Database: "stable", Username: "node", TLSMode: data.DatabaseTLSRequire, MaxOpenConns: 4, MaxIdleConns: 2}}
	mustDataCommand(t, mysql, nil, data.DatabaseCommandConfigure, valid)

	invalid := valid
	invalid.Config.Database = "has space"
	if out := mysql.Command(nil, data.DatabaseCommandConfigure, invalid); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("invalid update error = %v", out.Err)
	}
	got := mustDataCommand(t, mysql, nil, data.DatabaseCommandGetConfig, nil).Value.(data.SQLDatabaseConfig)
	status := mustDataCommand(t, mysql, nil, data.DatabaseCommandStatus, nil).Value.(data.DatabaseStatus)
	if got.Database != "stable" || status.Revision != 1 {
		t.Fatalf("invalid update mutated state: config=%+v status=%+v", got, status)
	}
}

func TestCombinationDatabaseSliceIsolation(t *testing.T) {
	redis := data.NewRedisData()
	addresses := []string{"10.30.0.1:6379", "10.30.0.2:6379"}
	mustDataCommand(t, redis, nil, data.DatabaseCommandConfigure, data.RedisConfigureArgs{Config: data.RedisConfig{Addresses: addresses, DB: 1, PoolSize: 2}})
	addresses[0] = "10.99.0.1:6379"
	first := mustDataCommand(t, redis, nil, data.DatabaseCommandGetConfig, nil).Value.(data.RedisConfig)
	first.Addresses[0] = "10.88.0.1:6379"
	second := mustDataCommand(t, redis, nil, data.DatabaseCommandGetConfig, nil).Value.(data.RedisConfig)
	if second.Addresses[0] != "10.30.0.1:6379" {
		t.Fatalf("Redis config alias leaked: %v", second.Addresses)
	}

	mongo := data.NewMongoDBData()
	hosts := []string{"10.31.0.1:27017", "10.31.0.2:27017"}
	mustDataCommand(t, mongo, nil, data.DatabaseCommandConfigure, data.MongoDBConfigureArgs{Config: data.MongoDBConfig{Hosts: hosts, Database: "edge"}})
	hosts[0] = "10.99.0.1:27017"
	mongoFirst := mustDataCommand(t, mongo, nil, data.DatabaseCommandGetConfig, nil).Value.(data.MongoDBConfig)
	mongoFirst.Hosts[0] = "10.88.0.1:27017"
	mongoSecond := mustDataCommand(t, mongo, nil, data.DatabaseCommandGetConfig, nil).Value.(data.MongoDBConfig)
	if mongoSecond.Hosts[0] != "10.31.0.1:27017" {
		t.Fatalf("MongoDB config alias leaked: %v", mongoSecond.Hosts)
	}
}

func TestCombinationDatabaseConcurrentRevisionAndRedaction(t *testing.T) {
	redis := data.NewRedisData()
	mustDataCommand(t, redis, nil, data.DatabaseCommandConfigure, data.RedisConfigureArgs{Config: data.RedisConfig{Addresses: []string{"10.40.0.1:6379"}, PoolSize: 4}})

	const writers = 8
	const rotations = 40
	var wg sync.WaitGroup
	for worker := 0; worker < writers; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < rotations; n++ {
				secret := []byte(fmt.Sprintf("redis-%02d-%03d-secret", worker, n))
				if out := redis.Command(nil, data.DatabaseCommandSetSecret, data.DatabaseSetSecretArgs{Secret: secret}); out.Err != nil {
					t.Errorf("set secret: %v", out.Err)
					return
				}
				if out := redis.Command(nil, data.DatabaseCommandGetConfig, nil); out.Err != nil {
					t.Errorf("get config: %v", out.Err)
					return
				}
			}
		}()
	}
	wg.Wait()
	status := mustDataCommand(t, redis, nil, data.DatabaseCommandStatus, nil).Value.(data.DatabaseStatus)
	if want := uint64(1 + writers*rotations); status.Revision != want {
		t.Fatalf("revision=%d want=%d", status.Revision, want)
	}
	encoded, err := redis.JSONMarshal()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "redis-") {
		t.Fatalf("JSON leaked rotated secret: %s", encoded)
	}
}

type combinationInfluxTransport struct {
	mu      sync.Mutex
	pinged  int
	written []ability.InfluxPoint
	queries []string
	rows    []map[string]any
}

func (f *combinationInfluxTransport) Ping() error {
	f.mu.Lock()
	f.pinged++
	f.mu.Unlock()
	return nil
}
func (f *combinationInfluxTransport) Write(points []ability.InfluxPoint) error {
	f.mu.Lock()
	f.written = append(f.written, points...)
	f.mu.Unlock()
	return nil
}
func (f *combinationInfluxTransport) Query(query string) ([]map[string]any, error) {
	f.mu.Lock()
	f.queries = append(f.queries, query)
	rows := append([]map[string]any(nil), f.rows...)
	f.mu.Unlock()
	return rows, nil
}
func (f *combinationInfluxTransport) snapshot() (int, []ability.InfluxPoint, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pinged, append([]ability.InfluxPoint(nil), f.written...), append([]string(nil), f.queries...)
}

func TestCombinationInfluxDataAbilityWorkflow(t *testing.T) {
	atom := InitStandardAtom()
	influx := ability.NewInfluxAbility()
	transport := &combinationInfluxTransport{rows: []map[string]any{{"_measurement": "cpu", "_value": 42.5}}}
	influx.SetTransport(transport)
	if err := atom.AddAbility(influx); err != nil {
		t.Fatal(err)
	}
	if err := atom.PreRun(); err != nil {
		t.Fatal(err)
	}

	// Legacy Ability configuration commands must write into InfluxDBData.
	mustCommand(t, influx, atom, ability.InfluxCommandSetEndpoint, ability.InfluxConfigArgs{Value: "https://influx.example.com:8086"})
	mustCommand(t, influx, atom, ability.InfluxCommandSetOrg, ability.InfluxConfigArgs{Value: "edge"})
	mustCommand(t, influx, atom, ability.InfluxCommandSetBucket, ability.InfluxConfigArgs{Value: "telemetry"})
	mustCommand(t, influx, atom, ability.InfluxCommandSetToken, ability.InfluxConfigArgs{Value: "first-influx-token"})

	influxDataComponent, _ := atom.Data("InfluxDBData")
	influxData := influxDataComponent.(*data.InfluxDBData)
	config := influxData.PublicConfig()
	if config.Endpoint != "https://influx.example.com:8086" || config.Org != "edge" || config.Bucket != "telemetry" {
		t.Fatalf("Data did not receive Ability config: %+v", config)
	}
	material, err := influxData.ConnectionMaterial()
	if err != nil {
		t.Fatal(err)
	}
	if string(material.Token) != "first-influx-token" {
		t.Fatal("Data did not receive Ability token")
	}
	legacyConfig := mustCommand(t, influx, atom, ability.InfluxCommandGetConfig, nil).Value.(ability.InfluxConfig)
	if legacyConfig.Token != "" {
		t.Fatal("Ability get_config leaked token")
	}

	mustCommand(t, influx, atom, ability.InfluxCommandPing, nil)
	points := []ability.InfluxPoint{
		{Measurement: "cpu", Tags: map[string]string{"node": "edge-1"}, Fields: map[string]any{"usage": 42.5}, Time: time.Now()},
		{Measurement: "memory", Tags: map[string]string{"node": "edge-1"}, Fields: map[string]any{"used": 1024}},
	}
	if count := mustCommand(t, influx, atom, ability.InfluxCommandWrite, ability.InfluxWriteArgs{Points: points}).Value.(int); count != 2 {
		t.Fatalf("written count=%d", count)
	}
	rows := mustCommand(t, influx, atom, ability.InfluxCommandQuery, ability.InfluxQueryArgs{Query: `from(bucket: "telemetry")`}).Value.([]map[string]any)
	if len(rows) != 1 {
		t.Fatalf("rows=%v", rows)
	}
	series := mustCommand(t, influx, atom, ability.InfluxCommandListSeries, nil).Value.([]string)
	if fmt.Sprint(series) != "[cpu memory]" {
		t.Fatalf("series=%v", series)
	}
	mustCommand(t, influx, atom, ability.InfluxCommandDeleteSeries, ability.InfluxSeriesArgs{Measurement: "cpu"})
	series = mustCommand(t, influx, atom, ability.InfluxCommandListSeries, nil).Value.([]string)
	if fmt.Sprint(series) != "[memory]" {
		t.Fatalf("series after delete=%v", series)
	}
	pinged, written, queries := transport.snapshot()
	if pinged != 1 || len(written) != 2 || len(queries) != 1 {
		t.Fatalf("transport calls ping=%d write=%d query=%d", pinged, len(written), len(queries))
	}
}

func TestCombinationInfluxTokenRotationAndClear(t *testing.T) {
	atom := InitStandardAtom()
	influx := ability.NewInfluxAbility()
	influx.SetTransport(&combinationInfluxTransport{})
	if err := atom.AddAbility(influx); err != nil {
		t.Fatal(err)
	}
	if err := atom.PreRun(); err != nil {
		t.Fatal(err)
	}
	influxDataComponent, _ := atom.Data("InfluxDBData")
	influxData := influxDataComponent.(*data.InfluxDBData)
	mustCommand(t, influx, atom, ability.InfluxCommandSetEndpoint, ability.InfluxConfigArgs{Value: "https://influx.example.com"})
	mustCommand(t, influx, atom, ability.InfluxCommandSetToken, ability.InfluxConfigArgs{Value: "rotation-token-one"})
	first, err := influxData.ConnectionMaterial()
	if err != nil {
		t.Fatal(err)
	}
	mustCommand(t, influx, atom, ability.InfluxCommandSetToken, ability.InfluxConfigArgs{Value: "rotation-token-two"})
	second, err := influxData.ConnectionMaterial()
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision >= second.Revision || string(second.Token) != "rotation-token-two" {
		t.Fatalf("rotation material first=%+v second=%+v", first, second)
	}
	// Returned material owns its token copy; mutating it cannot alter Data.
	second.Token[0] = 'X'
	third, err := influxData.ConnectionMaterial()
	if err != nil {
		t.Fatal(err)
	}
	if string(third.Token) != "rotation-token-two" {
		t.Fatalf("material token alias leaked: %q", third.Token)
	}
	mustDataCommand(t, influxData, atom, data.DatabaseCommandClearSecret, nil)
	cleared, err := influxData.ConnectionMaterial()
	if err != nil {
		t.Fatal(err)
	}
	if len(cleared.Token) != 0 {
		t.Fatalf("cleared token still present: %q", cleared.Token)
	}
	encoded, err := influxData.JSONMarshal()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "rotation-token") {
		t.Fatalf("Influx JSON leaked token: %s", encoded)
	}
}

func TestCombinationOneKeyAuthenticatedDatabaseData(t *testing.T) {
	atom := InitStandardAtom()
	if err := atom.PreRun(); err != nil {
		t.Fatal(err)
	}
	oneKey, _ := atom.Ability("OneKeyAbility")
	issued := mustCommand(t, oneKey, atom, ability.OneKeyCommandIssueToken, ability.OneKeyIssueTokenArgs{Subject: "database-client", TTL: time.Minute}).Value.(ability.OneKeyToken)
	credential := ability.OneKeyCredential{Subject: issued.Subject, IssuedAt: issued.IssuedAt, ExpiresAt: issued.ExpiresAt, Signature: issued.Signature}

	args := data.SQLDatabaseConfigureArgs{Config: data.SQLDatabaseConfig{Host: "10.50.0.1", Port: 3306, Database: "edge", Username: "node", TLSMode: data.DatabaseTLSRequire, MaxOpenConns: 2, MaxIdleConns: 1}}
	if out := atom.AuthenticatedCommand(credential, "MySQLData", data.DatabaseCommandConfigure, args); out.Err != nil {
		t.Fatalf("authenticated configure: %v", out.Err)
	}
	if out := atom.AuthenticatedCommand(credential, "MySQLData", data.DatabaseCommandGetConfig, nil); out.Err != nil {
		t.Fatalf("authenticated get: %v", out.Err)
	} else if out.Value.(data.SQLDatabaseConfig).Database != "edge" {
		t.Fatalf("config = %+v", out.Value)
	}

	bad := credential
	bad.Signature += "bad"
	if out := atom.AuthenticatedCommand(bad, "MySQLData", data.DatabaseCommandGetConfig, nil); !errors.Is(out.Err, types.ErrAuthenticationFailed) {
		t.Fatalf("bad credential error = %v", out.Err)
	}
}
