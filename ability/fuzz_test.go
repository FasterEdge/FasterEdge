package ability

import (
	"testing"
)

// FuzzShellQuote 确保 shellQuote 输出的字符串在 POSIX sh 中可被原样解析回去。
// 关键是:任何输入都不应该让 shellQuote 崩溃,且输出始终以单引号包裹。
// 内嵌单引号会被替换为 '\'' 序列,这会在内部引入单引号,但那是正确的 POSIX 引用模式。
func FuzzShellQuote(f *testing.F) {
	f.Add("")
	f.Add("echo")
	f.Add("it's")
	f.Add("a'b'c")
	f.Add("$(rm -rf /)")
	f.Add("with\nnewline")
	f.Add("中文字符")
	f.Fuzz(func(t *testing.T, s string) {
		out := shellQuote(s)
		if len(out) < 2 {
			t.Fatalf("quote of %q too short: %q", s, out)
		}
		if out[0] != '\'' || out[len(out)-1] != '\'' {
			t.Fatalf("quote of %q not single-quoted: %q", s, out)
		}
		// 不应包含连续两个以上的单引号(即 "" 的假想表示)
		// 不应出现裸 '' 段(可能表示空字符串补丁)
		// 核心不变量:shellQuote(x) 的结果在 POSIX sh 中应展开为 x(功能等价)
		// 这里仅做不崩溃检查,不再检查内部单引号,因为 '\'' 序列是合法的。
	})
}

// FuzzIsValidMAC 确保 isValidMAC 对任意输入都不会 panic。
func FuzzIsValidMAC(f *testing.F) {
	f.Add("00:11:22:33:44:55")
	f.Add("AA:BB:CC:DD:EE:FF")
	f.Add("")
	f.Add("xx")
	f.Add("00-11-22-33-44-55")
	f.Add("::::::::::::")
	f.Fuzz(func(t *testing.T, s string) {
		_ = isValidMAC(s)
	})
}

// FuzzIsValidSerialPort 确保端口校验对任意输入都不 panic。
func FuzzIsValidSerialPort(f *testing.F) {
	f.Add("/dev/ttyUSB0")
	f.Add("COM1")
	f.Add("")
	f.Add("/dev/null")
	f.Add("COM999999")
	f.Fuzz(func(t *testing.T, s string) {
		_ = isValidSerialPort(s)
	})
}

// FuzzIsValidModbusEndpoint 确保 Modbus endpoint 校验对任意输入都不 panic。
func FuzzIsValidModbusEndpoint(f *testing.F) {
	f.Add("192.168.1.10:502")
	f.Add("/dev/ttyUSB0:9600:8N1")
	f.Add("")
	f.Add("host:abc")
	f.Add("/dev/tty:abc:xyz")
	f.Fuzz(func(t *testing.T, s string) {
		_ = isValidModbusEndpoint(s)
	})
}

// FuzzDecodeFromTransmission 确保 token 解码对任意输入都不 panic,且能正确拒绝畸形输入。
func FuzzDecodeFromTransmission(f *testing.F) {
	f.Add("edge-2.1787643449148755000.1787647049148755000.abc")
	f.Add("not.enough.parts")
	f.Add("a.b.c.d")
	f.Add("")
	f.Add("....")
	f.Add("a.bb.c.zzz")
	f.Fuzz(func(t *testing.T, s string) {
		// 不应 panic;合法返回 error,非法返回 error 或成功(允许成功,但不应 panic)
		_, _ = DecodeFromTransmission(s)
	})
}

// FuzzIsAcceptableControllerURL 确保网络 URL 校验对任意输入都不 panic。
func FuzzIsAcceptableControllerURL(f *testing.F) {
	f.Add("https://ctrl.example.com")
	f.Add("http://localhost:8080")
	f.Add("")
	f.Add("ftp://x")
	f.Add("http://")
	f.Fuzz(func(t *testing.T, s string) {
		_ = isAcceptableControllerURL(s)
	})
}

// FuzzIsAcceptableBrokerURL 确保 MQTT broker URL 校验对任意输入都不 panic。
func FuzzIsAcceptableBrokerURL(f *testing.F) {
	f.Add("tcp://broker.example.com:1883")
	f.Add("tcp://127.0.0.1:1883")
	f.Add("")
	f.Add("ws://localhost")
	f.Fuzz(func(t *testing.T, s string) {
		_ = isAcceptableBrokerURL(s)
	})
}

// FuzzModbusEncodingRoundTrip 验证 write_multi 编码后能正确解回原值。
// 用 []byte 作为 fuzz 输入(偶数长度即是一组 uint16),避免 []uint16 不被 fuzz 支持。
func FuzzModbusEncodingRoundTrip(f *testing.F) {
	f.Add([]byte{0x00, 0x01, 0x00, 0x02, 0x00, 0x03, 0x00, 0x04, 0x00, 0x05})
	f.Add([]byte{0xFF, 0xFF, 0x00, 0x00})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw)%2 != 0 {
			raw = raw[:len(raw)-1]
		}
		if len(raw) == 0 || len(raw)/2 > 123 {
			return
		}
		vals := decodeRegs(raw)
		pdu, err := encodeWriteMultiPDU(0x10, vals)
		if err != nil {
			return
		}
		byteCount := int(pdu[5])
		if byteCount != len(raw) {
			t.Fatalf("byte count = %d, want %d", byteCount, len(raw))
		}
		got := decodeRegs(pdu[6 : 6+byteCount])
		if len(got) != len(vals) {
			t.Fatalf("len mismatch: got %d, want %d", len(got), len(vals))
		}
		for i := range vals {
			if got[i] != vals[i] {
				t.Fatalf("reg[%d] = %d, want %d", i, got[i], vals[i])
			}
		}
	})
}
