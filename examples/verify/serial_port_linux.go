// ─────────────────────────────────────────────────────────────
// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
// ─────────────────────────────────────────────────────────────
//go:build linux

// Linux 串口后端: termios (ioctl TCGETS/TCSETS) + select。
// Linux 没有 COMx (Windows 的 Win32 通信 API 也不在任何 POSIX 平台存在), 因此
// 本文件与 serial_port_windows.go 按 build tag 互斥——examples/verify 在
// GOOS=windows/linux 下都能编译。
package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/FasterEdge/FasterEdge/ability"
)

// linuxTermios 是 Linux 的 struct termios (asm-generic: 4×uint32 + 行规程 +
// Cc[32] + Ispeed/Ospeed)。
type linuxTermios struct {
	Iflag  uint32
	Oflag  uint32
	Cflag  uint32
	Lflag  uint32
	Line   uint8
	Cc     [32]uint8
	Ispeed uint32
	Ospeed uint32
}

// linuxInterByteGap 是首字节到达后继续收取后续字节的空闲窗口: 串口没有报文
// 边界, 只靠它把一次 read 收敛成"已到达的这段字节"。
const linuxInterByteGap = 50 * time.Millisecond

// linuxSerialDevice 是一个已打开的 Linux 串口描述符。
type linuxSerialDevice struct {
	mu     sync.Mutex
	f      *os.File
	config ability.SerialConfig
	closed bool
}

// linuxNormalizePort 把 "ttyUSB0" 补成 "/dev/ttyUSB0"。
func linuxNormalizePort(port string) string {
	trimmed := strings.TrimSpace(port)
	if strings.HasPrefix(trimmed, "/dev/") {
		return trimmed
	}
	if strings.HasPrefix(trimmed, "tty") {
		return "/dev/" + trimmed
	}
	return trimmed
}

// openSerialDevice 打开端口并下发初始配置。
func openSerialDevice(port string, cfg ability.SerialConfig) (serialDevice, error) {
	path := linuxNormalizePort(port)
	// O_NOCTTY: 不让本进程把该 tty 占用为控制终端 (否则会话挂断时设备被收走);
	// O_NONBLOCK 只在 open 期间用, 随后清掉——读语义交给 select。
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", port, err)
	}
	if err := clearNonblock(f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("open %s: %w", port, err)
	}
	dev := &linuxSerialDevice{f: f, config: cfg}
	if err := dev.ApplyConfig(cfg); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("open %s: %w", port, err)
	}
	return dev, nil
}

// ApplyConfig 重设 termios 为 raw 模式并下发波特率/数据位/停止位/校验。
// 关掉回显/规范模式/输入输出转换是必须的: 否则 \n 被展开成 \r\n、输入被回显,
// 读到的就不是对端发的原始字节。
func (d *linuxSerialDevice) ApplyConfig(cfg ability.SerialConfig) error {
	var t linuxTermios
	if err := linuxIoctl(d.f.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&t))); err != nil {
		return fmt.Errorf("get termios: %w", err)
	}
	// Iflag: 关闭所有输入处理 (回车转换/流控/奇偶剥离)。
	const clearIflag = syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK |
		syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	t.Iflag &^= clearIflag
	// Oflag: 关输出处理 (\n 不被展开成 \r\n)。
	t.Oflag &^= syscall.OPOST
	// Lflag: 非规范模式, 关回显与信号字符。
	const clearLflag = syscall.ECHO | syscall.ECHONL | syscall.ICANON |
		syscall.ISIG | syscall.IEXTEN
	t.Lflag &^= clearLflag
	// Cflag: 本地连接 + 读使能, 清掉数据位/停止位/校验后按下发值重设。
	t.Cflag |= syscall.CLOCAL | syscall.CREAD
	t.Cflag &^= syscall.CSIZE | syscall.CSTOPB | syscall.PARENB | syscall.PARODD
	t.Cflag |= linuxDataBits(cfg.DataBits)
	switch strings.ToUpper(strings.TrimSpace(cfg.Parity)) {
	case "O":
		t.Cflag |= syscall.PARENB | syscall.PARODD
	case "E":
		t.Cflag |= syscall.PARENB
	}
	if cfg.StopBits == 2 {
		t.Cflag |= syscall.CSTOPB
	}
	// 波特率: 同时写 Ispeed/Ospeed 与 Cflag 里的 Bxxx——Linux 上 57600 以上
	// 的速率必须走 speed 字段 (Cflag 里没有对应位)。
	t.Ispeed = linuxBaud(cfg.Baud)
	t.Ospeed = t.Ispeed
	t.Cflag |= linuxBaudCFlag(cfg.Baud)
	// VMIN/VTIME: 超时控制交给 select, 这里要求"至少 1 字节、无字节间隔超时"。
	t.Cc[syscall.VMIN] = 1
	t.Cc[syscall.VTIME] = 0
	if err := linuxIoctl(d.f.Fd(), syscall.TCSETS, uintptr(unsafe.Pointer(&t))); err != nil {
		return fmt.Errorf("set termios: %w", err)
	}
	// 切换参数后丢弃旧缓冲 (速率采样口径已变)。
	if err := d.f.Sync(); err != nil {
		return fmt.Errorf("flush: %w", err)
	}
	d.mu.Lock()
	d.config = cfg
	d.mu.Unlock()
	return nil
}

// Read 读取至多 length 字节; 超时返回已读到的部分 (可能为空)。
func (d *linuxSerialDevice) Read(length int, timeout time.Duration) ([]byte, error) {
	if length <= 0 {
		return nil, fmt.Errorf("read: length must be positive")
	}
	if d.isClosed() {
		return nil, errSerialClosed
	}
	buf := make([]byte, length)
	out := make([]byte, 0, length)
	for len(out) < length {
		// 首字节用调用方给的 timeout, 后续字节用短窗口收敛一次读取。
		wait := timeout
		if len(out) > 0 {
			wait = linuxInterByteGap
		}
		n, err := linuxReadWait(d.f.Fd(), buf[:length-len(out)], wait)
		if n > 0 {
			out = append(out, buf[:n]...)
		}
		if err != nil {
			return out, fmt.Errorf("read: %w", err)
		}
		if n == 0 {
			// 超时或 EIO (设备被拔掉): 都按本次读取收敛。
			return out, nil
		}
	}
	return out, nil
}

// Write 写入全部数据; 短写按剩余部分继续 (流控未就绪时等写就绪)。
func (d *linuxSerialDevice) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, fmt.Errorf("write: empty payload")
	}
	if d.isClosed() {
		return 0, errSerialClosed
	}
	deadline := time.Now().Add(serialWriteTimeout)
	written := 0
	for written < len(data) {
		ok, err := linuxWriteWait(d.f.Fd(), time.Until(deadline))
		if err != nil {
			return written, fmt.Errorf("write: %w", err)
		}
		if !ok {
			return written, fmt.Errorf("write: not writable within %s", serialWriteTimeout)
		}
		n, err := d.f.Write(data[written:])
		written += n
		if err != nil {
			return written, fmt.Errorf("write: %w", err)
		}
		if n == 0 {
			return written, fmt.Errorf("write: device accepted 0 of %d bytes", len(data)-written)
		}
	}
	return written, nil
}

// Close 释放描述符; 重复 Close 幂等。
func (d *linuxSerialDevice) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	f := d.f
	d.mu.Unlock()
	if err := f.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	return nil
}

func (d *linuxSerialDevice) isClosed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.closed
}

// clearNonblock 清掉 open 时设的 O_NONBLOCK。
func clearNonblock(f *os.File) error {
	fd := int(f.Fd())
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFL, 0)
	if errno != 0 {
		return fmt.Errorf("fcntl F_GETFL: %w", errno)
	}
	_, _, errno = syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_SETFL, flags&^uintptr(syscall.O_NONBLOCK))
	if errno != 0 {
		return fmt.Errorf("fcntl F_SETFL: %w", errno)
	}
	return nil
}

// linuxIoctl 调用 termios 相关 ioctl。
func linuxIoctl(fd uintptr, req uintptr, arg uintptr) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, arg)
	if errno != 0 {
		return errno
	}
	return nil
}

// linuxReadWait 用 select 等 fd 可读后读取至多 len(buf) 字节。
// wait<=0 表示"只看已缓冲的数据", 不走 select 直接读。
func linuxReadWait(fd uintptr, buf []byte, wait time.Duration) (int, error) {
	if wait <= 0 {
		return linuxRead(fd, buf)
	}
	var r syscall.FdSet
	r.Bits[fd/64] |= 1 << (fd % 64)
	tv := linuxTimeval(wait)
	n, err := syscall.Select(int(fd)+1, &r, nil, nil, &tv)
	if err != nil {
		if err == syscall.EINTR {
			return 0, nil // 信号打断: 按"本次无数据"收敛
		}
		return 0, err
	}
	if n == 0 {
		return 0, nil // 超时
	}
	return linuxRead(fd, buf)
}

// linuxWriteWait 等 fd 可写; timeout<=0 时直接认为可写。
func linuxWriteWait(fd uintptr, timeout time.Duration) (bool, error) {
	if timeout <= 0 {
		return true, nil
	}
	var w syscall.FdSet
	w.Bits[fd/64] |= 1 << (fd % 64)
	tv := linuxTimeval(timeout)
	n, err := syscall.Select(int(fd)+1, nil, &w, nil, &tv)
	if err != nil {
		if err == syscall.EINTR {
			return true, nil
		}
		return false, err
	}
	return n > 0, nil
}

// linuxRead 从串口读一次; EIO (设备被拔掉) 归一为 0 字节。
func linuxRead(fd uintptr, buf []byte) (int, error) {
	n, err := syscall.Read(int(fd), buf)
	if err == syscall.EIO {
		return n, nil
	}
	return n, err
}

// linuxTimeval 把 time.Duration 转成 syscall.Timeval。
func linuxTimeval(d time.Duration) syscall.Timeval {
	return syscall.Timeval{
		Sec:  int64(d / time.Second),
		Usec: int64((d % time.Second) / time.Microsecond),
	}
}

// linuxDataBits 把数据位映射为 CS5..CS8。
func linuxDataBits(bits int) uint32 {
	switch bits {
	case 5:
		return syscall.CS5
	case 6:
		return syscall.CS6
	case 7:
		return syscall.CS7
	default:
		return syscall.CS8
	}
}

// linuxBaud 把波特率映射为 Bxxx 常量, 写进 Ispeed/Ospeed。
func linuxBaud(baud int) uint32 {
	switch baud {
	case 1200:
		return syscall.B1200
	case 2400:
		return syscall.B2400
	case 4800:
		return syscall.B4800
	case 9600:
		return syscall.B9600
	case 19200:
		return syscall.B19200
	case 38400:
		return syscall.B38400
	case 57600:
		return syscall.B57600
	case 115200:
		return syscall.B115200
	case 230400:
		return syscall.B230400
	case 460800:
		return syscall.B460800
	default:
		return syscall.B921600
	}
}

// linuxBaudCFlag 把波特率映射进 Cflag: B38400 及以下在 Cflag 里有对应位,
// B57600 以上必须走 Ispeed/Ospeed 字段, 因此这里只处理低位。
func linuxBaudCFlag(baud int) uint32 {
	switch baud {
	case 1200:
		return syscall.B1200
	case 2400:
		return syscall.B2400
	case 4800:
		return syscall.B4800
	case 9600:
		return syscall.B9600
	case 19200:
		return syscall.B19200
	default:
		return syscall.B38400
	}
}

// listSerialPorts 枚举 /dev 下的串口设备。
func listSerialPorts() ([]string, error) {
	entries, err := os.ReadDir("/dev")
	if err != nil {
		// /dev 不可读 (容器里被裁减) 时返回空列表而不是错误: list_ports 的语义
		// 是"尽可能枚举", 无法枚举不是能力故障。
		return []string{}, nil
	}
	ports := make([]string, 0, 8)
	for _, e := range entries {
		name := e.Name()
		// ttyS*/ttyUSB*/ttyACM* 是 Linux 上三类主要物理串口设备名。
		if !strings.HasPrefix(name, "ttyS") &&
			!strings.HasPrefix(name, "ttyUSB") &&
			!strings.HasPrefix(name, "ttyACM") {
			continue
		}
		if strings.HasPrefix(name, "ttySAC") {
			continue // Android 的伪设备, 不是物理串口
		}
		ports = append(ports, "/dev/"+name)
	}
	sort.Strings(ports)
	return ports, nil
}
