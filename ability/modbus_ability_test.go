package ability

import (
	"errors"
	"testing"

	"github.com/FasterEdge/FasterEdge/data"
	"github.com/FasterEdge/FasterEdge/types"
)

// fakeModbusTransport 记录最近一次请求并返回预设响应。
type fakeModbusTransport struct {
	unit   uint8
	pdu    []byte
	resp   []byte
	err    error
	closed bool
}

func (f *fakeModbusTransport) Send(unitID uint8, pdu []byte) ([]byte, error) {
	f.unit = unitID
	f.pdu = append([]byte(nil), pdu...)
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func (f *fakeModbusTransport) Close() error { f.closed = true; return nil }

func newModbusAtom(t *testing.T) *types.Atom {
	t.Helper()
	a := &types.Atom{}
	if err := a.AddData(&data.BaseData{}); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestModbusAbilityRejectsMissingDeps(t *testing.T) {
	m := NewModbusAbility()
	if out := m.Command(&types.Atom{}, ModbusCommandGetEndpoint, nil); !errors.Is(out.Err, types.ErrMissingDependency) {
		t.Fatalf("missing deps error = %v", out.Err)
	}
}

func TestModbusAbilitySetGetEndpointAndUnitID(t *testing.T) {
	m := NewModbusAbility()
	atom := newModbusAtom(t)
	// 类型错误
	if out := m.Command(atom, ModbusCommandSetEndpoint, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set wrong type error = %v", out.Err)
	}
	// 空地址
	if out := m.Command(atom, ModbusCommandSetEndpoint, ModbusEndpointArgs{Addr: ""}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set empty error = %v", out.Err)
	}
	// 各种非法格式
	for _, bad := range []string{"foo", "host:", ":502", "host:abc", "/dev/ttyUSB0", "/dev/ttyUSB0:abc:8N1"} {
		if out := m.Command(atom, ModbusCommandSetEndpoint, ModbusEndpointArgs{Addr: bad}); !errors.Is(out.Err, types.ErrInvalidArguments) {
			t.Errorf("set %q should reject, got %v", bad, out.Err)
		}
	}
	// 合法 TCP
	if out := m.Command(atom, ModbusCommandSetEndpoint, ModbusEndpointArgs{Addr: "192.168.1.10:502"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// 合法 RTU
	if out := m.Command(atom, ModbusCommandSetEndpoint, ModbusEndpointArgs{Addr: "/dev/ttyUSB0:9600:8N1"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// get
	if out := m.Command(atom, ModbusCommandGetEndpoint, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get with args error = %v", out.Err)
	}
	if out := m.Command(atom, ModbusCommandGetEndpoint, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if out.Value != "/dev/ttyUSB0:9600:8N1" {
		t.Fatalf("endpoint = %q", out.Value)
	}
	// set_unit_id
	if out := m.Command(atom, ModbusCommandSetUnitID, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set unit wrong type error = %v", out.Err)
	}
	if out := m.Command(atom, ModbusCommandSetUnitID, ModbusUnitIDArgs{UnitID: 3}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := m.Command(atom, ModbusCommandGetUnitID, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get unit with args error = %v", out.Err)
	}
	if out := m.Command(atom, ModbusCommandGetUnitID, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if v, _ := out.Value.(uint8); v != 3 {
		t.Fatalf("unit id = %d, want 3", v)
	}
}

func TestModbusAbilityRejectsReadWithoutTransport(t *testing.T) {
	m := NewModbusAbility()
	atom := newModbusAtom(t)
	if out := m.Command(atom, ModbusCommandReadHolding, ModbusReadArgs{Address: 0, Quantity: 1}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("no transport error = %v", out.Err)
	}
	// quantity 0 / 过大
	if out := m.Command(atom, ModbusCommandReadHolding, ModbusReadArgs{Quantity: 0}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("quantity 0 error = %v", out.Err)
	}
	if out := m.Command(atom, ModbusCommandReadHolding, ModbusReadArgs{Quantity: 200}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("quantity 200 error = %v", out.Err)
	}
	// 上界 126 非法(1..125 合法区间)(边界原只测 0 与 200)
	if out := m.Command(atom, ModbusCommandReadHolding, ModbusReadArgs{Quantity: 126}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("quantity 126 error = %v", out.Err)
	}
	// 类型错误
	if out := m.Command(atom, ModbusCommandReadHolding, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("read wrong type error = %v", out.Err)
	}
}

func TestModbusAbilityReadHoldingSuccess(t *testing.T) {
	m := NewModbusAbility()
	atom := newModbusAtom(t)
	// 模拟读 3 个保持寄存器的响应: FC=0x03, ByteCount=6, [0x00 0x01 0x00 0x02 0x00 0x03]
	resp := []byte{0x03, 0x06, 0x00, 0x01, 0x00, 0x02, 0x00, 0x03}
	ft := &fakeModbusTransport{resp: resp}
	m.SetTransport(ft)
	out := m.Command(atom, ModbusCommandReadHolding, ModbusReadArgs{Address: 100, Quantity: 3})
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	res, ok := out.Value.(ModbusReadResult)
	if !ok {
		t.Fatalf("result type = %T", out.Value)
	}
	if len(res.Regs) != 3 || res.Regs[0] != 1 || res.Regs[2] != 3 {
		t.Fatalf("regs = %#v", res.Regs)
	}
	if res.Address != 100 || res.Quantity != 3 {
		t.Fatalf("meta = %+v", res)
	}
	if ft.unit != 1 {
		t.Fatalf("unit id = %d", ft.unit)
	}
	// 校验请求 PDU
	if len(ft.pdu) != 5 || ft.pdu[0] != 0x03 {
		t.Fatalf("pdu = %v", ft.pdu)
	}
}

// TestModbusAbilityReadQuantityUpperBound 覆盖 1..125 合法区间的上界 125
// (旧测试只测 0 与 200 非法, 合法边界从未验证)。
func TestModbusAbilityReadQuantityUpperBound(t *testing.T) {
	m := NewModbusAbility()
	atom := newModbusAtom(t)
	resp := make([]byte, 0, 252)
	resp = append(resp, 0x03, 250) // FC=0x03, ByteCount=250
	for i := 0; i < 250; i++ {
		resp = append(resp, byte(i))
	}
	ft := &fakeModbusTransport{resp: resp}
	m.SetTransport(ft)
	out := m.Command(atom, ModbusCommandReadHolding, ModbusReadArgs{Address: 0, Quantity: 125})
	if out.Err != nil {
		t.Fatalf("quantity 125 should be valid: %v", out.Err)
	}
	res, ok := out.Value.(ModbusReadResult)
	if !ok || len(res.Regs) != 125 {
		t.Fatalf("regs = %#v", res)
	}
}

func TestModbusAbilityReadCoilsSuccess(t *testing.T) {
	m := NewModbusAbility()
	atom := newModbusAtom(t)
	// 10 个线圈: 0xB5 = 10110101 (LSB first → 1,0,1,0,1,1,0,1), 0x03 = 00000011 (LSB first → 1,1)
	resp := []byte{0x01, 0x02, 0xB5, 0x03}
	ft := &fakeModbusTransport{resp: resp}
	m.SetTransport(ft)
	out := m.Command(atom, ModbusCommandReadCoils, ModbusReadArgs{Address: 0, Quantity: 10})
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	res := out.Value.(ModbusReadResult)
	if len(res.Coils) != 10 {
		t.Fatalf("coils len = %d", len(res.Coils))
	}
	want := []bool{true, false, true, false, true, true, false, true, true, true}
	for i, w := range want {
		if res.Coils[i] != w {
			t.Errorf("coil[%d] = %v, want %v", i, res.Coils[i], w)
		}
	}
}

func TestModbusAbilityDeviceException(t *testing.T) {
	m := NewModbusAbility()
	atom := newModbusAtom(t)
	// 异常响应: 0x83 (0x80 | 0x03) + 异常码
	ft := &fakeModbusTransport{resp: []byte{0x83, 0x02}}
	m.SetTransport(ft)
	if out := m.Command(atom, ModbusCommandReadHolding, ModbusReadArgs{Quantity: 1}); !errors.Is(out.Err, types.ErrOperationFailed) {
		t.Fatalf("exception error = %v", out.Err)
	}
}

func TestModbusAbilityShortResponse(t *testing.T) {
	m := NewModbusAbility()
	atom := newModbusAtom(t)
	ft := &fakeModbusTransport{resp: []byte{0x03}}
	m.SetTransport(ft)
	if out := m.Command(atom, ModbusCommandReadHolding, ModbusReadArgs{Quantity: 1}); !errors.Is(out.Err, types.ErrOperationFailed) {
		t.Fatalf("short error = %v", out.Err)
	}
	ft = &fakeModbusTransport{resp: []byte{0x03, 0x10, 0x00, 0x01}}
	m.SetTransport(ft)
	if out := m.Command(atom, ModbusCommandReadHolding, ModbusReadArgs{Quantity: 8}); !errors.Is(out.Err, types.ErrOperationFailed) {
		t.Fatalf("truncated error = %v", out.Err)
	}
}

func TestModbusAbilityTransportError(t *testing.T) {
	m := NewModbusAbility()
	atom := newModbusAtom(t)
	m.SetTransport(&fakeModbusTransport{err: errors.New("net down")})
	if out := m.Command(atom, ModbusCommandReadHolding, ModbusReadArgs{Quantity: 1}); !errors.Is(out.Err, types.ErrOperationFailed) {
		t.Fatalf("transport error = %v", out.Err)
	}
}

func TestModbusAbilityWriteSingle(t *testing.T) {
	m := NewModbusAbility()
	atom := newModbusAtom(t)
	// write_holding: 回包 echo 请求前 5 字节
	ft := &fakeModbusTransport{resp: []byte{0x06, 0x00, 0x10, 0x12, 0x34}}
	m.SetTransport(ft)
	// 类型错误
	if out := m.Command(atom, ModbusCommandWriteHolding, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("write wrong type error = %v", out.Err)
	}
	if out := m.Command(atom, ModbusCommandWriteHolding, ModbusWriteHoldingArgs{Address: 0x10, Value: 0x1234}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if len(ft.pdu) != 5 || ft.pdu[0] != 0x06 {
		t.Fatalf("pdu = %v", ft.pdu)
	}
	// write_coil
	ft = &fakeModbusTransport{resp: []byte{0x05, 0x00, 0x20, 0xFF, 0x00}}
	m.SetTransport(ft)
	if out := m.Command(atom, ModbusCommandWriteCoil, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("write coil wrong type error = %v", out.Err)
	}
	if out := m.Command(atom, ModbusCommandWriteCoil, ModbusWriteCoilArgs{Address: 0x20, Value: true}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if ft.pdu[3] != 0xFF || ft.pdu[4] != 0x00 {
		t.Fatalf("coil pdu = %v", ft.pdu)
	}
	ft = &fakeModbusTransport{resp: []byte{0x05, 0x00, 0x20, 0x00, 0x00}}
	m.SetTransport(ft)
	if out := m.Command(atom, ModbusCommandWriteCoil, ModbusWriteCoilArgs{Address: 0x20, Value: false}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if ft.pdu[3] != 0x00 || ft.pdu[4] != 0x00 {
		t.Fatalf("false coil pdu = %v", ft.pdu)
	}
	// write_coil nil transport
	m2 := NewModbusAbility()
	if out := m2.Command(atom, ModbusCommandWriteCoil, ModbusWriteCoilArgs{Address: 0, Value: true}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("no transport write error = %v", out.Err)
	}
}

func TestModbusAbilityWriteMulti(t *testing.T) {
	m := NewModbusAbility()
	atom := newModbusAtom(t)
	// write_holding_multi
	if out := m.Command(atom, ModbusCommandWriteMultiReg, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("write multi wrong type error = %v", out.Err)
	}
	if out := m.Command(atom, ModbusCommandWriteMultiReg, ModbusWriteMultiArgs{Address: 0, Values: nil}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("empty values error = %v", out.Err)
	}
	big := make([]uint16, 200)
	if out := m.Command(atom, ModbusCommandWriteMultiReg, ModbusWriteMultiArgs{Address: 0, Values: big}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("too many values error = %v", out.Err)
	}
	// 正常: 3 个值 → ByteCount=6
	ft := &fakeModbusTransport{resp: []byte{0x10, 0x00, 0x10, 0x00, 0x03}}
	m.SetTransport(ft)
	if out := m.Command(atom, ModbusCommandWriteMultiReg, ModbusWriteMultiRegArgsForTest(0x10, []uint16{1, 2, 3})); out.Err != nil {
		t.Fatal(out.Err)
	}
	if len(ft.pdu) != 6+6 || ft.pdu[5] != 6 {
		t.Fatalf("pdu = %v", ft.pdu)
	}
}

// TestModbusAbilityWriteEchoMismatchIsOperationFailed: 回显不匹配是设备/链路
// 运行期行为——哨兵必须是 ErrOperationFailed(第九轮修复: 旧实现误用
// ErrInvalidArguments, 上层按哨兵分流会把运行期故障误判为调用方错误)。
func TestModbusAbilityWriteEchoMismatchIsOperationFailed(t *testing.T) {
	m := NewModbusAbility()
	atom := newModbusAtom(t)
	// 功能码对但回显载荷与请求不符(伪造/损坏响应)
	ft := &fakeModbusTransport{resp: []byte{0x06, 0x00, 0x10, 0xDE, 0xAD}}
	m.SetTransport(ft)
	if out := m.Command(atom, ModbusCommandWriteHolding, ModbusWriteHoldingArgs{Address: 0x10, Value: 0x1234}); !errors.Is(out.Err, types.ErrOperationFailed) {
		t.Fatalf("echo mismatch error = %v (want ErrOperationFailed)", out.Err)
	}
	// 短响应(<5 字节): 拒绝且不 panic(旧实现格式化 resp[:5] 越界)
	ft = &fakeModbusTransport{resp: []byte{0x06, 0x00, 0x10}}
	m.SetTransport(ft)
	if out := m.Command(atom, ModbusCommandWriteHolding, ModbusWriteHoldingArgs{Address: 0x10, Value: 0x1234}); !errors.Is(out.Err, types.ErrOperationFailed) {
		t.Fatalf("short echo error = %v (want ErrOperationFailed)", out.Err)
	}
	// 功能码异常(0x86 = 0x80|0x06)
	ft = &fakeModbusTransport{resp: []byte{0x86, 0x02}}
	m.SetTransport(ft)
	if out := m.Command(atom, ModbusCommandWriteHolding, ModbusWriteHoldingArgs{Address: 0x10, Value: 0x1234}); !errors.Is(out.Err, types.ErrOperationFailed) {
		t.Fatalf("device exception error = %v (want ErrOperationFailed)", out.Err)
	}
}

func TestModbusAbilityUnknownCommand(t *testing.T) {
	m := NewModbusAbility()
	atom := newModbusAtom(t)
	if out := m.Command(atom, "nope", nil); !errors.Is(out.Err, types.ErrUnsupportedCommand) {
		t.Fatalf("unknown error = %v", out.Err)
	}
}

// helper to keep imports minimal in test
func ModbusWriteMultiRegArgsForTest(addr uint16, vals []uint16) ModbusWriteMultiArgs {
	return ModbusWriteMultiArgs{Address: addr, Values: vals}
}
