// ─────────────────────────────────────────────────────────────
// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
// ─────────────────────────────────────────────────────────────
//go:build windows

// Windows 串口后端: Win32 通信 API。
// 端口名规范化为 "\\.\COMx" 后交给 CreateFile (COM10 以上不加前缀会被解析成
// 旧式设备名而返回 ERROR_FILE_NOT_FOUND); DCB 承载波特率/数据位/停止位/校验,
// COMMTIMEOUTS 承载读超时——ability.SerialReadArgs.Timeout 真正生效的落点。
package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/FasterEdge/FasterEdge/ability"
)

const (
	winGenericRead  = 0x80000000
	winGenericWrite = 0x40000000
	winOpenExisting = 3

	// DCB.Flags 位 (WinBase.h)。
	winFlagBinary    = 0x0001 // fBinary: 二进制模式, 不做 EOF 处理
	winFlagParity    = 0x0002 // fParity: 使能校验检查
	winFlagDtrEnable = 0x0010 // fDtrControl = DTR_CONTROL_ENABLE
	winFlagRtsEnable = 0x1000 // fRtsControl = RTS_CONTROL_ENABLE

	winNoParity   = 0
	winOddParity  = 1
	winEvenParity = 2
	winOneStopBit = 0
	winTwoStopBit = 2

	// winDCBLength 是 WinBase.h 中 struct DCB 的固定字节长度。
	winDCBLength = 28

	// winMaxDword 用于 ReadIntervalTimeout 的"立即返回"语义。
	winMaxDword = 0xFFFFFFFF

	// winReadIntervalTimeout 是"首字节之后多久没有新字节就收敛本次读取"的
	// 空闲窗口; 串口没有报文边界, 用它把一次 read 收敛成已到达的那段字节。
	winReadIntervalTimeout = 20 // 毫秒
)

// winDCB 对应 Win32 DCB (Device Control Block)。
type winDCB struct {
	DCBlength  uint32
	BaudRate   uint32
	Flags      uint32
	WReserved  uint16
	XonLim     uint16
	XoffLim    uint16
	ByteSize   byte
	Parity     byte
	StopBits   byte
	XonChar    byte
	XoffChar   byte
	ErrorChar  byte
	EofChar    byte
	EvtChar    byte
	WReserved1 uint16
}

// winCommTimeouts 对应 Win32 COMMTIMEOUTS。
type winCommTimeouts struct {
	ReadIntervalTimeout         uint32
	ReadTotalTimeoutMultiplier  uint32
	ReadTotalTimeoutConstant    uint32
	WriteTotalTimeoutMultiplier uint32
	WriteTotalTimeoutConstant   uint32
}

// winSerialDevice 是一个已打开的 Windows 串口句柄。
type winSerialDevice struct {
	mu     sync.Mutex
	handle syscall.Handle
	config ability.SerialConfig
	closed bool
}

// winNormalizePort 规范化为 "\\.\COMx"。
func winNormalizePort(port string) string {
	trimmed := strings.TrimSpace(port)
	if strings.HasPrefix(trimmed, `\\.\`) || strings.HasPrefix(trimmed, `\\?\`) {
		return trimmed
	}
	if !strings.HasPrefix(strings.ToUpper(trimmed), "COM") {
		return trimmed
	}
	return `\\.\` + trimmed
}

// winParityByte 把 "N"/"O"/"E" 映射为 Win32 校验常量。
func winParityByte(p string) byte {
	switch strings.ToUpper(strings.TrimSpace(p)) {
	case "O":
		return winOddParity
	case "E":
		return winEvenParity
	default:
		return winNoParity
	}
}

// winStopBitsByte 把停止位映射为 Win32 常量。
func winStopBitsByte(bits int) byte {
	if bits == 2 {
		return winTwoStopBit
	}
	return winOneStopBit
}

// winCall 是 Win32 "返回 0 表示失败" 的调用的薄封装 (通信 API 都是这个约定;
// GetLastError 已由 Proc.Call 填进 Errno)。
func winCall(proc *syscall.LazyProc, args ...uintptr) error {
	for len(args) < 6 {
		args = append(args, 0)
	}
	r1, _, e := proc.Call(args[:6]...)
	if r1 == 0 {
		return fmt.Errorf("%s: %w", proc.Name, e)
	}
	return nil
}

// createCommHandle 以独占方式打开串口, 返回句柄。
//
// 先试 "\.\COMx" (COM10 以上的必需写法), 若报 ERROR_FILE_NOT_FOUND 再退回
// 裸 "COMx": 少数虚拟串口驱动 (如本机验证环境里的 \Device\Serial0) 只认裸名,
// 只认其中一种写法就会让合法设备变成"打不开"。两次都失败才返回错误。
func createCommHandle(port string) (syscall.Handle, error) {
	names := []string{winNormalizePort(port), strings.TrimSpace(port)}
	var lastErr error
	for _, name := range names {
		namep, err := syscall.UTF16PtrFromString(name)
		if err != nil {
			return syscall.InvalidHandle, err
		}
		// 同步句柄 (不带 FILE_FLAG_OVERLAPPED): COMMTIMEOUTS 对同步句柄同样
		// 生效, ReadFile 在超时/读满后返回, 读路径不需要重叠 I/O 那套事件机制。
		h, err := syscall.CreateFile(namep,
			winGenericRead|winGenericWrite,
			0, // 独占: 同一端口不被两个进程同时打开
			nil,
			winOpenExisting,
			0,
			0)
		if err == nil {
			return h, nil
		}
		lastErr = err
	}
	return syscall.InvalidHandle, lastErr
}

// openSerialDevice 打开端口并下发初始配置。
func openSerialDevice(port string, cfg ability.SerialConfig) (serialDevice, error) {
	h, err := createCommHandle(port)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", port, err)
	}
	dev := &winSerialDevice{handle: h, config: cfg}
	// 任一初始化步骤失败都先关句柄: 否则调用方看到 open 失败但 fd 泄漏。
	if err := dev.ApplyConfig(cfg); err != nil {
		_ = syscall.CloseHandle(h)
		return nil, fmt.Errorf("open %s: %w", port, err)
	}
	return dev, nil
}

// ApplyConfig 读取当前 DCB, 覆盖波特率/数据位/停止位/校验后下发。
// 用 Get→改→Set 而不是全零构造: DCB 里的 DTR/RTS 握手位保持设备当前状态。
func (d *winSerialDevice) ApplyConfig(cfg ability.SerialConfig) error {
	cur := winDCB{DCBlength: winDCBLength}
	if err := winCall(procGetCommState, uintptr(d.handle), uintptr(unsafe.Pointer(&cur))); err != nil {
		return err
	}
	parity := winParityByte(cfg.Parity)
	cur.BaudRate = uint32(cfg.Baud)
	cur.ByteSize = byte(cfg.DataBits)
	cur.Parity = parity
	cur.StopBits = winStopBitsByte(cfg.StopBits)
	// DCB 的 fBinary/fParity/fDtrControl/fRtsControl 位: 只置"二进制 + DTR/RTS
	// 常有效", 不置 fOutxCtsFlow/fOutxDsrFlow——置了流控位后, 对端不拉 CTS 的
	// 设备 (含无外设的 COM1) 上 WriteFile 会直接返回"写了 0 字节"。
	flags := uint32(winFlagBinary | winFlagDtrEnable | winFlagRtsEnable)
	if parity != winNoParity {
		flags |= winFlagParity
	}
	cur.Flags = flags
	cur.DCBlength = winDCBLength
	if err := winCall(procSetCommState, uintptr(d.handle), uintptr(unsafe.Pointer(&cur))); err != nil {
		return err
	}
	// 波特率变更后丢弃旧缓冲: 旧速率采样的字节按新速率解读就是脏数据。
	if err := d.purge(); err != nil {
		return err
	}
	d.mu.Lock()
	d.config = cfg
	d.mu.Unlock()
	return nil
}

// purge 清空收发缓冲 (PurgeComm: TxAbort|RxAbort|TxClear|RxClear)。
func (d *winSerialDevice) purge() error {
	const winPurgeAll = 0x1 | 0x2 | 0x4 | 0x8
	return winCall(procPurgeComm, uintptr(d.handle), winPurgeAll)
}

// setTimeouts 把本次读取的 timeout 落到 COMMTIMEOUTS。
//
// timeout<=0 表示"只取已缓冲数据": 按 Win32 文档, ReadIntervalTimeout=MAXDWORD
// 且两个 total-timeout 为 0 时 ReadFile 立刻返回缓冲区里现有的字节。旧写法
// (三个读超时全 0) 的语义恰好相反——那是"无限阻塞直到读满", 会让无数据的
// read 永久挂住 (实测 118s 未返回)。
func (d *winSerialDevice) setTimeouts(timeout time.Duration) error {
	to := winCommTimeouts{
		// 写超时: 设备侧流控卡死时同步写会永久阻塞, 用 5s 上限兜底。
		WriteTotalTimeoutConstant: uint32(serialWriteTimeout / time.Millisecond),
	}
	if timeout > 0 {
		to.ReadIntervalTimeout = winReadIntervalTimeout
		to.ReadTotalTimeoutConstant = uint32(timeout / time.Millisecond)
	} else {
		to.ReadIntervalTimeout = winMaxDword
	}
	return winCall(procSetCommTimeouts, uintptr(d.handle), uintptr(unsafe.Pointer(&to)))
}

// Write 写入全部数据; 短写按剩余部分继续, 总时长受 5s 上限约束。
func (d *winSerialDevice) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, fmt.Errorf("write: empty payload")
	}
	if d.isClosed() {
		return 0, errSerialClosed
	}
	if err := d.setTimeouts(0); err != nil {
		return 0, err
	}
	written := 0
	for written < len(data) {
		var done uint32
		err := syscall.WriteFile(d.handle, data[written:], &done, nil)
		written += int(done)
		if err != nil {
			return written, fmt.Errorf("write: %w", err)
		}
		if done == 0 {
			return written, fmt.Errorf("write: device accepted 0 of %d bytes", len(data)-written)
		}
	}
	return written, nil
}

// Read 读取至多 length 字节; 超时返回已读到的部分 (可能为空)。
func (d *winSerialDevice) Read(length int, timeout time.Duration) ([]byte, error) {
	if length <= 0 {
		return nil, fmt.Errorf("read: length must be positive")
	}
	if d.isClosed() {
		return nil, errSerialClosed
	}
	if err := d.setTimeouts(timeout); err != nil {
		return nil, err
	}
	buf := make([]byte, length)
	var done uint32
	if err := syscall.ReadFile(d.handle, buf, &done, nil); err != nil {
		// 同步句柄的读超时不报错而是返回 0 字节, 走到这里是真实 I/O 故障。
		return nil, fmt.Errorf("read: %w", err)
	}
	if done == 0 {
		if timeout <= 0 {
			// 无数据可读不是错误: ability 层把空切片视为"本次没有数据"。
			return []byte{}, nil
		}
		return nil, fmt.Errorf("read: no data within %s", timeout)
	}
	return buf[:done], nil
}

// Close 释放句柄; 重复 Close 幂等。
func (d *winSerialDevice) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	h := d.handle
	d.mu.Unlock()
	if err := syscall.CloseHandle(h); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	return nil
}

func (d *winSerialDevice) isClosed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.closed
}

// listSerialPorts 枚举本机串口: 注册表 HARDWARE\DEVICEMAP\SERIALCOMM 的每个
// 值是 (设备描述名, 端口名), 端口名在值数据里。
func listSerialPorts() ([]string, error) {
	sub, err := syscall.UTF16PtrFromString(`HARDWARE\DEVICEMAP\SERIALCOMM`)
	if err != nil {
		return nil, fmt.Errorf("list ports: %w", err)
	}
	var key syscall.Handle
	if err := syscall.RegOpenKeyEx(syscall.HKEY_LOCAL_MACHINE, sub, 0, syscall.KEY_READ, &key); err != nil {
		// 机器上没有任何串口时该键不存在: 空列表是正常结果, 不是故障。
		if errno, ok := err.(syscall.Errno); ok && errno == syscall.ERROR_FILE_NOT_FOUND {
			return []string{}, nil
		}
		return nil, fmt.Errorf("list ports: RegOpenKeyEx: %w", err)
	}
	defer func() { _ = syscall.RegCloseKey(key) }()

	var values, maxName, maxValue uint32
	if err := syscall.RegQueryInfoKey(key, nil, nil, nil, nil, nil, nil,
		&values, &maxName, &maxValue, nil, nil); err != nil {
		return nil, fmt.Errorf("list ports: RegQueryInfoKey: %w", err)
	}
	if values == 0 {
		return []string{}, nil
	}
	nameBuf := make([]uint16, maxName+1)
	valueBuf := make([]uint16, maxValue+1)
	ports := make([]string, 0, values)
	for i := uint32(0); ; i++ {
		nameLen := uint32(len(nameBuf))
		valueLen := uint32(len(valueBuf))
		err := winRegEnumValue(key, i, &nameBuf[0], &nameLen, &valueBuf[0], &valueLen)
		if err != nil {
			break // ERROR_NO_MORE_ITEMS: 枚举结束
		}
		name := strings.TrimSpace(syscall.UTF16ToString(valueBuf[:valueLen/2]))
		if !isCOMLikePort(name) {
			continue
		}
		ports = append(ports, name)
	}
	sort.Strings(ports)
	return ports, nil
}

// isCOMLikePort 只保留 "COMx" 形式的端口名。
func isCOMLikePort(name string) bool {
	upper := strings.ToUpper(strings.TrimSpace(name))
	if !strings.HasPrefix(upper, "COM") {
		return false
	}
	_, err := strconv.Atoi(upper[3:])
	return err == nil
}
