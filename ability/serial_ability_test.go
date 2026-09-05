package ability

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/FasterEdge/FasterEdge/data"
	"github.com/FasterEdge/FasterEdge/types"
)

type fakeSerialTransport struct {
	mu       sync.Mutex
	opened   map[string]SerialConfig
	written  map[string][]byte
	readData map[string][]byte
	writeErr error
	readErr  error
	openErr  error
}

func newFakeSerialTransport() *fakeSerialTransport {
	return &fakeSerialTransport{
		opened:   make(map[string]SerialConfig),
		written:  make(map[string][]byte),
		readData: make(map[string][]byte),
	}
}

func (f *fakeSerialTransport) Open(port string, cfg SerialConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.openErr != nil {
		return f.openErr
	}
	f.opened[port] = cfg
	return nil
}

func (f *fakeSerialTransport) Close(port string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.opened, port)
	return nil
}

func (f *fakeSerialTransport) ApplyConfig(port string, cfg SerialConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened[port] = cfg
	return nil
}

func (f *fakeSerialTransport) Write(port string, data []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	f.written[port] = append(f.written[port], data...)
	return len(data), nil
}

func (f *fakeSerialTransport) Read(port string, length int, _ time.Duration) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readErr != nil {
		return nil, f.readErr
	}
	buf := f.readData[port]
	if len(buf) > length {
		buf = buf[:length]
	}
	return buf, nil
}

type fakeLister struct{ ports []string }

func (f fakeLister) ListPorts() ([]string, error) { return f.ports, nil }

func newSerialAtom(t *testing.T) *types.Atom {
	t.Helper()
	a := &types.Atom{}
	if err := a.AddData(&data.BaseData{}); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestSerialAbilityRejectsMissingDeps(t *testing.T) {
	s := NewSerialAbility()
	if out := s.Command(&types.Atom{}, SerialCommandListPorts, nil); !errors.Is(out.Err, types.ErrMissingDependency) {
		t.Fatalf("missing deps error = %v", out.Err)
	}
}

func TestSerialAbilityPortValidation(t *testing.T) {
	for _, ok := range []string{"/dev/ttyUSB0", "/dev/ttyS1", "COM1", "COM10"} {
		if !isValidSerialPort(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "ttyUSB0", "COM", "COMx", "COM0x10", "/dev/null"} {
		if isValidSerialPort(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestSerialAbilityConfigValidation(t *testing.T) {
	good := SerialConfig{Baud: 9600, DataBits: 8, StopBits: 1, Parity: "N"}
	if !isValidSerialConfig(good) {
		t.Fatal("good config should validate")
	}
	for _, bad := range []SerialConfig{
		{Baud: 1000, DataBits: 8, StopBits: 1, Parity: "N"},
		{Baud: 9600, DataBits: 4, StopBits: 1, Parity: "N"},
		{Baud: 9600, DataBits: 8, StopBits: 3, Parity: "N"},
		{Baud: 9600, DataBits: 8, StopBits: 1, Parity: "X"},
	} {
		if isValidSerialConfig(bad) {
			t.Errorf("bad config should reject: %+v", bad)
		}
	}
}

func TestSerialAbilityOpenClose(t *testing.T) {
	s := NewSerialAbility()
	atom := newSerialAtom(t)
	ft := newFakeSerialTransport()
	s.SetTransport(ft)
	// 类型错误
	if out := s.Command(atom, SerialCommandOpen, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("open wrong type error = %v", out.Err)
	}
	// 非法端口
	if out := s.Command(atom, SerialCommandOpen, SerialOpenArgs{Port: "bogus", Config: SerialConfig{Baud: 9600, DataBits: 8, StopBits: 1, Parity: "N"}}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("open bad port error = %v", out.Err)
	}
	// 非法 config
	if out := s.Command(atom, SerialCommandOpen, SerialOpenArgs{Port: "/dev/ttyUSB0", Config: SerialConfig{Baud: 1, DataBits: 8, StopBits: 1, Parity: "N"}}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("open bad config error = %v", out.Err)
	}
	// 正常 open
	if out := s.Command(atom, SerialCommandOpen, SerialOpenArgs{Port: "/dev/ttyUSB0", Config: SerialConfig{Baud: 9600, DataBits: 8, StopBits: 1, Parity: "N"}}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// is_open
	if out := s.Command(atom, SerialCommandIsOpen, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("is_open wrong type error = %v", out.Err)
	}
	if out := s.Command(atom, SerialCommandIsOpen, SerialPortArg{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("is_open empty error = %v", out.Err)
	}
	if out := s.Command(atom, SerialCommandIsOpen, SerialPortArg{Port: "/dev/ttyUSB0"}); out.Err != nil {
		t.Fatal(out.Err)
	} else if v, _ := out.Value.(bool); !v {
		t.Fatal("expected is_open = true")
	}
	if out := s.Command(atom, SerialCommandIsOpen, SerialPortArg{Port: "/dev/ttyUSB1"}); out.Err != nil {
		t.Fatal(out.Err)
	} else if v, _ := out.Value.(bool); v {
		t.Fatal("expected is_open = false")
	}
	// get_config
	if out := s.Command(atom, SerialCommandGetConfig, SerialPortArg{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get_config empty error = %v", out.Err)
	}
	if out := s.Command(atom, SerialCommandGetConfig, SerialPortArg{Port: "/dev/ttyUSB0"}); out.Err != nil {
		t.Fatal(out.Err)
	} else if cfg, _ := out.Value.(SerialConfig); cfg.Baud != 9600 {
		t.Fatalf("config = %+v", cfg)
	}
	if out := s.Command(atom, SerialCommandGetConfig, SerialPortArg{Port: "/dev/ttyUSB1"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get_config not open error = %v", out.Err)
	}
	// set_config
	if out := s.Command(atom, SerialCommandSetConfig, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set_config wrong type error = %v", out.Err)
	}
	if out := s.Command(atom, SerialCommandSetConfig, SerialSetConfigArgs{Port: "bogus"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set_config bad port error = %v", out.Err)
	}
	if out := s.Command(atom, SerialCommandSetConfig, SerialSetConfigArgs{Port: "/dev/ttyUSB0", Config: SerialConfig{Baud: 1, DataBits: 8, StopBits: 1, Parity: "N"}}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set_config bad config error = %v", out.Err)
	}
	if out := s.Command(atom, SerialCommandSetConfig, SerialSetConfigArgs{Port: "/dev/ttyUSB1", Config: SerialConfig{Baud: 115200, DataBits: 8, StopBits: 1, Parity: "N"}}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set_config not open error = %v", out.Err)
	}
	if out := s.Command(atom, SerialCommandSetConfig, SerialSetConfigArgs{Port: "/dev/ttyUSB0", Config: SerialConfig{Baud: 115200, DataBits: 8, StopBits: 1, Parity: "E"}}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// close
	if out := s.Command(atom, SerialCommandClose, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("close wrong type error = %v", out.Err)
	}
	if out := s.Command(atom, SerialCommandClose, SerialPortArg{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("close empty error = %v", out.Err)
	}
	if out := s.Command(atom, SerialCommandClose, SerialPortArg{Port: "/dev/ttyUSB1"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("close not open error = %v", out.Err)
	}
	if out := s.Command(atom, SerialCommandClose, SerialPortArg{Port: "/dev/ttyUSB0"}); out.Err != nil {
		t.Fatal(out.Err)
	}
}

func TestSerialAbilityReadWrite(t *testing.T) {
	s := NewSerialAbility()
	atom := newSerialAtom(t)
	ft := newFakeSerialTransport()
	s.SetTransport(ft)
	// open
	if out := s.Command(atom, SerialCommandOpen, SerialOpenArgs{
		Port:   "/dev/ttyUSB0",
		Config: SerialConfig{Baud: 9600, DataBits: 8, StopBits: 1, Parity: "N"},
	}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// write
	if out := s.Command(atom, SerialCommandWrite, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("write wrong type error = %v", out.Err)
	}
	if out := s.Command(atom, SerialCommandWrite, SerialWriteArgs{Port: "bogus", Data: []byte{1}}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("write bad port error = %v", out.Err)
	}
	if out := s.Command(atom, SerialCommandWrite, SerialWriteArgs{Port: "/dev/ttyUSB0", Data: nil}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("write nil data error = %v", out.Err)
	}
	if out := s.Command(atom, SerialCommandWrite, SerialWriteArgs{Port: "/dev/ttyUSB1", Data: []byte{1}}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("write not open error = %v", out.Err)
	}
	if out := s.Command(atom, SerialCommandWrite, SerialWriteArgs{Port: "/dev/ttyUSB0", Data: []byte("hello"), Length: 3}); out.Err != nil {
		t.Fatal(out.Err)
	} else if n, _ := out.Value.(int); n != 3 {
		t.Fatalf("write n = %d, want 3", n)
	}
	ft.mu.Lock()
	if string(ft.written["/dev/ttyUSB0"]) != "hel" {
		t.Fatalf("written = %q", ft.written["/dev/ttyUSB0"])
	}
	ft.mu.Unlock()
	// read
	if out := s.Command(atom, SerialCommandRead, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("read wrong type error = %v", out.Err)
	}
	if out := s.Command(atom, SerialCommandRead, SerialReadArgs{Port: "/dev/ttyUSB0", Length: 0}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("read length 0 error = %v", out.Err)
	}
	if out := s.Command(atom, SerialCommandRead, SerialReadArgs{Port: "/dev/ttyUSB0", Length: 4, Timeout: -1}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("read negative timeout error = %v", out.Err)
	}
	if out := s.Command(atom, SerialCommandRead, SerialReadArgs{Port: "bogus", Length: 4}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("read bad port error = %v", out.Err)
	}
	// 准备读取数据
	ft.readData["/dev/ttyUSB0"] = []byte("0123456789")
	if out := s.Command(atom, SerialCommandRead, SerialReadArgs{Port: "/dev/ttyUSB0", Length: 4}); out.Err != nil {
		t.Fatal(out.Err)
	} else if buf, _ := out.Value.([]byte); string(buf) != "0123" {
		t.Fatalf("read = %q", buf)
	}
}

func TestSerialAbilityListPorts(t *testing.T) {
	s := NewSerialAbility()
	atom := newSerialAtom(t)
	// 类型错误
	if out := s.Command(atom, SerialCommandListPorts, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("list with args error = %v", out.Err)
	}
	// 默认 lister → 空
	if out := s.Command(atom, SerialCommandListPorts, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if ports, _ := out.Value.([]string); len(ports) != 0 {
		t.Fatalf("default list = %v", ports)
	}
	// 自定义 lister
	s.SetLister(fakeLister{ports: []string{"/dev/ttyUSB0", "/dev/ttyUSB1"}})
	if out := s.Command(atom, SerialCommandListPorts, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if ports, _ := out.Value.([]string); len(ports) != 2 {
		t.Fatalf("custom list = %v", ports)
	}
	// 注入错误的 lister
	s.SetLister(errorLister{})
	if out := s.Command(atom, SerialCommandListPorts, nil); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("lister error = %v", out.Err)
	}
}

type errorLister struct{}

func (errorLister) ListPorts() ([]string, error) { return nil, errors.New("nope") }

func TestSerialAbilityNoTransport(t *testing.T) {
	s := NewSerialAbility()
	atom := newSerialAtom(t)
	if out := s.Command(atom, SerialCommandOpen, SerialOpenArgs{
		Port: "/dev/ttyUSB0", Config: SerialConfig{Baud: 9600, DataBits: 8, StopBits: 1, Parity: "N"},
	}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("open no transport error = %v", out.Err)
	}
}

func TestSerialAbilityUnknownCommand(t *testing.T) {
	s := NewSerialAbility()
	atom := newSerialAtom(t)
	if out := s.Command(atom, "nope", nil); !errors.Is(out.Err, types.ErrUnsupportedCommand) {
		t.Fatalf("unknown error = %v", out.Err)
	}
}
