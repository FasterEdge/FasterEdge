// ─────────────────────────────────────────────────────────────
// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
// ─────────────────────────────────────────────────────────────
// Package main 真实串口 transport 的测试: 覆盖"注入后透传"与"错误路径有明确
// error"两条主线。与仓库其它 verify 测试的分工: ability 包内的 fake transport
// 测 SerialAbility 的状态机, 这里测 serialTransport 本身与真实 OS 后端的
// 交互 (不依赖物理设备是否存在——无设备时走"打开失败"分支, 同样必须有明确
// 错误而不是 panic 或静默成功)。
package main

import (
	"errors"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FasterEdge/FasterEdge/ability"
	"github.com/FasterEdge/FasterEdge/data"
	"github.com/FasterEdge/FasterEdge/types"
)

var serialTestConfig = ability.SerialConfig{Baud: 115200, DataBits: 8, StopBits: 1, Parity: "N"}

// TestSerialTransportNoPortInjectedIsError 覆盖"未注入即失败": SerialAbility
// 没注入 transport 时 open 必须报错 (与 verify_skeleton.go 的
// Serial/open-no-transport 断言同一语义, 这里在 transport 层再钉一次)。
func TestSerialTransportNoPortInjectedIsError(t *testing.T) {
	s := ability.NewSerialAbility()
	atom := serialTestAtom(t)
	out := s.Command(atom, ability.SerialCommandOpen, ability.SerialOpenArgs{
		Port: "COM1", Config: serialTestConfig,
	})
	if out.Err == nil {
		t.Fatal("open without injected transport must fail")
	}
	if !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("error = %v, want ErrInvalidArguments", out.Err)
	}
	if out.Value != nil {
		t.Fatalf("value = %v, want nil", out.Value)
	}
}

// TestSerialTransportWriteWithoutOpen 覆盖"注入了 transport 但端口未打开"的
// 透传路径: write/read/close/set_config 都必须给出明确 error, 而不是 panic。
func TestSerialTransportWriteWithoutOpen(t *testing.T) {
	tr := newSerialTransport()
	cases := []struct {
		name string
		fn   func() error
	}{
		{"write", func() error { _, err := tr.Write("COM1", []byte("x")); return err }},
		{"read", func() error { _, err := tr.Read("COM1", 1, 0); return err }},
		{"close", func() error { return tr.Close("COM1") }},
		{"set_config", func() error { return tr.ApplyConfig("COM1", serialTestConfig) }},
	}
	for _, c := range cases {
		err := c.fn()
		if err == nil {
			t.Fatalf("%s on unopened port must fail", c.name)
		}
		if !strings.Contains(err.Error(), "not open") {
			t.Fatalf("%s error = %v, want 'not open'", c.name, err)
		}
	}
}

// TestSerialTransportValidationErrors 覆盖 transport 层的参数校验: 空端口、
// 空 payload、非正 length、超大 length、负超时都必须被明确拒绝。
func TestSerialTransportValidationErrors(t *testing.T) {
	tr := newSerialTransport()
	if err := tr.Open("", serialTestConfig); err == nil {
		t.Fatal("open with empty port must fail")
	}
	if _, err := tr.Write("COM1", nil); err == nil {
		t.Fatal("write with empty payload must fail")
	}
	if _, err := tr.Read("COM1", 0, 0); err == nil {
		t.Fatal("read with zero length must fail")
	}
	if _, err := tr.Read("COM1", -1, 0); err == nil {
		t.Fatal("read with negative length must fail")
	}
	if _, err := tr.Read("COM1", 1<<21, 0); err == nil {
		t.Fatal("read with oversized length must fail")
	}
	if _, err := tr.Read("COM1", 1, -time.Second); err == nil {
		t.Fatal("read with negative timeout must fail")
	}
}

// TestSerialTransportDoubleOpenAndDoubleClose 覆盖重复 open/close 的幂等语义:
// 双开返回明确错误 (与 SerialAbility 的重复开防护一致), 双关返回明确错误。
func TestSerialTransportDoubleOpenAndDoubleClose(t *testing.T) {
	port := probeSerialPort(t)
	if port == "" {
		t.Skip("本机没有可用串口设备, 跳过 open/close 往返")
	}
	tr := newSerialTransport()
	if err := tr.Open(port, serialTestConfig); err != nil {
		t.Fatalf("open %s: %v", port, err)
	}
	if err := tr.Open(port, serialTestConfig); err == nil {
		t.Fatal("double open must fail")
	}
	if err := tr.Close(port); err != nil {
		t.Fatalf("close %s: %v", port, err)
	}
	if err := tr.Close(port); err == nil {
		t.Fatal("double close must fail")
	}
}

// TestSerialTransportOpenUnavailablePort 覆盖"设备不存在"路径: 打开不存在的
// 端口必须返回明确 error (不是 panic, 也不是 nil)。
func TestSerialTransportOpenUnavailablePort(t *testing.T) {
	tr := newSerialTransport()
	err := tr.Open("COM_DOES_NOT_EXIST_98765", serialTestConfig)
	if err == nil {
		t.Skip("本机竟然存在名为 COM_DOES_NOT_EXIST_98765 的串口, 跳过")
	}
	if !strings.Contains(err.Error(), "COM_DOES_NOT_EXIST_98765") {
		t.Fatalf("error = %v, want port name in message", err)
	}
}

// TestSerialTransportAbilityRoundTrip 覆盖"注入后透传": SerialAbility 的
// open→is_open→get_config→write→close 全链路打到真实 transport 上, 每一步
// 的值都要回读断言, 防止 ability 层悄悄退回内存 map 假成功。
func TestSerialTransportAbilityRoundTrip(t *testing.T) {
	port := probeSerialPort(t)
	if port == "" {
		t.Skip("本机没有可用串口设备, 跳过 ability 往返")
	}
	tr := newSerialTransport()
	s := ability.NewSerialAbility()
	s.SetTransport(tr)
	s.SetLister(staticPortLister{ports: []string{port}})
	atom := serialTestAtom(t)

	if out := s.Command(atom, ability.SerialCommandListPorts, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if ports, _ := out.Value.([]string); len(ports) != 1 || ports[0] != port {
		t.Fatalf("list_ports = %v, want [%s]", ports, port)
	}

	cfg := ability.SerialConfig{Baud: 115200, DataBits: 8, StopBits: 1, Parity: "E"}
	if out := s.Command(atom, ability.SerialCommandOpen, ability.SerialOpenArgs{Port: port, Config: cfg}); out.Err != nil {
		t.Fatal(out.Err)
	} else if got, _ := out.Value.(string); got != port {
		t.Fatalf("open value = %q, want %q", got, port)
	}

	if out := s.Command(atom, ability.SerialCommandIsOpen, ability.SerialPortArg{Port: port}); out.Err != nil {
		t.Fatal(out.Err)
	} else if v, _ := out.Value.(bool); !v {
		t.Fatal("is_open = false, want true")
	}
	if out := s.Command(atom, ability.SerialCommandGetConfig, ability.SerialPortArg{Port: port}); out.Err != nil {
		t.Fatal(out.Err)
	} else if got, _ := out.Value.(ability.SerialConfig); got != cfg {
		t.Fatalf("get_config = %+v, want %+v", got, cfg)
	}

	// write 必须走真实设备句柄: 长度与 ability 的 Length 截断语义都要对。
	if out := s.Command(atom, ability.SerialCommandWrite, ability.SerialWriteArgs{Port: port, Data: []byte("hello"), Length: 3}); out.Err != nil {
		t.Fatal(out.Err)
	} else if n, _ := out.Value.(int); n != 3 {
		t.Fatalf("write n = %d, want 3", n)
	}

	// set_config 必须真正下发到设备 (不是只改 ability 的记录)。
	newCfg := ability.SerialConfig{Baud: 9600, DataBits: 7, StopBits: 2, Parity: "O"}
	if out := s.Command(atom, ability.SerialCommandSetConfig, ability.SerialSetConfigArgs{Port: port, Config: newCfg}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := s.Command(atom, ability.SerialCommandGetConfig, ability.SerialPortArg{Port: port}); out.Err != nil {
		t.Fatal(out.Err)
	} else if got, _ := out.Value.(ability.SerialConfig); got != newCfg {
		t.Fatalf("get_config after set_config = %+v, want %+v", got, newCfg)
	}

	// read 超时必须返回明确 error (没有设备回字节)。
	if out := s.Command(atom, ability.SerialCommandRead, ability.SerialReadArgs{Port: port, Length: 4, Timeout: 100 * time.Millisecond}); out.Err == nil {
		t.Fatal("read with no data must fail")
	}

	// close 之后 is_open 必须为 false, 再 close/读写必须报错。
	if out := s.Command(atom, ability.SerialCommandClose, ability.SerialPortArg{Port: port}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := s.Command(atom, ability.SerialCommandIsOpen, ability.SerialPortArg{Port: port}); out.Err != nil {
		t.Fatal(out.Err)
	} else if v, _ := out.Value.(bool); v {
		t.Fatal("is_open after close = true, want false")
	}
	if _, err := tr.Write(port, []byte("x")); err == nil {
		t.Fatal("write after close must fail")
	}
	if _, err := tr.Read(port, 1, 0); err == nil {
		t.Fatal("read after close must fail")
	}
}

// TestSerialTransportConcurrentReadWrite 覆盖并发安全: 多 goroutine 同时对
// 同一端口读写, transport 不得出现数据竞争或越界 (CI 有 -race 门禁)。
func TestSerialTransportConcurrentReadWrite(t *testing.T) {
	port := probeSerialPort(t)
	if port == "" {
		t.Skip("本机没有可用串口设备, 跳过并发读写")
	}
	tr := newSerialTransport()
	if err := tr.Open(port, serialTestConfig); err != nil {
		t.Fatalf("open %s: %v", port, err)
	}
	defer tr.Close(port)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				// 读用 0 超时: 只看已缓冲数据, 不因无数据而阻塞。
				_, _ = tr.Read(port, 8, 0)
				_, _ = tr.Write(port, []byte("ping"))
			}
		}()
	}
	wg.Wait()
}

// TestSerialTransportListPortsIsSortedUnique 覆盖 list_ports: 结果必须排序
// 且不含重复 (调用方按名字建端口表, 重复/无序会让 UI 与配置漂移)。
func TestSerialTransportListPortsIsSortedUnique(t *testing.T) {
	ports, err := listSerialPorts()
	if err != nil {
		t.Fatalf("listSerialPorts: %v", err)
	}
	seen := map[string]bool{}
	for i, p := range ports {
		if seen[p] {
			t.Fatalf("duplicate port %q in %v", p, ports)
		}
		seen[p] = true
		if i > 0 && ports[i-1] > p {
			t.Fatalf("ports not sorted: %v", ports)
		}
	}
}

// probeSerialPort 挑一个可用于往返的端口: 优先 listSerialPorts 的结果,
// 否则退回环境变量 FE_SERIAL_PORT (CI/容器联调时注入)。
//
// 枚举到不等于能用: CI runner 上 /dev/ttyS0 一定存在, 但当前用户没有权限
// 打开它。若只按"枚举结果非空"就往下走, 用例会拿到 EACCES 而 FAIL——那是
// 环境缺设备, 不是能力坏了。所以这里再打一次可读写探测, 打不开就返回空,
// 由调用方 skip。
func probeSerialPort(t *testing.T) string {
	t.Helper()
	candidates := []string{}
	if p := os.Getenv("FE_SERIAL_PORT"); p != "" {
		candidates = append(candidates, p)
	} else if ports, err := listSerialPorts(); err == nil {
		candidates = append(candidates, ports...)
	}
	for _, p := range candidates {
		if canOpenSerialPort(p) {
			return p
		}
	}
	return ""
}

// canOpenSerialPort 用只读打开探测权限, 不碰任何串口参数(不置 DTR/不改波特率),
// 因此探测本身对已连接的设备没有副作用。
//
// 只在非 Windows 上探测: Linux CI runner 的 /dev/ttyS0 恒存在但常无权限,
// 这是"枚举到却打不开"的典型场景; Windows 的端口来自注册表(SERIALCOMM),
// 枚举到即是已安装的端口, 不需要额外探测, 也避免对真实串口做多余的打开动作。
func canOpenSerialPort(port string) bool {
	if runtime.GOOS == "windows" {
		return true
	}
	f, err := os.OpenFile(port, os.O_RDONLY, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// serialTestAtom 构造只含 BaseData 的最小 atom (SerialAbility.Check 要求)。
func serialTestAtom(t *testing.T) *types.Atom {
	t.Helper()
	a := &types.Atom{}
	if err := a.AddData(&data.BaseData{}); err != nil {
		t.Fatal(err)
	}
	return a
}

// staticPortLister 让 list_ports 返回固定端口表。
type staticPortLister struct{ ports []string }

func (l staticPortLister) ListPorts() ([]string, error) { return l.ports, nil }
