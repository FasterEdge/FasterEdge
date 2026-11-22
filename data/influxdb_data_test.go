package data

import (
	"sync"
	"testing"
)

// TestInfluxDBConcurrentSetterAndCommand: 第九轮修复前 Command(configure)
// 不持 d.mu——与 Setter 的 public()→configure() RMW 区间并发时整块覆盖,
// 先落盘字段被后落盘整份替换(文件头注释声称修复的"互相覆盖"在混用路径
// 仍存在)。修复后 Command 与 Setter 共用 mu。行为回归: 并发混用后 config
// 必须是完整有效状态(每次 configure 整块覆盖 + 每次 SetEndpoint 只改
// Endpoint——无论交错如何, Org/Bucket 保持某次 configure 的完整非零值)。
func TestInfluxDBConcurrentSetterAndCommand(t *testing.T) {
	d := NewInfluxDBData()
	// 公网域名作为基线端点: 统一地址策略(influxEndpointPolicy, PublicOnly)
	// 对 Influx 端点的要求是"仅公网可路由地址"——203.0.113.x 这类文档保留段
	// 已归入 PublicOnly 拒绝集合, 旧实现手写判定只拒回环/私网/组播时才放行。
	base := InfluxDBConfig{Endpoint: "http://influx.example.com:8086", Org: "org0", Bucket: "bucket0"}
	if out := d.Command(nil, DatabaseCommandConfigure, InfluxDBConfigureArgs{Config: base}); out.Err != nil {
		t.Fatal(out.Err)
	}
	const workers = 8
	var wg sync.WaitGroup
	wg.Add(workers + 1)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = d.SetEndpoint("http://influx" + intString(j%200+1) + ".example.com:8086")
			}
		}()
	}
	go func() {
		defer wg.Done()
		for j := 0; j < 100; j++ {
			cfg := InfluxDBConfig{
				Endpoint: "http://influx250.example.com:8086",
				Org:      "org" + intString(j%10),
				Bucket:   "bucket" + intString(j%10),
			}
			_ = d.Command(nil, DatabaseCommandConfigure, InfluxDBConfigureArgs{Config: cfg})
		}
	}()
	wg.Wait()

	cfg := d.PublicConfig()
	if cfg.Org == "" || cfg.Bucket == "" {
		t.Fatalf("torn config after concurrent setter/configure: %+v", cfg)
	}
	if cfg.Endpoint == "" {
		t.Fatalf("endpoint lost: %+v", cfg)
	}
}
