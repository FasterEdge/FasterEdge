// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package ability

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// encodeReadPDU 构造读类功能码的请求 PDU:FC(1) + Address(2) + Quantity(2)。
func encodeReadPDU(funcCode ModbusFunctionCode, address, quantity uint16) []byte {
	pdu := make([]byte, 5)
	pdu[0] = byte(funcCode)
	binary.BigEndian.PutUint16(pdu[1:3], address)
	binary.BigEndian.PutUint16(pdu[3:5], quantity)
	return pdu
}

// encodeWriteHoldingPDU 构造写单个保持寄存器的 PDU。
func encodeWriteHoldingPDU(address, value uint16) []byte {
	pdu := make([]byte, 5)
	pdu[0] = byte(ModbusFuncWriteHolding)
	binary.BigEndian.PutUint16(pdu[1:3], address)
	binary.BigEndian.PutUint16(pdu[3:5], value)
	return pdu
}

// encodeWriteCoilPDU 构造写单个线圈的 PDU。true → 0xFF00,false → 0x0000。
func encodeWriteCoilPDU(address uint16, value bool) []byte {
	pdu := make([]byte, 5)
	pdu[0] = byte(ModbusFuncWriteCoil)
	binary.BigEndian.PutUint16(pdu[1:3], address)
	if value {
		binary.BigEndian.PutUint16(pdu[3:5], 0xFF00)
	} else {
		binary.BigEndian.PutUint16(pdu[3:5], 0x0000)
	}
	return pdu
}

// encodeWriteMultiPDU 构造写多个保持寄存器的 PDU。返回 PDU 与可能的错误。
func encodeWriteMultiPDU(address uint16, values []uint16) ([]byte, error) {
	byteCount := len(values) * 2
	if byteCount > 246 {
		return nil, fmt.Errorf("byte count %d exceeds 246", byteCount)
	}
	pdu := make([]byte, 6+byteCount)
	pdu[0] = byte(ModbusFuncWriteMultiReg)
	binary.BigEndian.PutUint16(pdu[1:3], address)
	binary.BigEndian.PutUint16(pdu[3:5], uint16(len(values)))
	pdu[5] = byte(byteCount)
	for i, v := range values {
		binary.BigEndian.PutUint16(pdu[6+2*i:6+2*i+2], v)
	}
	return pdu, nil
}

// decodeRegs 把字节流解码为 uint16 数组,大端序。
func decodeRegs(payload []byte) []uint16 {
	if len(payload)%2 != 0 {
		return nil
	}
	out := make([]uint16, len(payload)/2)
	for i := range out {
		out[i] = binary.BigEndian.Uint16(payload[2*i : 2*i+2])
	}
	return out
}

// decodeCoils 把字节流解码为 bool 数组,每个 bit 表示一个线圈状态。
func decodeCoils(payload []byte, quantity int) []bool {
	out := make([]bool, quantity)
	for i := 0; i < quantity; i++ {
		byteIdx := i / 8
		bitIdx := uint(i % 8)
		if byteIdx >= len(payload) {
			continue
		}
		out[i] = payload[byteIdx]&(1<<bitIdx) != 0
	}
	return out
}

// isValidModbusEndpoint 接受 host:port (TCP) 或 /dev/...:baud:format (RTU)。
func isValidModbusEndpoint(addr string) bool {
	if addr == "" {
		return false
	}
	if strings.HasPrefix(addr, "/") {
		// RTU 形如 /dev/ttyUSB0:9600:8N1 — 波特率必须为纯数字
		parts := strings.Split(addr, ":")
		if len(parts) < 3 {
			return false
		}
		if parts[1] == "" {
			return false
		}
		for _, r := range parts[1] {
			if r < '0' || r > '9' {
				return false
			}
		}
		// 简易 format 校验: 5-9 位,字母数字
		for _, r := range parts[2] {
			if !(r >= '0' && r <= '9') && !(r >= 'A' && r <= 'Z') && !(r >= 'a' && r <= 'z') {
				return false
			}
		}
		return true
	}
	// TCP
	idx := strings.LastIndex(addr, ":")
	if idx <= 0 || idx == len(addr)-1 {
		return false
	}
	host := addr[:idx]
	port := addr[idx+1:]
	if host == "" || port == "" {
		return false
	}
	for _, r := range port {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
