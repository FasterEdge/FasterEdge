// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package ability

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/FasterEdge/FasterEdge/types"
)

const (
	SerialCommandOpen      = "open"
	SerialCommandClose     = "close"
	SerialCommandWrite     = "write"
	SerialCommandRead      = "read"
	SerialCommandIsOpen    = "is_open"
	SerialCommandSetConfig = "set_config"
	SerialCommandGetConfig = "get_config"
	SerialCommandListPorts = "list_ports"
)

// SerialConfig 描述串口参数。
type SerialConfig struct {
	Baud     int
	DataBits int
	StopBits int
	Parity   string // "N" | "O" | "E"
}

// SerialOpenArgs 是 open 命令的参数。
type SerialOpenArgs struct {
	Port   string
	Config SerialConfig
}

// SerialPortArg 是 close / is_open 的参数。
type SerialPortArg struct {
	Port string
}

// SerialWriteArgs 是 write 命令的参数。
type SerialWriteArgs struct {
	Port   string
	Data   []byte
	Length int // 0 表示全部
}

// SerialReadArgs 是 read 命令的参数。
type SerialReadArgs struct {
	Port    string
	Length  int
	Timeout time.Duration
}

// SerialSetConfigArgs 是 set_config 命令的参数。
type SerialSetConfigArgs struct {
	Port   string
	Config SerialConfig
}

// SerialTransport 抽象出真实串口字节流收发。
// Open 打开并按配置初始化,Close 释放资源,Write/Read 完成字节流读写,
// ApplyConfig 把新配置下发到已打开的端口(参数变更真正生效)。
type SerialTransport interface {
	Open(port string, cfg SerialConfig) error
	Close(port string) error
	Write(port string, data []byte) (int, error)
	Read(port string, length int, timeout time.Duration) ([]byte, error)
	ApplyConfig(port string, cfg SerialConfig) error
}

// SerialPortLister 抽象出列举可用串口设备(默认实现枚举 /dev/tty* 等)。
type SerialPortLister interface {
	ListPorts() ([]string, error)
}

// SerialAbility 在 Transport 之上提供串口管理。
type SerialAbility struct {
	mu        sync.RWMutex
	openPorts map[string]SerialConfig
	transport SerialTransport
	lister    SerialPortLister
	closing   bool // Unmount 置位后拒绝 open/set_config/close
}

const (
	// maxSerialReadBytes 是 read 命令单次读取的字节上限(对齐 CmdAbility
	// 1MiB 输出上限): 旧实现 Length 无上限, 真实 transport 按 length 分配
	// 缓冲时, 已认证对端可传 Length=MaxInt 造成内存 DoS。
	maxSerialReadBytes = 1 << 20
)

func NewSerialAbility() *SerialAbility {
	return &SerialAbility{openPorts: make(map[string]SerialConfig)}
}

func (s *SerialAbility) SetTransport(t SerialTransport) {
	s.mu.Lock()
	s.transport = t
	s.mu.Unlock()
}

func (s *SerialAbility) SetLister(l SerialPortLister) {
	s.mu.Lock()
	s.lister = l
	s.mu.Unlock()
}

func (s *SerialAbility) GetName() string { return "SerialAbility" }

func (s *SerialAbility) Describe() string {
	return "SerialAbility提供串口管理能力:open/close/read/write,字节流通过注入的 SerialTransport 完成;默认提供 tty* 风格的 PortLister。"
}

func (s *SerialAbility) Check(atom *types.Atom) error {
	if atom == nil {
		return types.ErrMissingDependency
	}
	if _, ok := atom.Data("BaseData"); !ok {
		return types.ErrMissingDependency
	}
	return nil
}

func (s *SerialAbility) Mount(atom *types.Atom) error {
	if s.lister == nil {
		s.SetLister(defaultPortLister{})
	}
	return s.Check(atom)
}

// Unmount 关闭所有已打开端口并清空记录, 防止 atom 拆除/回滚时串口 fd 泄漏
// (旧实现未实现 Unmounter, 框架永不调用 Close)。closing 标志在快照前置位,
// 此后并发 open 被拒绝, 保证快照覆盖全部端口——旧实现快照后锁外 Close
// 期间新 open 写入的端口既不被 Close 也不被 delete, fd 永久泄漏。
func (s *SerialAbility) Unmount(ctx context.Context, atom *types.Atom) error {
	s.mu.Lock()
	s.closing = true
	ports := make([]string, 0, len(s.openPorts))
	for p := range s.openPorts {
		ports = append(ports, p)
	}
	transport := s.transport
	s.mu.Unlock()
	var errs []error
	for _, p := range ports {
		if transport != nil {
			if err := transport.Close(p); err != nil {
				errs = append(errs, err)
			}
		}
		s.mu.Lock()
		delete(s.openPorts, p)
		s.mu.Unlock()
	}
	return errors.Join(errs...)
}

// Compile-time guarantee that SerialAbility satisfies Unmounter.
var _ types.Unmounter = (*SerialAbility)(nil)

func (s *SerialAbility) Command(atom *types.Atom, act string, args any) types.CommandOutput {
	if err := s.Check(atom); err != nil {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, err)}
	}
	switch act {
	case SerialCommandOpen:
		typed, ok := args.(SerialOpenArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		port := strings.TrimSpace(typed.Port)
		if !isValidSerialPort(port) {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: invalid port %q: %w", act, port, types.ErrInvalidArguments)}
		}
		if !isValidSerialConfig(typed.Config) {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: invalid config: %w", act, types.ErrInvalidArguments)}
		}
		s.mu.Lock()
		transport := s.transport
		closing := s.closing
		_, alreadyOpen := s.openPorts[port]
		s.mu.Unlock()
		if closing {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: ability is closing: %w", act, types.ErrInvalidArguments)}
		}
		if alreadyOpen {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: port %q already open: %w", act, port, types.ErrInvalidArguments)}
		}
		if transport == nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no transport set: %w", act, types.ErrInvalidArguments)}
		}
		if err := transport.Open(port, typed.Config); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: open: %w: %v", act, types.ErrInvalidArguments, err)}
		}
		s.mu.Lock()
		// 双开防护: 两个并发 open 同端口时 transport.Open 可双双成功,
		// 但 map 只留一份——后到者回滚关闭自身连接, 防首个 fd 无人管理。
		if _, dup := s.openPorts[port]; dup || s.closing {
			s.mu.Unlock()
			_ = transport.Close(port)
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: port %q already open or closing: %w", act, port, types.ErrInvalidArguments)}
		}
		s.openPorts[port] = typed.Config
		s.mu.Unlock()
		return types.CommandOutput{Name: act, Value: port}
	case SerialCommandClose:
		typed, ok := args.(SerialPortArg)
		if !ok || strings.TrimSpace(typed.Port) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		port := strings.TrimSpace(typed.Port)
		s.mu.Lock()
		transport := s.transport
		_, open := s.openPorts[port]
		closing := s.closing
		s.mu.Unlock()
		if !open {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: port %q not open: %w", act, port, types.ErrInvalidArguments)}
		}
		if closing {
			// Unmount 已接管清理, 拒绝并发 close(避免与 Unmount 的
			// 快照后 Close 竞态)。
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: ability is closing: %w", act, types.ErrInvalidArguments)}
		}
		if transport != nil {
			if err := transport.Close(port); err != nil {
				return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: close: %w: %v", act, types.ErrInvalidArguments, err)}
			}
		}
		s.mu.Lock()
		delete(s.openPorts, port)
		s.mu.Unlock()
		return types.CommandOutput{Name: act, Value: port}
	case SerialCommandIsOpen:
		typed, ok := args.(SerialPortArg)
		if !ok || strings.TrimSpace(typed.Port) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		s.mu.RLock()
		defer s.mu.RUnlock()
		_, ok = s.openPorts[strings.TrimSpace(typed.Port)]
		return types.CommandOutput{Name: act, Value: ok}
	case SerialCommandWrite:
		typed, ok := args.(SerialWriteArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		port := strings.TrimSpace(typed.Port)
		if !isValidSerialPort(port) || len(typed.Data) == 0 {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		data := typed.Data
		if typed.Length > 0 && typed.Length < len(data) {
			data = data[:typed.Length]
		}
		s.mu.RLock()
		transport := s.transport
		_, open := s.openPorts[port]
		s.mu.RUnlock()
		if !open {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: port %q not open: %w", act, port, types.ErrInvalidArguments)}
		}
		if transport == nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no transport: %w", act, types.ErrInvalidArguments)}
		}
		n, err := transport.Write(port, data)
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: write: %w: %v", act, types.ErrInvalidArguments, err)}
		}
		return types.CommandOutput{Name: act, Value: n}
	case SerialCommandRead:
		typed, ok := args.(SerialReadArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		if typed.Length <= 0 {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: length must be positive: %w", act, types.ErrInvalidArguments)}
		}
		if typed.Length > maxSerialReadBytes {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: length exceeds max %d: %w", act, maxSerialReadBytes, types.ErrInvalidArguments)}
		}
		if typed.Timeout < 0 {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: timeout must be non-negative: %w", act, types.ErrInvalidArguments)}
		}
		port := strings.TrimSpace(typed.Port)
		if !isValidSerialPort(port) {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: invalid port: %w", act, types.ErrInvalidArguments)}
		}
		s.mu.RLock()
		transport := s.transport
		_, open := s.openPorts[port]
		s.mu.RUnlock()
		if !open {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: port %q not open: %w", act, port, types.ErrInvalidArguments)}
		}
		if transport == nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no transport: %w", act, types.ErrInvalidArguments)}
		}
		buf, err := transport.Read(port, typed.Length, typed.Timeout)
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: read: %w: %v", act, types.ErrInvalidArguments, err)}
		}
		return types.CommandOutput{Name: act, Value: buf}
	case SerialCommandSetConfig:
		typed, ok := args.(SerialSetConfigArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		port := strings.TrimSpace(typed.Port)
		if !isValidSerialPort(port) || !isValidSerialConfig(typed.Config) {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		s.mu.Lock()
		_, open := s.openPorts[port]
		transport := s.transport
		closing := s.closing
		s.mu.Unlock()
		if !open {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: port %q not open: %w", act, port, types.ErrInvalidArguments)}
		}
		if closing {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: ability is closing: %w", act, types.ErrInvalidArguments)}
		}
		// 配置必须真正下发到 transport, 否则新波特率等参数静默失效
		// (旧实现只更新本地记录, 硬件仍按旧配置运行)。
		if transport != nil {
			if err := transport.ApplyConfig(port, typed.Config); err != nil {
				return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: apply config: %w: %v", act, types.ErrInvalidArguments, err)}
			}
		}
		s.mu.Lock()
		s.openPorts[port] = typed.Config
		s.mu.Unlock()
		return types.CommandOutput{Name: act, Value: typed.Config}
	case SerialCommandGetConfig:
		typed, ok := args.(SerialPortArg)
		if !ok || strings.TrimSpace(typed.Port) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		port := strings.TrimSpace(typed.Port)
		s.mu.RLock()
		cfg, open := s.openPorts[port]
		s.mu.RUnlock()
		if !open {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: port %q not open: %w", act, port, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: cfg}
	case SerialCommandListPorts:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		s.mu.RLock()
		lister := s.lister
		s.mu.RUnlock()
		if lister == nil {
			lister = defaultPortLister{}
		}
		ports, err := lister.ListPorts()
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: list: %w: %v", act, types.ErrInvalidArguments, err)}
		}
		return types.CommandOutput{Name: act, Value: ports}
	}
	return types.CommandOutput{Name: act, Err: fmt.Errorf("command %s: %w", act, types.ErrUnsupportedCommand)}
}

// isValidSerialPort 接受 /dev/tty*、COM1..COM255(Windows) 等。
func isValidSerialPort(port string) bool {
	if port == "" {
		return false
	}
	if strings.HasPrefix(port, "/dev/tty") {
		return true
	}
	upper := strings.ToUpper(port)
	if strings.HasPrefix(upper, "COM") && len(upper) > 3 {
		for _, r := range upper[3:] {
			if r < '0' || r > '9' {
				return false
			}
		}
		return true
	}
	return false
}

func isValidSerialConfig(c SerialConfig) bool {
	switch c.Baud {
	case 1200, 2400, 4800, 9600, 19200, 38400, 57600, 115200, 230400, 460800, 921600:
	default:
		return false
	}
	if c.DataBits != 5 && c.DataBits != 6 && c.DataBits != 7 && c.DataBits != 8 {
		return false
	}
	if c.StopBits != 1 && c.StopBits != 2 {
		return false
	}
	switch c.Parity {
	case "N", "O", "E", "n", "o", "e":
	default:
		return false
	}
	return true
}

// defaultPortLister 是 SerialPortLister 的默认实现:返回空列表(避免跨平台枚举差异)。
// 用户可通过 SetLister 注入真实实现(读取 /dev/tty* 或注册表)。
type defaultPortLister struct{}

func (defaultPortLister) ListPorts() ([]string, error) { return []string{}, nil }
