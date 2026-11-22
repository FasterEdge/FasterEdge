package data

import (
	"errors"
	"testing"

	"github.com/FasterEdge/FasterEdge/types"
)

// TestDatabaseAddressPolicyTable 锁住 data 侧三个地址入口(validateDatabaseHost /
// validateAddress / validateInfluxEndpoint)对同一批手法的结论: 它们现在共用
// types/address_policy.go 的判定真理, 不再各写一套 net.ParseIP 比较。
// 每一行对应任务点名的一类手法, 后四类是统一策略前**全部被放行**的绕过形态。
func TestDatabaseAddressPolicyTable(t *testing.T) {
	sqlCases := []struct {
		host string
		want bool
		note string
	}{
		{"db.example.com", true, "公网/内网域名(需 DNS 解析, 存储期放行)"},
		{"db.example.com.", true, "尾点 FQDN"},
		{"10.0.0.5", true, "RFC1918 私网(edge 场景的库常在私网)"},
		{"192.168.1.9", true, "RFC1918 私网"},
		{"8.8.8.8", true, "公网 IPv4"},
		{"127.0.0.1", false, "回环(策略前即拒绝)"},
		{"::1", false, "IPv6 回环(策略前即拒绝)"},
		{"::ffff:127.0.0.1", false, "IPv4-mapped 回环"},
		{"[::ffff:127.0.0.1]", false, "带方括号的 IPv4-mapped 回环"},
		{"localhost", false, "回环别名"},
		{"LOCALHOST", false, "回环别名(大小写)"},
		{"localhost.", false, "尾点回环别名"},
		{"0.0.0.0", false, "未指定地址"},
		{"::", false, "IPv6 未指定地址"},
		{"169.254.169.254", false, "链路本地(云元数据)"},
		{"fe80::1", false, "IPv6 链路本地"},
		{"224.0.0.1", false, "IPv4 组播"},
		{"ff02::1", false, "IPv6 链路本地组播"},
		{"240.0.0.1", true, "保留段 240/4: AllowPrivate 等级按\"非公网可路由段一并放行\"处理(与 PublicOnly 的 Influx 结论不同)"},
		{"255.255.255.255", false, "受限广播(任何等级都拒)"},
		{"2130706433", false, "十进制整数写法回环"},
		{"127.1", false, "十进制简写回环"},
		{"0x7f.0.0.1", false, "十六进制回环"},
		{"alice@db.example.com", false, "userinfo 手法"},
		{"db.example.com:3306", false, "host 字段含端口(端口由独立字段承载)"},
		{"db example.com", false, "内嵌空白"},
		{"", false, "空 host"},
	}
	for _, tc := range sqlCases {
		err := validateDatabaseHost(tc.host, 3306)
		if tc.want && err != nil {
			t.Errorf("validateDatabaseHost(%q, %s): 期望放行, 实际拒绝: %v", tc.host, tc.note, err)
			continue
		}
		if !tc.want {
			if err == nil {
				t.Errorf("validateDatabaseHost(%q, %s): 期望拒绝, 实际放行", tc.host, tc.note)
				continue
			}
			if !errors.Is(err, types.ErrInvalidArguments) {
				t.Errorf("validateDatabaseHost(%q): 拒绝错误未包装 ErrInvalidArguments: %v", tc.host, err)
			}
		}
	}

	addrCases := []struct {
		addr string
		want bool
		note string
	}{
		{"10.0.0.3:6379", true, "私网节点"},
		{"redis.example.com:6379", true, "域名节点"},
		{"redis.example.com.:6379", true, "尾点 FQDN"},
		{"[2001:4860:4860::8888]:6379", true, "公网 IPv6 节点"},
		{"127.0.0.1:6379", false, "回环"},
		{"[::1]:27017", false, "IPv6 回环"},
		{"[::ffff:10.0.0.1]:27017", true, "IPv4-mapped(Unmap 后按私网判定, AllowPrivate 放行)"},
		{"alice@10.0.0.3:6379", false, "userinfo 手法"},
		{"169.254.169.254:80", false, "链路本地(云元数据)"},
		{"224.0.0.1:6379", false, "组播"},
		{"240.0.0.1:6379", true, "保留段 240/4: AllowPrivate 等级放行(文档保留段同纪律)"},
		{"10.0.0.3:0", false, "端口 0"},
		{"10.0.0.3:70000", false, "端口越界"},
		{"10.0.0.3:abc", false, "端口非数字"},
		{"evil host:3306", false, "host 含空白"},
		{"", false, "空地址"},
	}
	for _, tc := range addrCases {
		err := validateAddress(tc.addr)
		// IPv4-mapped 私网在 AllowPrivate 下是放行的, 表里只是记录该行的期望;
		// 期望值以 want 为准, 不在此处做特判。
		if tc.want && err != nil {
			t.Errorf("validateAddress(%q, %s): 期望放行, 实际拒绝: %v", tc.addr, tc.note, err)
			continue
		}
		if !tc.want {
			if err == nil {
				t.Errorf("validateAddress(%q, %s): 期望拒绝, 实际放行", tc.addr, tc.note)
				continue
			}
			if !errors.Is(err, types.ErrInvalidArguments) {
				t.Errorf("validateAddress(%q): 拒绝错误未包装 ErrInvalidArguments: %v", tc.addr, err)
			}
		}
	}

	influxCases := []struct {
		endpoint string
		want     bool
		note     string
	}{
		{"https://influx.example.com:8086", true, "公网域名"},
		{"http://influx.example.com", true, "公网域名(无端口)"},
		{"https://influx.example.com.:8086", true, "尾点 FQDN"},
		{"https://8.8.8.8:8086", true, "公网 IPv4"},
		{"https://[2001:4860:4860::8888]:8086", true, "公网 IPv6"},
		{"https://127.0.0.1:8086", false, "回环"},
		{"https://[::1]:8086", false, "IPv6 回环"},
		{"https://[::ffff:127.0.0.1]:8086", false, "IPv4-mapped 回环"},
		{"https://localhost:8086", false, "回环别名"},
		{"https://localhost.:8086", false, "尾点回环别名"},
		{"https://10.0.0.5:8086", false, "私网(PublicOnly 等级拒绝)"},
		{"https://192.168.1.5:8086", false, "私网(PublicOnly 等级拒绝)"},
		{"https://169.254.169.254:8086", false, "链路本地(云元数据)"},
		{"https://224.0.0.1:8086", false, "组播"},
		{"https://240.0.0.1:8086", false, "保留段"},
		{"https://2130706433:8086", false, "十进制整数写法回环"},
		{"https://alice@influx.example.com:8086", false, "userinfo 手法"},
		{"https://influx.example.com:8086#frag", false, "fragment"},
		{"ftp://influx.example.com", false, "非 http(s) scheme"},
		{"https://", false, "无 host"},
		{"", false, "空端点"},
	}
	for _, tc := range influxCases {
		err := validateInfluxEndpoint(tc.endpoint)
		if tc.want && err != nil {
			t.Errorf("validateInfluxEndpoint(%q, %s): 期望放行, 实际拒绝: %v", tc.endpoint, tc.note, err)
			continue
		}
		if !tc.want {
			if err == nil {
				t.Errorf("validateInfluxEndpoint(%q, %s): 期望拒绝, 实际放行", tc.endpoint, tc.note)
				continue
			}
			if !errors.Is(err, types.ErrInvalidArguments) {
				t.Errorf("validateInfluxEndpoint(%q): 拒绝错误未包装 ErrInvalidArguments: %v", tc.endpoint, err)
			}
		}
	}
}

// TestDatabaseAddressPolicySharesUnifiedGuard 锁住"data 侧复用统一策略"这一
// 事实本身: databaseAddressPolicy / influxEndpointPolicy 必须与 types 的等级
// 常量一致, 且拒绝错误里能 errors.Is 到 types.ErrAddressDisallowed —— 一旦有人
// 在 data 里重新手写一套 net.ParseIP 比较, 该断言会先失败。
func TestDatabaseAddressPolicySharesUnifiedGuard(t *testing.T) {
	if databaseAddressPolicy.Level != types.AddressPolicyAllowPrivate {
		t.Errorf("databaseAddressPolicy 等级 = %d, want AddressPolicyAllowPrivate(%d)",
			databaseAddressPolicy.Level, types.AddressPolicyAllowPrivate)
	}
	if influxEndpointPolicy.Level != types.AddressPolicyPublicOnly {
		t.Errorf("influxEndpointPolicy 等级 = %d, want AddressPolicyPublicOnly(%d)",
			influxEndpointPolicy.Level, types.AddressPolicyPublicOnly)
	}
	err := validateDatabaseHost("169.254.169.254", 3306)
	if !errors.Is(err, types.ErrAddressDisallowed) {
		t.Fatalf("云元数据拒绝未经过统一策略(缺 ErrAddressDisallowed): %v", err)
	}
	err = validateInfluxEndpoint("https://10.0.0.5:8086")
	if !errors.Is(err, types.ErrAddressDisallowed) {
		t.Fatalf("私网端点拒绝未经过统一策略(缺 ErrAddressDisallowed): %v", err)
	}
}
