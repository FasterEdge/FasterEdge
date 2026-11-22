// ─────────────────────────────────────────────────────────────
// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
// ─────────────────────────────────────────────────────────────
// Package main 最小真实串口 transport 实现 (仅用于验证程序, 不入主库依赖):
// 把 ability.SerialTransport / ability.SerialPortLister 落到操作系统真实的
// 串口设备上。Windows 走 Win32 通信 API (CreateFile + SetCommState + COMMTIMEOUTS),
// Linux 走 termios (ioctl TCSETS) + select。平台差异按 build tag 隔离:
// 一份实现无法同时覆盖 COMx 与 /dev/tty*, 因此 serial_port_*.go 按 GOOS 互斥,
// 未实现的平台落到 serial_port_unsupported.go, 让任何 GOOS 都能编译。
package main

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/FasterEdge/FasterEdge/ability"
)

const (
	// serialWriteTimeout 是单次写入的上限。ability.SerialTransport.Write 没有
	// 超时参数, 设备侧流控卡死时同步写会永久阻塞——旧实现 (仅接口无后端) 对此
	// 没有任何兜底。
	serialWriteTimeout = 5 * time.Second
	// serialMaxReadBytes 与 ability 层的读上限 (1MiB) 对齐, 防止 length 直接
	// 驱动一次超大分配。
	serialMaxReadBytes = 1 << 20
)

// errSerialClosed 表示设备 Close 之后仍在读写。
var errSerialClosed = errors.New("serial device is closed")

// serialDevice 是平台相关的真实串口句柄。
type serialDevice interface {
	// Read 最多读 length 字节: 先等 timeout 让首字节到达, 再用一段空闲窗口
	// 收敛后续字节; timeout<=0 表示只看已缓冲数据。
	Read(length int, timeout time.Duration) ([]byte, error)
	Write(data []byte) (int, error)
	// ApplyConfig 把配置真正下发到设备 (SetCommState / termios ioctl),
	// 而不是只记在本进程里。
	ApplyConfig(cfg ability.SerialConfig) error
	Close() error
}

// serialTransport 实现 ability.SerialTransport 与 ability.SerialPortLister。
type serialTransport struct {
	mu    sync.Mutex
	ports map[string]serialDevice
}

func newSerialTransport() *serialTransport {
	return &serialTransport{ports: make(map[string]serialDevice)}
}

// Open 打开端口并按 cfg 初始化设备。
func (t *serialTransport) Open(port string, cfg ability.SerialConfig) error {
	if port == "" {
		return fmt.Errorf("open: empty port")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.ports[port]; ok {
		return fmt.Errorf("open %s: already open", port)
	}
	dev, err := openSerialDevice(port, cfg)
	if err != nil {
		return err
	}
	t.ports[port] = dev
	return nil
}

// device 取出已打开的设备句柄; 未打开时返回明确 error。
func (t *serialTransport) device(op, port string) (serialDevice, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	dev, ok := t.ports[port]
	if !ok {
		return nil, fmt.Errorf("%s %s: port is not open", op, port)
	}
	return dev, nil
}

// Close 关闭端口并释放句柄; 重复 Close 返回明确 error。
func (t *serialTransport) Close(port string) error {
	t.mu.Lock()
	dev, ok := t.ports[port]
	if ok {
		delete(t.ports, port)
	}
	t.mu.Unlock()
	if !ok {
		return fmt.Errorf("close %s: port is not open", port)
	}
	return dev.Close()
}

// Write 把 data 全部写入端口。
func (t *serialTransport) Write(port string, data []byte) (int, error) {
	if len(data) == 0 {
		return 0, fmt.Errorf("write %s: empty payload", port)
	}
	dev, err := t.device("write", port)
	if err != nil {
		return 0, err
	}
	return dev.Write(data)
}

// Read 读取至多 length 字节; 超时返回已读到的部分 (可能为空切片)。
func (t *serialTransport) Read(port string, length int, timeout time.Duration) ([]byte, error) {
	if length <= 0 {
		return nil, fmt.Errorf("read %s: length must be positive", port)
	}
	if length > serialMaxReadBytes {
		return nil, fmt.Errorf("read %s: length %d exceeds max %d", port, length, serialMaxReadBytes)
	}
	if timeout < 0 {
		return nil, fmt.Errorf("read %s: negative timeout %s", port, timeout)
	}
	dev, err := t.device("read", port)
	if err != nil {
		return nil, err
	}
	return dev.Read(length, timeout)
}

// ApplyConfig 把新配置下发到已打开的设备 (set_config 的真实落点)。
func (t *serialTransport) ApplyConfig(port string, cfg ability.SerialConfig) error {
	dev, err := t.device("set_config", port)
	if err != nil {
		return err
	}
	return dev.ApplyConfig(cfg)
}

// ListPorts 枚举本机串口设备。
func (t *serialTransport) ListPorts() ([]string, error) { return listSerialPorts() }

// 编译期断言: 注入到 SerialAbility 的正是它期望的两个接口。
var (
	_ ability.SerialTransport  = (*serialTransport)(nil)
	_ ability.SerialPortLister = (*serialTransport)(nil)
)
