// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package ability

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/FasterEdge/FasterEdge/types"
)

const (
	ModbusCommandSetEndpoint   = "set_endpoint"
	ModbusCommandGetEndpoint   = "get_endpoint"
	ModbusCommandReadHolding   = "read_holding"
	ModbusCommandReadInput     = "read_input"
	ModbusCommandReadCoils     = "read_coils"
	ModbusCommandReadDiscrete  = "read_discrete"
	ModbusCommandWriteHolding  = "write_holding"
	ModbusCommandWriteCoil     = "write_coil"
	ModbusCommandWriteMultiReg = "write_holding_multi"
	ModbusCommandSetUnitID     = "set_unit_id"
	ModbusCommandGetUnitID     = "get_unit_id"
)

// ModbusFunctionCode 是 Modbus PDU 功能码子集(本骨架实现覆盖最常用部分)。
type ModbusFunctionCode uint8

const (
	ModbusFuncReadCoils          ModbusFunctionCode = 0x01
	ModbusFuncReadDiscreteInputs ModbusFunctionCode = 0x02
	ModbusFuncReadHolding        ModbusFunctionCode = 0x03
	ModbusFuncReadInput          ModbusFunctionCode = 0x04
	ModbusFuncWriteCoil          ModbusFunctionCode = 0x05
	ModbusFuncWriteHolding       ModbusFunctionCode = 0x06
	ModbusFuncWriteMultiReg      ModbusFunctionCode = 0x10
)

// ModbusEndpointArgs 是 set_endpoint 命令的参数。
type ModbusEndpointArgs struct {
	// Addr 形如 "192.168.1.10:502" (TCP) 或 "/dev/ttyUSB0:9600:8N1" (RTU)。
	Addr string
}

// ModbusUnitIDArgs 是 set_unit_id / get_unit_id 的参数。
type ModbusUnitIDArgs struct {
	UnitID uint8
}

// ModbusReadArgs 是 read_* 命令的参数。
type ModbusReadArgs struct {
	Address  uint16
	Quantity uint16
}

// ModbusWriteHoldingArgs 是 write_holding 的参数。
type ModbusWriteHoldingArgs struct {
	Address uint16
	Value   uint16
}

// ModbusWriteCoilArgs 是 write_coil 的参数。
type ModbusWriteCoilArgs struct {
	Address uint16
	Value   bool
}

// ModbusWriteMultiArgs 是 write_holding_multi 的参数。
type ModbusWriteMultiArgs struct {
	Address uint16
	Values  []uint16
}

// ModbusReadResult 是 read_* 命令的返回结构。
type ModbusReadResult struct {
	Function ModbusFunctionCode
	Address  uint16
	Quantity uint16
	UnitID   uint8
	// Regs/Coils 至少有一个被填充;具体含义取决于 Function。
	Regs  []uint16
	Coils []bool
	Bytes []byte // 原始字节(供高级用法)
}

// ModbusTransport 抽象出 Modbus 字节流收发,允许用户注入 TCP/RTU 客户端。
// Send 接收完整请求 PDU,返回完整响应 PDU;返回 error 即视为通信失败。
type ModbusTransport interface {
	Send(unitID uint8, pdu []byte) ([]byte, error)
	Close() error
}

// ModbusAbility 提供 Modbus 协议读写能力,具体字节流由注入的 Transport 完成。
// 没有 Transport 时,所有读写命令返回 types.ErrInvalidArguments(防止误用)。
type ModbusAbility struct {
	mu        sync.RWMutex
	endpoint  string
	unitID    uint8
	transport ModbusTransport
}

func NewModbusAbility() *ModbusAbility {
	return &ModbusAbility{unitID: 1}
}

func (m *ModbusAbility) SetTransport(t ModbusTransport) {
	m.mu.Lock()
	m.transport = t
	m.mu.Unlock()
}

func (m *ModbusAbility) GetName() string { return "ModbusAbility" }

func (m *ModbusAbility) Describe() string {
	return "ModbusAbility提供 Modbus TCP/RTU 主站读写能力:holding/input/coil/discrete 寄存器与线圈,字节流通过注入的 Transport 发送。"
}

func (m *ModbusAbility) Check(atom *types.Atom) error {
	if atom == nil {
		return types.ErrMissingDependency
	}
	if _, ok := atom.Data("BaseData"); !ok {
		return types.ErrMissingDependency
	}
	return nil
}

func (m *ModbusAbility) Mount(atom *types.Atom) error { return m.Check(atom) }

// Unmount 释放注入的 transport 连接。旧实现未实现 Unmounter, 框架永不调用
// Close, 导致 TCP 连接/串口 fd 在 atom 拆除时泄漏。
func (m *ModbusAbility) Unmount(_ context.Context, _ *types.Atom) error {
	m.mu.RLock()
	transport := m.transport
	m.mu.RUnlock()
	if transport != nil {
		return transport.Close()
	}
	return nil
}

// Compile-time guarantee that ModbusAbility satisfies Unmounter.
var _ types.Unmounter = (*ModbusAbility)(nil)

func (m *ModbusAbility) Command(atom *types.Atom, act string, args any) types.CommandOutput {
	if err := m.Check(atom); err != nil {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, err)}
	}
	switch act {
	case ModbusCommandSetEndpoint:
		typed, ok := args.(ModbusEndpointArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		addr := strings.TrimSpace(typed.Addr)
		if !isValidModbusEndpoint(addr) {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: invalid endpoint %q: %w", act, addr, types.ErrInvalidArguments)}
		}
		m.mu.Lock()
		m.endpoint = addr
		m.mu.Unlock()
		return types.CommandOutput{Name: act, Value: addr}
	case ModbusCommandGetEndpoint:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		m.mu.RLock()
		defer m.mu.RUnlock()
		return types.CommandOutput{Name: act, Value: m.endpoint}
	case ModbusCommandSetUnitID:
		typed, ok := args.(ModbusUnitIDArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		m.mu.Lock()
		m.unitID = typed.UnitID
		m.mu.Unlock()
		return types.CommandOutput{Name: act, Value: typed.UnitID}
	case ModbusCommandGetUnitID:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		m.mu.RLock()
		defer m.mu.RUnlock()
		return types.CommandOutput{Name: act, Value: m.unitID}
	case ModbusCommandReadHolding, ModbusCommandReadInput, ModbusCommandReadCoils, ModbusCommandReadDiscrete:
		return m.handleRead(act, args)
	case ModbusCommandWriteHolding, ModbusCommandWriteCoil, ModbusCommandWriteMultiReg:
		return m.handleWrite(act, args)
	}
	return types.CommandOutput{Name: act, Err: fmt.Errorf("command %s: %w", act, types.ErrUnsupportedCommand)}
}

func (m *ModbusAbility) handleRead(act string, args any) types.CommandOutput {
	typed, ok := args.(ModbusReadArgs)
	if !ok {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
	}
	if typed.Quantity == 0 || typed.Quantity > 125 {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: quantity must be 1..125: %w", act, types.ErrInvalidArguments)}
	}
	funcCode := readFuncFor(act)
	if funcCode == 0 {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("command %s: %w", act, types.ErrUnsupportedCommand)}
	}
	pdu := encodeReadPDU(funcCode, typed.Address, typed.Quantity)
	return m.executeRead(act, funcCode, typed.Address, typed.Quantity, pdu)
}

func (m *ModbusAbility) handleWrite(act string, args any) types.CommandOutput {
	switch act {
	case ModbusCommandWriteHolding:
		typed, ok := args.(ModbusWriteHoldingArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		pdu := encodeWriteHoldingPDU(typed.Address, typed.Value)
		return m.executeWrite(act, ModbusFuncWriteHolding, pdu)
	case ModbusCommandWriteCoil:
		typed, ok := args.(ModbusWriteCoilArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		pdu := encodeWriteCoilPDU(typed.Address, typed.Value)
		return m.executeWrite(act, ModbusFuncWriteCoil, pdu)
	case ModbusCommandWriteMultiReg:
		typed, ok := args.(ModbusWriteMultiArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		if len(typed.Values) == 0 || len(typed.Values) > 123 {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: values length must be 1..123: %w", act, types.ErrInvalidArguments)}
		}
		pdu, err := encodeWriteMultiPDU(typed.Address, typed.Values)
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %w", act, types.ErrOperationFailed, err)}
		}
		return m.executeWrite(act, ModbusFuncWriteMultiReg, pdu)
	}
	return types.CommandOutput{Name: act, Err: fmt.Errorf("command %s: %w", act, types.ErrUnsupportedCommand)}
}

func (m *ModbusAbility) executeRead(act string, funcCode ModbusFunctionCode, address, quantity uint16, pdu []byte) types.CommandOutput {
	m.mu.RLock()
	unit := m.unitID
	transport := m.transport
	m.mu.RUnlock()
	if transport == nil {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no transport set: %w", act, types.ErrInvalidArguments)}
	}
	resp, err := transport.Send(unit, pdu)
	if err != nil {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: send: %w: %w", act, types.ErrOperationFailed, err)}
	}
	if len(resp) < 2 {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: short response (%d): %w", act, len(resp), types.ErrOperationFailed)}
	}
	if ModbusFunctionCode(resp[0]) != funcCode {
		// 异常响应:0x80 | funcCode + 异常码
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: device exception 0x%02x: %w", act, resp[0], types.ErrOperationFailed)}
	}
	byteCount := int(resp[1])
	if 2+byteCount > len(resp) {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: response truncated: %w", act, types.ErrOperationFailed)}
	}
	// byteCount 必须与 quantity 一致: 寄存器 q*2 字节, 线圈/离散 (q+7)/8 字节。
	// 短响应(byteCount 不足)是截断/伪造响应, 静默补零或截断会产出与声明不符
	// 的数据(旧实现只查长度上界)。
	expected := int(quantity) * 2
	if !isRegisterFunc(funcCode) {
		expected = (int(quantity) + 7) / 8
	}
	if byteCount != expected {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: byte count %d != expected %d for quantity %d: %w", act, byteCount, expected, quantity, types.ErrOperationFailed)}
	}
	payload := resp[2 : 2+byteCount]
	res := ModbusReadResult{
		Function: funcCode,
		Address:  address,
		Quantity: quantity,
		UnitID:   unit,
		Bytes:    append([]byte(nil), payload...),
	}
	if isRegisterFunc(funcCode) {
		res.Regs = decodeRegs(payload)
	} else {
		res.Coils = decodeCoils(payload, int(quantity))
	}
	return types.CommandOutput{Name: act, Value: res}
}

func (m *ModbusAbility) executeWrite(act string, funcCode ModbusFunctionCode, pdu []byte) types.CommandOutput {
	m.mu.RLock()
	unit := m.unitID
	transport := m.transport
	m.mu.RUnlock()
	if transport == nil {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no transport set: %w", act, types.ErrInvalidArguments)}
	}
	resp, err := transport.Send(unit, pdu)
	if err != nil {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: send: %w: %w", act, types.ErrOperationFailed, err)}
	}
	if len(resp) < 2 {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: short response (%d): %w", act, len(resp), types.ErrOperationFailed)}
	}
	if ModbusFunctionCode(resp[0]) != funcCode {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: device exception 0x%02x: %w", act, resp[0], types.ErrOperationFailed)}
	}
	// 写回显校验: FC05/06/10 回显均为 5 字节(功能码+地址+值/数量),
	// 必须与请求的 pdu[1:5] 一致(FC10 的 resp[3:5] 是数量, 由 pdu[3:5] 定义)。
	// 旧实现只查首字节功能码, {0x06,0x00,...} 之类的伪造/损坏响应会误报"成功"。
	// 注意: len<5 时错误消息里的 resp[:5] 会先于消息构造被执行——越界 panic,
	// 短响应(截断半包/伪造设备)从"优雅报错"退化为"进程崩溃"。按最小长度截取。
	if len(resp) < 5 || resp[1] != pdu[1] || resp[2] != pdu[2] || resp[3] != pdu[3] || resp[4] != pdu[4] {
		got := resp
		if len(got) > 5 {
			got = got[:5]
		}
		// 回显不匹配是设备/链路运行期行为(伪造/损坏响应), 非参数校验失败——
		// 与同函数短响应/异常码/截断一致用 ErrOperationFailed(旧实现误用
		// ErrInvalidArguments, 上层按哨兵分流会把运行期故障误判为调用方错误)。
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: write echo mismatch (got % x, want % x): %w", act, got, pdu[1:5], types.ErrOperationFailed)}
	}
	return types.CommandOutput{Name: act, Value: true}
}

func readFuncFor(act string) ModbusFunctionCode {
	switch act {
	case ModbusCommandReadHolding:
		return ModbusFuncReadHolding
	case ModbusCommandReadInput:
		return ModbusFuncReadInput
	case ModbusCommandReadCoils:
		return ModbusFuncReadCoils
	case ModbusCommandReadDiscrete:
		return ModbusFuncReadDiscreteInputs
	}
	return 0
}

func isRegisterFunc(c ModbusFunctionCode) bool {
	return c == ModbusFuncReadHolding || c == ModbusFuncReadInput
}
