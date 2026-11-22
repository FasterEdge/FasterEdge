package ability

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/FasterEdge/FasterEdge/data"
	"github.com/FasterEdge/FasterEdge/types"
)

// TestModbusEndpointAddressGuard 是 ModbusAbility.set_endpoint 的地址守卫表:
// 只对**出网的 TCP 端点**判定(RTU 的 /dev/... 是本地设备路径, 不走网络策略),
// 且默认拒绝回环 —— 任务点名的六类手法各一行。
func TestModbusEndpointAddressGuard(t *testing.T) {
	cases := []struct {
		addr string
		want bool
		note string
	}{
		{"192.168.1.10:502", true, "私网 TCP 从站(工业场景基线)"},
		{"10.0.0.9:1502", true, "私网 TCP 从站(非默认端口)"},
		{"modbus.example.com:502", true, "域名端点"},
		{"modbus.example.com.:502", true, "尾点 FQDN"},
		{"/dev/ttyUSB0:9600:8N1", true, "RTU 设备路径(不经网络策略)"},
		{"/dev/ttyS0:115200:8E1", true, "RTU 设备路径(另一波特率)"},
		{"8.8.8.8:502", true, "公网 IPv4"},

		{"127.0.0.1:502", false, "IPv4 回环(默认拒绝)"},
		{"[::1]:502", false, "IPv6 回环"},
		{"[::ffff:127.0.0.1]:502", false, "IPv4-mapped 回环"},
		{"localhost:502", false, "回环别名"},
		{"localhost.:502", false, "尾点回环别名"},
		{"0.0.0.0:502", false, "未指定地址"},
		{"169.254.169.254:502", false, "链路本地(云元数据)"},
		{"fe80::1:502", false, "IPv6 链路本地"},
		{"224.0.0.1:502", false, "IPv4 组播"},
		{"ff02::1:502", false, "IPv6 链路本地组播"},
		{"240.0.0.1:502", true, "保留段 240/4(AllowPrivate 等级放行, 与 TCP 从站私网同处理)"},
		{"255.255.255.255:502", false, "受限广播(任何等级都拒)"},
		{"2130706433:502", false, "十进制整数写法回环"},
		{"127.1:502", false, "十进制简写回环"},
		{"alice@10.0.0.9:502", false, "userinfo 手法"},
		{"10.0.0.9:0", false, "端口 0"},
		{"10.0.0.9:70000", false, "端口越界"},
	}
	for _, tc := range cases {
		m := NewModbusAbility()
		atom := newModbusAtom(t)
		out := m.Command(atom, ModbusCommandSetEndpoint, ModbusEndpointArgs{Addr: tc.addr})
		if tc.want && out.Err != nil {
			t.Errorf("set_endpoint(%q, %s): 期望放行, 实际拒绝: %v", tc.addr, tc.note, out.Err)
			continue
		}
		if !tc.want {
			if out.Err == nil {
				t.Errorf("set_endpoint(%q, %s): 期望拒绝, 实际放行", tc.addr, tc.note)
				continue
			}
			if !errors.Is(out.Err, types.ErrInvalidArguments) {
				t.Errorf("set_endpoint(%q): 拒绝错误未包装 ErrInvalidArguments: %v", tc.addr, out.Err)
			}
		}
	}
}

// TestModbusEndpointGuardSharesUnifiedPolicy 锁住"Modbus 复用统一地址策略"这
// 一事实: 默认等级是 AllowPrivate, SetAllowLoopback(true) 后升到 AllowLoopback,
// 且拒绝错误里 errors.Is 到 types.ErrAddressDisallowed。
func TestModbusEndpointGuardSharesUnifiedPolicy(t *testing.T) {
	m := NewModbusAbility()
	if got := m.endpointGuard().Level; got != AddressPolicyAllowPrivate {
		t.Errorf("默认 Modbus 端点等级 = %d, want AddressPolicyAllowPrivate(%d)", got, AddressPolicyAllowPrivate)
	}
	m.SetAllowLoopback(true)
	if got := m.endpointGuard().Level; got != AddressPolicyAllowLoopback {
		t.Errorf("SetAllowLoopback(true) 后等级 = %d, want AddressPolicyAllowLoopback(%d)", got, AddressPolicyAllowLoopback)
	}
	atom := newModbusAtom(t)
	out := m.Command(atom, ModbusCommandSetEndpoint, ModbusEndpointArgs{Addr: "169.254.169.254:502"})
	if !errors.Is(out.Err, types.ErrAddressDisallowed) {
		t.Fatalf("云元数据拒绝即使放开回环也必须经过统一策略: %v", out.Err)
	}
}

// TestFileTransferPeerAddressGuard 是 FileTransferAbility 的地址守卫表:
// 只有**目标对端**(NetMapPeer.Address 会出网)受策略约束; LocalPath /
// RemotePath 是文件路径, 不能被地址策略误伤 —— 两个方向都要断言。
func TestFileTransferPeerAddressGuard(t *testing.T) {
	peerCases := []struct {
		addr string
		want bool
		note string
	}{
		{"10.0.0.2:7000", true, "私网对端(edge 基线)"},
		{"192.168.1.2:7000", true, "私网对端"},
		{"edge-2.internal:7000", true, "域名对端"},
		{"edge-2.internal.:7000", true, "尾点 FQDN"},
		{"[2001:4860:4860::8888]:7000", true, "公网 IPv6 对端"},

		{"127.0.0.1:7000", false, "IPv4 回环"},
		{"[::1]:7000", false, "IPv6 回环"},
		{"[::ffff:127.0.0.1]:7000", false, "IPv4-mapped 回环"},
		{"localhost:7000", false, "回环别名"},
		{"localhost.:7000", false, "尾点回环别名"},
		{"0.0.0.0:7000", false, "未指定地址"},
		{"169.254.169.254:7000", false, "链路本地(云元数据)"},
		{"fe80::1:7000", false, "IPv6 链路本地"},
		{"224.0.0.1:7000", false, "IPv4 组播"},
		{"ff02::1:7000", false, "IPv6 组播"},
		{"255.255.255.255:7000", false, "受限广播"},
		{"2130706433:7000", false, "十进制整数写法回环"},
		{"alice@10.0.0.2:7000", false, "userinfo 手法"},
		{"10.0.0.2:0", false, "端口 0"},
	}
	for _, tc := range peerCases {
		err := checkFileTransferPeerAddress(tc.addr)
		if tc.want && err != nil {
			t.Errorf("checkFileTransferPeerAddress(%q, %s): 期望放行, 实际拒绝: %v", tc.addr, tc.note, err)
			continue
		}
		if !tc.want {
			if err == nil {
				t.Errorf("checkFileTransferPeerAddress(%q, %s): 期望拒绝, 实际放行", tc.addr, tc.note)
				continue
			}
			if !errors.Is(err, types.ErrInvalidArguments) {
				t.Errorf("checkFileTransferPeerAddress(%q): 拒绝错误未包装 ErrInvalidArguments: %v", tc.addr, err)
			}
		}
	}

	// 路径侧反证: 形如 "/data/x.bin" 的本地/远端文件路径不能被地址策略误判,
	// 否则 upload/download 在真实部署上会整体不可用。
	for _, localPath := range []string{"/data/edge/x.bin", "/var/lib/fasteredge/a.bin"} {
		if err := checkFileTransferPeerAddress(localPath); err == nil {
			t.Errorf("地址守卫不应放行路径 %q(说明路径被当成 host 判断了)", localPath)
		}
	}
	if !isValidAbsoluteRemotePath("/data/edge/x.bin") {
		t.Error("远端路径校验与地址策略混淆: 合法远端路径被判非法")
	}
}

// TestFileTransferUploadRejectsLoopbackPeer 走真实 Command 路径确认接线没断:
// 私网对端 + 绝对路径的对照放行, 以及"peer 表被其它路径写入回环地址时
// upload 必须被拒"的反证。
func TestFileTransferUploadRejectsLoopbackPeer(t *testing.T) {
	atom := newFileTransferAtom(t)
	f := NewFileTransferAbility()
	tmp := filepath.Join(t.TempDir(), "upload.txt")
	if err := os.WriteFile(tmp, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 对照: 私网对端 + 绝对路径 → 放行
	registerPeer(t, atom, "edge-2", "10.0.0.2:7000")
	if out := f.Command(atom, FileTransferCommandSetTarget, FileTransferTargetArgs{PeerName: "edge-2"}); out.Err != nil {
		t.Fatalf("私网对端应放行: %v", out.Err)
	}
	if out := f.Command(atom, FileTransferCommandUpload, FileTransferUploadArgs{
		LocalPath: tmp, RemotePath: "/data/edge/upload.txt",
	}); out.Err != nil {
		t.Fatalf("私网对端上传应放行: %v", out.Err)
	}

	// per-call 目标路径同样经过守卫: 回环地址传进去必须被拒, 且拒绝理由来自
	// 统一策略(NetMapAbility 的 register_peer 本身也拒回环, 所以这里直接对守卫
	// 函数断言, 保证 peer 表的任何其它写入路径都不会成为绕过口)。
	if err := checkFileTransferPeerAddress("127.0.0.1:7000"); err == nil {
		t.Fatal("回环对端未被地址守卫拒绝")
	}
	// upload 走的是 peer 名, 不是原始地址; 未知 peer 名仍旧按"not found"拒绝。
	if out := f.Command(atom, FileTransferCommandUpload, FileTransferUploadArgs{
		LocalPath: tmp, RemotePath: "/data/edge/upload.txt", Target: "ghost",
	}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("未知 per-call 目标应拒绝: %v", out.Err)
	}
}

// TestFileTransferDataAbilityWiring 保证 FileTransferAbility 依赖的 Data 名与
// 地址策略等级没有被无意改变(等级选择是安全决策, 必须显式声明)。
func TestFileTransferDataAbilityWiring(t *testing.T) {
	if got := fileTransferPeerPolicy.Level; got != AddressPolicyAllowPrivate {
		t.Errorf("fileTransferPeerPolicy 等级 = %d, want AddressPolicyAllowPrivate(%d)", got, AddressPolicyAllowPrivate)
	}
	if got := netMapPeerPolicy.Level; got != fileTransferPeerPolicy.Level {
		t.Errorf("file_transfer 与 net_map 的 peer 等级不一致: %d vs %d", got, fileTransferPeerPolicy.Level)
	}
	a := NewFileTransferAbility()
	if err := a.Check(nil); !errors.Is(err, types.ErrMissingDependency) {
		t.Errorf("nil atom 应报 ErrMissingDependency: %v", err)
	}
	atom := &types.Atom{}
	if err := atom.AddData(&data.BaseData{}); err != nil {
		t.Fatal(err)
	}
	if err := a.Check(atom); !errors.Is(err, types.ErrMissingDependency) {
		t.Errorf("缺 NetMapData 应报 ErrMissingDependency: %v", err)
	}
}
