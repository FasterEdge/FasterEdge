package ability

import (
	"errors"
	"testing"
	"time"

	"github.com/FasterEdge/FasterEdge/data"
	"github.com/FasterEdge/FasterEdge/types"
)

func newTSNAtom(t *testing.T) *types.Atom {
	t.Helper()
	a := &types.Atom{}
	if err := a.AddData(&data.BaseData{}); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestTSNAbilityRejectsMissingDeps(t *testing.T) {
	tn := NewTSNAbility()
	if out := tn.Command(&types.Atom{}, TSNCommandGetInterface, nil); !errors.Is(out.Err, types.ErrMissingDependency) {
		t.Fatalf("missing deps error = %v", out.Err)
	}
}

func TestTSNAbilitySetGetInterface(t *testing.T) {
	tn := NewTSNAbility()
	atom := newTSNAtom(t)
	if out := tn.Command(atom, TSNCommandSetInterface, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set wrong type error = %v", out.Err)
	}
	if out := tn.Command(atom, TSNCommandSetInterface, TSNInterfaceArg{Interface: ""}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set empty error = %v", out.Err)
	}
	if out := tn.Command(atom, TSNCommandSetInterface, TSNInterfaceArg{Interface: "eth0"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := tn.Command(atom, TSNCommandGetInterface, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get with args error = %v", out.Err)
	}
	if out := tn.Command(atom, TSNCommandGetInterface, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if out.Value != "eth0" {
		t.Fatalf("interface = %q", out.Value)
	}
}

func TestTSNAbilityRegisterTalker(t *testing.T) {
	tn := NewTSNAbility()
	atom := newTSNAtom(t)
	// 类型错误
	if out := tn.Command(atom, TSNCommandRegisterTalker, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("register wrong type error = %v", out.Err)
	}
	// 缺 id/mac
	if out := tn.Command(atom, TSNCommandRegisterTalker, TSNRegisterTalkerArgs{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("register empty error = %v", out.Err)
	}
	// 非法 MAC
	if out := tn.Command(atom, TSNCommandRegisterTalker, TSNRegisterTalkerArgs{
		ID: "s1", MAC: "bad", Interval: time.Millisecond,
	}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("register bad mac error = %v", out.Err)
	}
	// 非法 dest MAC
	if out := tn.Command(atom, TSNCommandRegisterTalker, TSNRegisterTalkerArgs{
		ID: "s1", MAC: "00:11:22:33:44:55", DestMAC: "bad", Interval: time.Millisecond,
	}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("register bad dest error = %v", out.Err)
	}
	// interval <= 0
	if out := tn.Command(atom, TSNCommandRegisterTalker, TSNRegisterTalkerArgs{
		ID: "s1", MAC: "00:11:22:33:44:55",
	}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("register no interval error = %v", out.Err)
	}
	// priority > 7
	if out := tn.Command(atom, TSNCommandRegisterTalker, TSNRegisterTalkerArgs{
		ID: "s1", MAC: "00:11:22:33:44:55", Priority: 10, Interval: time.Millisecond,
	}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("register bad priority error = %v", out.Err)
	}
	// 正常
	if out := tn.Command(atom, TSNCommandRegisterTalker, TSNRegisterTalkerArgs{
		ID: "s1", MAC: "00:11:22:33:44:55", DestMAC: "AA:BB:CC:DD:EE:FF",
		VLANID: 100, Priority: 5, PayloadLen: 64, Interval: 500 * time.Microsecond,
	}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// 重复
	if out := tn.Command(atom, TSNCommandRegisterTalker, TSNRegisterTalkerArgs{
		ID: "s1", MAC: "00:11:22:33:44:55", Interval: time.Millisecond,
	}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("register duplicate error = %v", out.Err)
	}
}

func TestTSNAbilityRegisterListener(t *testing.T) {
	tn := NewTSNAbility()
	atom := newTSNAtom(t)
	if out := tn.Command(atom, TSNCommandRegisterListener, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("register wrong type error = %v", out.Err)
	}
	if out := tn.Command(atom, TSNCommandRegisterListener, TSNRegisterListenerArgs{
		ID: "l1", MAC: "AA:BB:CC:DD:EE:FF", VLANID: 100, Priority: 3,
	}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := tn.Command(atom, TSNCommandRegisterListener, TSNRegisterListenerArgs{ID: "l1", MAC: "AA:BB:CC:DD:EE:FF"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("register duplicate error = %v", out.Err)
	}
}

func TestTSNAbilityUnregisterAndList(t *testing.T) {
	tn := NewTSNAbility()
	atom := newTSNAtom(t)
	if out := tn.Command(atom, TSNCommandRegisterTalker, TSNRegisterTalkerArgs{
		ID: "s1", MAC: "00:11:22:33:44:55", Interval: time.Millisecond,
	}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := tn.Command(atom, TSNCommandUnregister, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("unregister wrong type error = %v", out.Err)
	}
	if out := tn.Command(atom, TSNCommandUnregister, TSNStreamIDArg{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("unregister empty error = %v", out.Err)
	}
	if out := tn.Command(atom, TSNCommandUnregister, TSNStreamIDArg{ID: "missing"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("unregister missing error = %v", out.Err)
	}
	if out := tn.Command(atom, TSNCommandUnregister, TSNStreamIDArg{ID: "s1"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := tn.Command(atom, TSNCommandUnregister, TSNStreamIDArg{ID: "s1"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("unregister re error = %v", out.Err)
	}
	// list
	if out := tn.Command(atom, TSNCommandListStreams, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("list with args error = %v", out.Err)
	}
	if out := tn.Command(atom, TSNCommandListStreams, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if list, _ := out.Value.([]TSNStream); len(list) != 0 {
		t.Fatalf("list after unregister = %v", list)
	}
}

func TestTSNAbilityPriorityMap(t *testing.T) {
	tn := NewTSNAbility()
	atom := newTSNAtom(t)
	// 类型错误
	if out := tn.Command(atom, TSNCommandSetPriority, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set wrong type error = %v", out.Err)
	}
	// 越界
	if out := tn.Command(atom, TSNCommandSetPriority, TSNPriorityMapArgs{Mappings: map[uint8]uint8{8: 0}}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("priority out of range error = %v", out.Err)
	}
	if out := tn.Command(atom, TSNCommandSetPriority, TSNPriorityMapArgs{Mappings: map[uint8]uint8{0: 9}}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("queue out of range error = %v", out.Err)
	}
	// 正常
	mapping := map[uint8]uint8{0: 0, 3: 7, 5: 2}
	if out := tn.Command(atom, TSNCommandSetPriority, TSNPriorityMapArgs{Mappings: mapping}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// get
	if out := tn.Command(atom, TSNCommandGetPriority, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get with args error = %v", out.Err)
	}
	if out := tn.Command(atom, TSNCommandGetPriority, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if got, _ := out.Value.(map[uint8]uint8); got[3] != 7 || got[5] != 2 {
		t.Fatalf("mapping = %v", got)
	}
}

func TestTSNAbilityTimeAware(t *testing.T) {
	tn := NewTSNAbility()
	atom := newTSNAtom(t)
	// 类型错误
	if out := tn.Command(atom, TSNCommandSetTimeAware, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set wrong type error = %v", out.Err)
	}
	// 负 cycle
	if out := tn.Command(atom, TSNCommandSetTimeAware, TSNTimeAwareArgs{Enabled: true, CycleTime: -1}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("negative cycle error = %v", out.Err)
	}
	// 正常
	base := time.Now()
	if out := tn.Command(atom, TSNCommandSetTimeAware, TSNTimeAwareArgs{
		Enabled: true, BaseTime: base, CycleTime: time.Millisecond, GateStates: []byte{0xFF, 0x00},
	}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := tn.Command(atom, TSNCommandGetTimeAware, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get with args error = %v", out.Err)
	}
	if out := tn.Command(atom, TSNCommandGetTimeAware, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if got, _ := out.Value.(TSNTimeAwareArgs); !got.Enabled || got.CycleTime != time.Millisecond {
		t.Fatalf("time aware = %+v", got)
	}
}

func TestTSNIsValidMAC(t *testing.T) {
	for _, ok := range []string{"00:11:22:33:44:55", "AA:BB:CC:DD:EE:FF", "0a:0b:0c:0d:0e:0f"} {
		if !isValidMAC(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "00:11:22:33:44", "00:11:22:33:44:55:66", "00-11-22-33-44-55", "xx:yy:zz:11:22:33"} {
		if isValidMAC(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestTSNAbilityUnknownCommand(t *testing.T) {
	tn := NewTSNAbility()
	atom := newTSNAtom(t)
	if out := tn.Command(atom, "nope", nil); !errors.Is(out.Err, types.ErrUnsupportedCommand) {
		t.Fatalf("unknown error = %v", out.Err)
	}
}
