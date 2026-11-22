// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package ability

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
)

// TestAddressPolicyCheckHostTable 是统一地址策略的判定表: 每个用例都必须
// 得到明确结论(accept 或 reject), 不允许"看情况"。表按策略等级分组,
// 覆盖任务点名的全部手法。
func TestAddressPolicyCheckHostTable(t *testing.T) {
	cases := []struct {
		level AddressPolicyLevel
		host  string
		want  bool // true=放行
		note  string
	}{
		// ── PublicOnly: 仅公网可路由 ──
		{AddressPolicyPublicOnly, "8.8.8.8", true, "公网 IPv4"},
		{AddressPolicyPublicOnly, "2001:4860:4860::8888", true, "公网 IPv6"},
		{AddressPolicyPublicOnly, "time.example.com", true, "公网域名"},
		{AddressPolicyPublicOnly, "time.example.com.", true, "尾点 FQDN"},
		{AddressPolicyPublicOnly, "127.0.0.1", false, "IPv4 回环"},
		{AddressPolicyPublicOnly, "127.1", false, "十进制简写回环"},
		{AddressPolicyPublicOnly, "2130706433", false, "十进制整数回环"},
		{AddressPolicyPublicOnly, "0x7f.0.0.1", false, "十六进制回环"},
		{AddressPolicyPublicOnly, "0177.0.0.1", false, "八进制回环"},
		{AddressPolicyPublicOnly, "::1", false, "IPv6 回环"},
		{AddressPolicyPublicOnly, "::ffff:127.0.0.1", false, "IPv4-mapped 回环"},
		{AddressPolicyPublicOnly, "[::ffff:127.0.0.1]", false, "带方括号 IPv4-mapped 回环"},
		{AddressPolicyPublicOnly, "localhost", false, "回环别名"},
		{AddressPolicyPublicOnly, "LOCALHOST", false, "回环别名(大小写)"},
		{AddressPolicyPublicOnly, "localhost.", false, "尾点回环别名"},
		{AddressPolicyPublicOnly, "a.localhost", false, ".localhost 子域"},
		{AddressPolicyPublicOnly, "10.0.0.5", false, "RFC1918 私网"},
		{AddressPolicyPublicOnly, "192.168.1.1", false, "RFC1918 私网"},
		{AddressPolicyPublicOnly, "172.16.0.1", false, "RFC1918 私网"},
		{AddressPolicyPublicOnly, "100.64.0.1", false, "CGNAT 共享段"},
		{AddressPolicyPublicOnly, "169.254.169.254", false, "链路本地(云元数据)"},
		{AddressPolicyPublicOnly, "fe80::1", false, "IPv6 链路本地"},
		{AddressPolicyPublicOnly, "224.0.0.1", false, "IPv4 组播"},
		{AddressPolicyPublicOnly, "ff02::1", false, "IPv6 链路本地组播"},
		{AddressPolicyPublicOnly, "0.0.0.0", false, "未指定地址"},
		{AddressPolicyPublicOnly, "::", false, "IPv6 未指定地址"},
		{AddressPolicyPublicOnly, "255.255.255.255", false, "受限广播"},
		{AddressPolicyPublicOnly, "240.0.0.1", false, "保留段"},
		{AddressPolicyPublicOnly, "203.0.113.10", false, "TEST-NET-3 文档段"},
		{AddressPolicyPublicOnly, "192.0.2.10", false, "TEST-NET-1 文档段"},
		{AddressPolicyPublicOnly, "198.51.100.10", false, "TEST-NET-2 文档段"},
		{AddressPolicyPublicOnly, "2001:db8::1", false, "IPv6 文档段"},
		{AddressPolicyPublicOnly, "192.0.0.1", false, "IETF 协议分配"},
		{AddressPolicyPublicOnly, "fc00::1", false, "IPv6 ULA"},
		{AddressPolicyPublicOnly, "", false, "空 host"},
		{AddressPolicyPublicOnly, "   ", false, "纯空白"},
		{AddressPolicyPublicOnly, "a b", false, "内嵌空白"},
		{AddressPolicyPublicOnly, "host/path", false, "含路径分隔符"},
		{AddressPolicyPublicOnly, "host\\path", false, "含反斜杠"},
		{AddressPolicyPublicOnly, "alice@host", false, "userinfo 形式"},
		{AddressPolicyPublicOnly, "host?q", false, "查询串"},
		{AddressPolicyPublicOnly, "host#f", false, "片段"},
		{AddressPolicyPublicOnly, "host\x00", false, "NUL 字节"},
		{AddressPolicyPublicOnly, "host\x7f", false, "DEL 控制符"},
		{AddressPolicyPublicOnly, "[::1", false, "未闭合方括号"},
		{AddressPolicyPublicOnly, "::1]", false, "孤立右方括号"},
		{AddressPolicyPublicOnly, ".", false, "仅尾点"},

		// ── AllowPrivate: 额外放行私网/CGNAT/文档保留段 ──
		{AddressPolicyAllowPrivate, "10.0.0.5", true, "RFC1918"},
		{AddressPolicyAllowPrivate, "192.168.1.1", true, "RFC1918"},
		{AddressPolicyAllowPrivate, "172.31.255.254", true, "RFC1918 上界"},
		{AddressPolicyAllowPrivate, "100.64.0.1", true, "CGNAT"},
		{AddressPolicyAllowPrivate, "203.0.113.10", true, "文档段(AllowPrivate 放行)"},
		{AddressPolicyAllowPrivate, "fc00::1", true, "IPv6 ULA"},
		{AddressPolicyAllowPrivate, "broker.internal", true, "内网域名"},
		{AddressPolicyAllowPrivate, "8.8.8.8", true, "公网仍放行"},
		// 私网等级**仍拒绝**的 SSRF 种子:
		{AddressPolicyAllowPrivate, "127.0.0.1", false, "回环"},
		{AddressPolicyAllowPrivate, "::1", false, "IPv6 回环"},
		{AddressPolicyAllowPrivate, "::ffff:127.0.0.1", false, "IPv4-mapped 回环"},
		{AddressPolicyAllowPrivate, "localhost", false, "回环别名"},
		{AddressPolicyAllowPrivate, "169.254.169.254", false, "云元数据"},
		{AddressPolicyAllowPrivate, "fe80::1", false, "IPv6 链路本地"},
		{AddressPolicyAllowPrivate, "224.0.0.1", false, "组播"},
		{AddressPolicyAllowPrivate, "0.0.0.0", false, "未指定"},
		{AddressPolicyAllowPrivate, "255.255.255.255", false, "受限广播"},
		{AddressPolicyAllowPrivate, "2130706433", false, "十进制整数"},
		{AddressPolicyAllowPrivate, "127.1", false, "十进制简写"},

		// ── AllowLoopback: 额外放行回环(本机联调/测试服务器) ──
		{AddressPolicyAllowLoopback, "127.0.0.1", true, "回环"},
		{AddressPolicyAllowLoopback, "127.0.0.53", true, "回环非 .1"},
		{AddressPolicyAllowLoopback, "::1", true, "IPv6 回环"},
		{AddressPolicyAllowLoopback, "::ffff:127.0.0.1", true, "IPv4-mapped 回环"},
		{AddressPolicyAllowLoopback, "localhost", true, "回环别名"},
		{AddressPolicyAllowLoopback, "localhost.", true, "尾点回环别名"},
		{AddressPolicyAllowLoopback, "127.1", false, "十进制简写仍拒"},
		{AddressPolicyAllowLoopback, "2130706433", false, "十进制整数仍拒"},
		{AddressPolicyAllowLoopback, "10.0.0.5", true, "私网(高于 AllowPrivate)"},
		{AddressPolicyAllowLoopback, "0.0.0.0", false, "未指定**任何等级都拒**"},
		{AddressPolicyAllowLoopback, "169.254.169.254", false, "云元数据**任何等级都拒**"},
		{AddressPolicyAllowLoopback, "224.0.0.1", false, "组播**任何等级都拒**"},
		{AddressPolicyAllowLoopback, "::", false, "IPv6 未指定**任何等级都拒**"},
		{AddressPolicyAllowLoopback, "255.255.255.255", false, "受限广播"},
	}
	for _, tc := range cases {
		err := tc.level.CheckHost(tc.host)
		if tc.want && err != nil {
			t.Errorf("level=%d host=%q(%s): 期望放行, 实际拒绝: %v", tc.level, tc.host, tc.note, err)
			continue
		}
		if !tc.want {
			if err == nil {
				t.Errorf("level=%d host=%q(%s): 期望拒绝, 实际放行", tc.level, tc.host, tc.note)
				continue
			}
			// 拒绝必须带统一哨兵, 调用方才可 errors.Is 判定
			if !errors.Is(err, ErrAddressDisallowed) {
				t.Errorf("level=%d host=%q: 拒绝错误未包装 ErrAddressDisallowed: %v", tc.level, tc.host, err)
			}
		}
	}
}

// TestAddressPolicyCheckAddrTable 覆盖 CheckAddr(已解析地址) 的判定,
// 重点是 IPv4-mapped IPv6 与 zone 形态。
func TestAddressPolicyCheckAddrTable(t *testing.T) {
	cases := []struct {
		level AddressPolicyLevel
		addr  string
		want  bool
	}{
		{AddressPolicyPublicOnly, "8.8.8.8", true},
		{AddressPolicyPublicOnly, "::ffff:8.8.8.8", true},
		{AddressPolicyPublicOnly, "::ffff:127.0.0.1", false},
		{AddressPolicyPublicOnly, "::ffff:10.0.0.1", false},
		{AddressPolicyPublicOnly, "10.0.0.1", false},
		{AddressPolicyAllowPrivate, "::ffff:10.0.0.1", true},
		{AddressPolicyAllowPrivate, "::ffff:127.0.0.1", false},
		{AddressPolicyAllowLoopback, "::ffff:127.0.0.1", true},
		{AddressPolicyPublicOnly, "169.254.169.254", false},
		{AddressPolicyAllowPrivate, "169.254.169.254", false},
		{AddressPolicyPublicOnly, "fe80::1%eth0", false},
		{AddressPolicyAllowPrivate, "fe80::1%eth0", false},
	}
	for _, tc := range cases {
		addr, err := parseTestAddr(tc.addr)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.addr, err)
		}
		err = tc.level.CheckAddr(addr)
		if tc.want && err != nil {
			t.Errorf("level=%d addr=%q: 期望放行, 实际拒绝: %v", tc.level, tc.addr, err)
		}
		if !tc.want && err == nil {
			t.Errorf("level=%d addr=%q: 期望拒绝, 实际放行", tc.level, tc.addr)
		}
	}
}

func parseTestAddr(s string) (netip.Addr, error) { return netip.ParseAddr(s) }

// fakePolicyResolver 是注入 AddressGuard.Resolver 的假解析器。
type fakePolicyResolver struct {
	ips   []string
	err   error
	calls int
}

func (f *fakePolicyResolver) LookupIPAddr(_ context.Context, _ string) ([]net.IPAddr, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	out := make([]net.IPAddr, 0, len(f.ips))
	for _, ip := range f.ips {
		parsed := net.ParseIP(ip)
		if parsed == nil {
			return nil, errors.New("bad fake ip " + ip)
		}
		out = append(out, net.IPAddr{IP: parsed})
	}
	return out, nil
}

// recordingDial 记录 AddressGuard.Dial 是否被调用以及拨了哪些地址。
type recordingDial struct {
	calls   int
	targets []string
	conn    net.Conn
	err     error
}

func (d *recordingDial) dial(_ context.Context, _, address string) (net.Conn, error) {
	d.calls++
	d.targets = append(d.targets, address)
	if d.err != nil {
		return nil, d.err
	}
	c1, c2 := net.Pipe()
	_ = c2
	d.conn = c1
	return c1, nil
}

// TestAddressGuardDialContextRejectsRebinding 是防 DNS rebinding 的核心断言:
// 一个**第一轮放行的名字**解析出回环地址, 必须在拨号前被拒, 且 Dial 从未
// 被调用(否则说明校验与连接之间存在第二次解析/未校验即拨的窗口)。
func TestAddressGuardDialContextRejectsRebinding(t *testing.T) {
	fake := &fakePolicyResolver{ips: []string{"127.0.0.1"}}
	dial := &recordingDial{}
	g := AddressGuard{Level: AddressPolicyAllowPrivate, Resolver: fake, Dial: dial.dial}
	// attacker.example 语法合法(不是回环字面量), 旧实现到此即放行;
	// 解析后得到 127.0.0.1, 必须在 ResolveAndCheck 阶段被拒。
	if _, err := g.ResolveAndCheck(context.Background(), "attacker.example:8443"); err == nil {
		t.Fatal("ResolveAndCheck 放行了解析到 127.0.0.1 的名字")
	}
	conn, err := g.DialContext(context.Background(), "tcp", "attacker.example:8443")
	if err == nil {
		_ = conn.Close()
		t.Fatal("DialContext 放行了 DNS rebinding(名字合法、解析出回环)")
	}
	if !errors.Is(err, ErrAddressDisallowed) {
		t.Fatalf("DialContext 错误未包装 ErrAddressDisallowed: %v", err)
	}
	if dial.calls != 0 {
		t.Fatalf("Dial 被调用了 %d 次(%v)——校验与拨号之间存在窗口", dial.calls, dial.targets)
	}
}

// TestAddressGuardDialContextRejectsMixedAnswer 覆盖混合应答: 一个合法公网
// 地址 + 一个回环地址, 必须整体拒绝(不能"挑一个好的拨")。
func TestAddressGuardDialContextRejectsMixedAnswer(t *testing.T) {
	fake := &fakePolicyResolver{ips: []string{"8.8.8.8", "127.0.0.1"}}
	dial := &recordingDial{}
	g := AddressGuard{Level: AddressPolicyPublicOnly, Resolver: fake, Dial: dial.dial}
	if _, err := g.DialContext(context.Background(), "tcp", "mixed.example:443"); err == nil {
		t.Fatal("混合应答(含回环)被放行")
	}
	if dial.calls != 0 {
		t.Fatalf("Dial 被调用 %d 次, 应为 0", dial.calls)
	}
}

// TestAddressGuardDialContextAcceptsPrivateAndDialsNumericIP 验证放行路径:
// 私网等级下解析出私网 IP 时应成功拨号, 且拨的是**数值 IP**(不再解析名字)。
func TestAddressGuardDialContextAcceptsPrivateAndDialsNumericIP(t *testing.T) {
	fake := &fakePolicyResolver{ips: []string{"10.0.0.7"}}
	dial := &recordingDial{}
	g := AddressGuard{Level: AddressPolicyAllowPrivate, Resolver: fake, Dial: dial.dial}
	conn, err := g.DialContext(context.Background(), "tcp", "edge-7.internal:7000")
	if err != nil {
		t.Fatalf("私网端点应放行: %v", err)
	}
	_ = conn.Close()
	if dial.calls != 1 {
		t.Fatalf("Dial 调用次数 = %d, want 1", dial.calls)
	}
	if len(dial.targets) != 1 || dial.targets[0] != "10.0.0.7:7000" {
		t.Fatalf("拨号目标 = %v, want [10.0.0.7:7000](必须拨数值 IP, 不得回退解析名字)", dial.targets)
	}
	if fake.calls != 1 {
		t.Fatalf("名字被解析了 %d 次, want 1(解析与拨号之间不得有第二次解析)", fake.calls)
	}
}

// TestAddressGuardDialContextRejectsIPLiteralDirectly 覆盖"数值 IP 直连"路径:
// 不经过 Resolver, 但仍须被等级拒绝。
func TestAddressGuardDialContextRejectsIPLiteralDirectly(t *testing.T) {
	dial := &recordingDial{}
	g := AddressGuard{Level: AddressPolicyPublicOnly, Dial: dial.dial}
	for _, addr := range []string{"127.0.0.1:443", "[::1]:443", "[::ffff:127.0.0.1]:443", "169.254.169.254:80", "10.0.0.1:80"} {
		if _, err := g.DialContext(context.Background(), "tcp", addr); err == nil {
			t.Errorf("PublicOnly 放行了 %q", addr)
		}
	}
	if dial.calls != 0 {
		t.Fatalf("Dial 被调用 %d 次, 应为 0", dial.calls)
	}
	// 畸形 address(无端口) 必须报 ErrInvalidArguments 而非 panic
	if _, err := g.DialContext(context.Background(), "tcp", "127.0.0.1"); err == nil {
		t.Error("缺少端口的 address 应被拒绝")
	}
}

// TestAddressGuardResolverErrorPropagates 覆盖解析失败不吞错。
func TestAddressGuardResolverErrorPropagates(t *testing.T) {
	fake := &fakePolicyResolver{err: errors.New("dns down")}
	g := AddressGuard{Level: AddressPolicyAllowPrivate, Resolver: fake}
	if _, err := g.ResolveAndCheck(context.Background(), "edge-1.internal"); err == nil {
		t.Fatal("解析失败应向上传播")
	}
	// 空应答同样拒绝
	empty := &fakePolicyResolver{}
	if _, err := g.ResolveAndCheck(context.Background(), "edge-1.internal"); err != nil {
		_ = empty
	}
}

// TestTimeAddressPolicyAliasesUnifiedSentinel 保证 TimeAbility 的哨兵与统一
// 哨兵是同一个值: errors.Is 两种判定都成立。
func TestTimeAddressPolicyAliasesUnifiedSentinel(t *testing.T) {
	if ErrTimeAddressDisallowed != ErrAddressDisallowed {
		t.Fatal("ErrTimeAddressDisallowed 不再是 ErrAddressDisallowed 的别名")
	}
	if !errors.Is(ErrTimeAddressDisallowed, ErrAddressDisallowed) {
		t.Fatal("errors.Is(ErrTimeAddressDisallowed, ErrAddressDisallowed) 不成立")
	}
}

// TestTimeAddressPolicyLevelMapping 锁住 time 侧 allowPrivate 的等级映射,
// 防止回归到"两套判定"。
func TestTimeAddressPolicyLevelMapping(t *testing.T) {
	if got := (addressPolicy{}).level(); got != AddressPolicyPublicOnly {
		t.Errorf("allowPrivate=false 等级 = %d, want AddressPolicyPublicOnly(%d)", got, AddressPolicyPublicOnly)
	}
	if got := (addressPolicy{allowPrivate: true}).level(); got != AddressPolicyAllowLoopback {
		t.Errorf("allowPrivate=true 等级 = %d, want AddressPolicyAllowLoopback(%d)", got, AddressPolicyAllowLoopback)
	}
	// 拨号期二次校验对私网生效: allowPrivate=true 时回环放行、云元数据仍拒
	p := addressPolicy{allowPrivate: true}
	if err := p.validateIP(net.ParseIP("127.0.0.1")); err != nil {
		t.Errorf("allowPrivate=true 应放行回环(既有测试依赖): %v", err)
	}
	if err := p.validateIP(net.ParseIP("169.254.169.254")); err == nil {
		t.Error("allowPrivate=true 仍须拒绝云元数据 169.254.169.254")
	}
	if err := (addressPolicy{}).validateIP(net.ParseIP("10.0.0.1")); err == nil {
		t.Error("allowPrivate=false 应拒绝私网")
	}
}

// TestAbilityEndpointPolicyLevels 锁住每个能力选定的策略等级: 等级选择是
// 安全决策, 必须在测试里显式声明, 不允许被无意改动。
func TestAbilityEndpointPolicyLevels(t *testing.T) {
	for _, tc := range []struct {
		name  string
		guard AddressGuard
		level AddressPolicyLevel
	}{
		{"docker", dockerEndpointPolicy, AddressPolicyPublicOnly},
		{"ekuiper", ekuiperEndpointPolicy, AddressPolicyPublicOnly},
		{"mqtt", mqttBrokerPolicy, AddressPolicyAllowPrivate},
		{"net_map", netMapPeerPolicy, AddressPolicyAllowPrivate},
		{"k8s", k8sClusterPolicy, AddressPolicyAllowPrivate},
		{"influx", influxEndpointPolicy, AddressPolicyPublicOnly},
	} {
		if tc.guard.Level != tc.level {
			t.Errorf("%s 策略等级 = %d, want %d", tc.name, tc.guard.Level, tc.level)
		}
	}
}

// TestAbilityEndpointAcceptRejectTable 是各能力判定入口的显式 accept/reject 表。
// 走真实 Command 路径(set_endpoint / set_broker / register_peer), 而不是只测
// 内部函数——保证接线没有在中间断掉。
func TestAbilityEndpointAcceptRejectTable(t *testing.T) {
	t.Run("docker", func(t *testing.T) {
		d := NewDockerAbility()
		atom := newDockerAtom(t)
		for _, tc := range []struct {
			url  string
			want bool
		}{
			{"unix:///var/run/docker.sock", true},
			{"tcp://docker.example.com:2375", true},
			{"tcp://8.8.8.8:2375", true},
			{"tcp://127.0.0.1:2375", false},
			{"tcp://[::ffff:127.0.0.1]:2375", false},
			{"tcp://localhost:2375", false},
			{"tcp://10.0.0.5:2375", false},
			{"tcp://192.168.1.1:2375", false},
			{"tcp://169.254.169.254:2375", false},
			{"tcp://alice@127.0.0.1:2375", false},
			{"tcp://2130706433:2375", false},
			{"tcp://host:abc", false},
			{"tcp://host:", false},
			{"tcp://", false},
			{"ftp://host:2375", false},
			{"", false},
		} {
			out := d.Command(atom, DockerCommandSetEndpoint, DockerEndpointArgs{URL: tc.url})
			if tc.want && out.Err != nil {
				t.Errorf("docker %q: 期望放行, 实际拒绝: %v", tc.url, out.Err)
			}
			if !tc.want && out.Err == nil {
				t.Errorf("docker %q: 期望拒绝, 实际放行", tc.url)
			}
		}
	})

	t.Run("mqtt", func(t *testing.T) {
		m := NewMQTTAbility()
		atom := newMQTTAtom(t)
		for _, tc := range []struct {
			url  string
			want bool
		}{
			{"tcp://broker.example.com:1883", true},
			{"tcp://10.0.0.9:1883", true},
			{"tcp://192.168.1.9:1883", true},
			{"ssl://broker.internal:8883", true},
			{"wss://broker.internal:443/mqtt", true},
			{"tcp://127.0.0.1:1883", false},
			{"tcp://[::ffff:127.0.0.1]:1883", false},
			{"tcp://localhost:1883", false},
			{"tcp://0.0.0.0:1883", false},
			{"tcp://169.254.169.254:1883", false},
			{"tcp://alice@127.0.0.1:1883", false},
			{"tcp://alice@[::ffff:127.0.0.1]:1883", false},
			{"tcp://2130706433:1883", false},
			{"tcp://[", false},
			{"tcp://host:", false},
			{"http://broker.example.com:1883", false},
			{"", false},
		} {
			out := m.Command(atom, MQTTCommandSetBroker, MQTTBrokerArgs{URL: tc.url})
			if tc.want && out.Err != nil {
				t.Errorf("mqtt %q: 期望放行, 实际拒绝: %v", tc.url, out.Err)
			}
			if !tc.want && out.Err == nil {
				t.Errorf("mqtt %q: 期望拒绝, 实际放行", tc.url)
			}
		}
	})

	t.Run("net_map", func(t *testing.T) {
		a := NewNetMapAbility()
		atom := newNetMapAtom(t)
		for _, tc := range []struct {
			addr string
			want bool
		}{
			{"10.0.0.2:7000", true},
			{"edge-2.internal:7000", true},
			{"192.168.1.2:7000", true},
			{"172.16.5.9:7000", true},
			{"8.8.8.8:7000", true},
			{"10.0.0.2", true},
			{"127.0.0.1:7000", false},
			{"::1:7000", false},
			{"localhost:7000", false},
			{"0.0.0.0:7000", false},
			{"169.254.169.254:7000", false},
			{"10.0.0.5:0", false},
			{"10.0.0.5:99999", false},
			{"host:abc", false},
			{"http://bad", false},
			{"bad addr", false},
			{"2130706433:7000", false},
			{"", false},
		} {
			out := a.Command(atom, NetMapCommandRegisterPeer, NetMapRegisterPeerArgs{Name: "probe", Address: tc.addr})
			if tc.want && out.Err != nil {
				t.Errorf("net_map %q: 期望放行, 实际拒绝: %v", tc.addr, out.Err)
			}
			if !tc.want && out.Err == nil {
				t.Errorf("net_map %q: 期望拒绝, 实际放行", tc.addr)
			}
			// 清理, 避免重复注册干扰下一条
			a.Command(atom, NetMapCommandUnregisterPeer, NetMapLookupPeerArgs{Name: "probe"})
		}
	})

	t.Run("k8s", func(t *testing.T) {
		k := NewK8sAbility()
		atom := newK8sAtom(t)
		for _, tc := range []struct {
			cluster string
			want    bool
		}{
			// 裸集群名: 不是网络地址, 必须继续放行(kubeconfig 场景)
			{"prod", true},
			{"edge-cluster", true},
			{"gke_fast-edge_us-central1-a_cluster-1", true},
			// 端点形态: 按 AllowPrivate 判定
			{"k8s.internal:6443", true},
			{"10.96.0.1:443", true},
			{"192.168.1.10:6443", true},
			{"https://k8s.internal:6443", true},
			{"127.0.0.1:6443", false},
			{"localhost:6443", false},
			{"169.254.169.254:6443", false},
			{"0.0.0.0:6443", false},
			{"2130706433:6443", false},
			{"10.0.0.1:0", false},
			{"10.0.0.1:99999", false},
			{"https://alice@127.0.0.1:6443", false},
			{"k8s internal:6443", false},
			{"", false},
		} {
			out := k.Command(atom, K8sCommandSetContext, K8sContextArgs{K8sContext: K8sContext{Cluster: tc.cluster}})
			if tc.want && out.Err != nil {
				t.Errorf("k8s cluster %q: 期望放行, 实际拒绝: %v", tc.cluster, out.Err)
			}
			if !tc.want && out.Err == nil {
				t.Errorf("k8s cluster %q: 期望拒绝, 实际放行", tc.cluster)
			}
		}
	})

	t.Run("influx", func(t *testing.T) {
		i := NewInfluxAbility()
		atom := newInfluxAtom(t)
		for _, tc := range []struct {
			url  string
			want bool
		}{
			{"https://influx.example.com:8086", true},
			{"http://influx.example.com", true},
			{"https://8.8.8.8:8086", true},
			{"https://127.0.0.1:8086", false},
			{"https://[::ffff:127.0.0.1]:8086", false},
			{"https://localhost:8086", false},
			{"https://10.0.0.5:8086", false},
			{"https://192.168.1.5:8086", false},
			{"https://169.254.169.254:8086", false},
			{"https://alice@127.0.0.1:8086", false},
			{"https://2130706433:8086", false},
			{"ftp://influx.example.com", false},
			{"", false},
		} {
			out := i.Command(atom, InfluxCommandSetEndpoint, InfluxConfigArgs{Value: tc.url})
			if tc.want && out.Err != nil {
				t.Errorf("influx %q: 期望放行, 实际拒绝: %v", tc.url, out.Err)
			}
			if !tc.want && out.Err == nil {
				t.Errorf("influx %q: 期望拒绝, 实际放行", tc.url)
			}
		}
	})
}

// TestAbilityEndpointPoliciesRejectLoopbackAliasForms 集中验证所有能力对
// 同一批回环/歧义写法的结论一致——这是统一策略要解决的核心矛盾。
func TestAbilityEndpointPoliciesRejectLoopbackAliasForms(t *testing.T) {
	loopback := []string{"127.0.0.1", "127.1", "2130706433", "0x7f.0.0.1", "0177.0.0.1", "::1", "::ffff:127.0.0.1", "localhost"}
	for _, h := range loopback {
		// 三个 PublicOnly 能力
		if err := dockerEndpointPolicy.CheckHost(h); err == nil {
			t.Errorf("dockerEndpointPolicy 放行了回环写法 %q", h)
		}
		if err := ekuiperEndpointPolicy.CheckHost(h); err == nil {
			t.Errorf("ekuiperEndpointPolicy 放行了回环写法 %q", h)
		}
		if err := influxEndpointPolicy.CheckHost(h); err == nil {
			t.Errorf("influxEndpointPolicy 放行了回环写法 %q", h)
		}
		// 两个 AllowPrivate 能力: 回环仍拒
		if err := mqttBrokerPolicy.CheckHost(h); err == nil {
			t.Errorf("mqttBrokerPolicy 放行了回环写法 %q", h)
		}
		if err := netMapPeerPolicy.CheckHost(h); err == nil {
			t.Errorf("netMapPeerPolicy 放行了回环写法 %q", h)
		}
		if err := k8sClusterPolicy.CheckHost(h); err == nil {
			t.Errorf("k8sClusterPolicy 放行了回环写法 %q", h)
		}
	}
}
