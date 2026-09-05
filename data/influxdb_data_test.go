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
	// 203.0.113.x 是文档保留网段(TEST-NET-3), 非回环/私网/组播——通过
	// validateInfluxEndpoint 的私网拒绝校验。
	base := InfluxDBConfig{Endpoint: "http://203.0.113.10:8086", Org: "org0", Bucket: "bucket0"}
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
				_ = d.SetEndpoint("http://203.0.113." + intString(j%200+1) + ":8086")
			}
		}()
	}
	go func() {
		defer wg.Done()
		for j := 0; j < 100; j++ {
			cfg := InfluxDBConfig{
				Endpoint: "http://203.0.113.250:8086",
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
