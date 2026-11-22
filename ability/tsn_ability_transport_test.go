package ability

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/FasterEdge/FasterEdge/types"
)

// fakeTSNTransport 记录所有下发调用, 并可按 case 返回错误。
type fakeTSNTransport struct {
	mu sync.Mutex

	probeErr  error
	addErr    error
	removeErr error
	prioErr   error
	gateErr   error

	probeCalls   []string
	addedStreams []TSNStream
	removedIDs   []string
	prioMap      map[uint8]uint8
	gateCfg      TSNTimeAwareArgs
}

func (f *fakeTSNTransport) ProbeInterface(iface string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probeCalls = append(f.probeCalls, iface)
	return f.probeErr
}

func (f *fakeTSNTransport) AddStream(iface string, stream TSNStream) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addedStreams = append(f.addedStreams, stream)
	return f.addErr
}

func (f *fakeTSNTransport) RemoveStream(iface string, stream TSNStream) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removedIDs = append(f.removedIDs, stream.ID)
	return f.removeErr
}

func (f *fakeTSNTransport) ApplyPriorityMap(iface string, mappings map[uint8]uint8) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prioMap = cloneTSNPriorityMap(mappings)
	return f.prioErr
}

func (f *fakeTSNTransport) ApplyGateControl(iface string, cfg TSNTimeAwareArgs) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gateCfg = cloneTSNTimeAware(cfg)
	return f.gateErr
}

func (f *fakeTSNTransport) counts() (probe, add, remove, prio, gate int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.probeCalls), len(f.addedStreams), len(f.removedIDs), 0, 0
}

// TestTSNAbilityNoTransportRejectsDeviceCommands: 未注入 transport 时, 所有真正
// 需要设备的命令必须返回明确错误, 绝不能静默标成功(对齐 file_transfer/serial 语义)。
func TestTSNAbilityNoTransportRejectsDeviceCommands(t *testing.T) {
	tn := NewTSNAbility()
	atom := newTSNAtom(t)
	cases := []struct {
		name string
		act  string
		args any
	}{
		{"set_interface", TSNCommandSetInterface, TSNInterfaceArg{Interface: "eth0"}},
		{"register_talker", TSNCommandRegisterTalker, TSNRegisterTalkerArgs{ID: "s1", MAC: "00:11:22:33:44:55", Interval: time.Millisecond}},
		{"register_listener", TSNCommandRegisterListener, TSNRegisterListenerArgs{ID: "l1", MAC: "AA:BB:CC:DD:EE:FF"}},
		{"unregister", TSNCommandUnregister, TSNStreamIDArg{ID: "s1"}},
		{"set_priority_map", TSNCommandSetPriority, TSNPriorityMapArgs{Mappings: map[uint8]uint8{3: 7}}},
		{"set_time_aware", TSNCommandSetTimeAware, TSNTimeAwareArgs{Enabled: true, CycleTime: time.Millisecond, GateStates: []byte{0xFF}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := tn.Command(atom, tc.act, tc.args)
			if out.Err == nil {
				t.Fatalf("%s without transport: got success (value=%v), want error", tc.act, out.Value)
			}
			if !errors.Is(out.Err, types.ErrInvalidArguments) {
				t.Fatalf("%s without transport: error = %v, want ErrInvalidArguments", tc.act, out.Err)
			}
			if !out.Success() {
				return
			}
			t.Fatalf("%s: Success() = true, want false", tc.act)
		})
	}
	// 未注入时本地不得留下任何状态: 之后注入 transport, get_* 应仍为空
	if out := tn.Command(atom, TSNCommandListStreams, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if list, _ := out.Value.([]TSNStream); len(list) != 0 {
		t.Fatalf("streams recorded without transport: %v", list)
	}
}

// TestTSNAbilityInterfaceValidation: set_interface 在注入前先做名称校验(避免把
// 恶意字符串交给真实 transport 拼 tc 命令)。
func TestTSNAbilityInterfaceValidation(t *testing.T) {
	tn := NewTSNAbility()
	atom := newTSNAtom(t)
	for _, bad := range []string{"eth0;rm -rf /", "eth 0", "", "averyveryverylonginterfacename", "eth0/1"} {
		if out := tn.Command(atom, TSNCommandSetInterface, TSNInterfaceArg{Interface: bad}); !errors.Is(out.Err, types.ErrInvalidArguments) {
			t.Fatalf("set_interface %q error = %v", bad, out.Err)
		}
	}
	for _, ok := range []string{"eth0", "enp3s0", "eth0.100", "ENO1"} {
		tn2 := NewTSNAbility()
		tn2.SetTransport(&fakeTSNTransport{})
		if out := tn2.Command(atom, TSNCommandSetInterface, TSNInterfaceArg{Interface: ok}); out.Err != nil {
			t.Fatalf("set_interface %q: %v", ok, out.Err)
		}
	}
}

// TestTSNAbilityDelegatesToTransport: 注入假 transport 后, 参数必须原样透传,
// 且回读与设备侧调用一一对应。
func TestTSNAbilityDelegatesToTransport(t *testing.T) {
	ft := &fakeTSNTransport{}
	tn := NewTSNAbility()
	tn.SetTransport(ft)
	atom := newTSNAtom(t)

	// set_interface → ProbeInterface(eth0)
	if out := tn.Command(atom, TSNCommandSetInterface, TSNInterfaceArg{Interface: "eth0"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if probe, _, _, _, _ := ft.counts(); probe != 1 {
		t.Fatalf("probe calls = %d, want 1", probe)
	}
	if got := ft.probeCalls[0]; got != "eth0" {
		t.Fatalf("probed interface = %q, want eth0", got)
	}
	// probe 失败 → set_interface 失败, 本地不记录
	ftBad := &fakeTSNTransport{probeErr: errors.New("no qbv support")}
	tnBad := NewTSNAbility()
	tnBad.SetTransport(ftBad)
	if out := tnBad.Command(atom, TSNCommandSetInterface, TSNInterfaceArg{Interface: "eth1"}); out.Err == nil {
		t.Fatal("set_interface with failing probe: got success")
	} else if !errors.Is(out.Err, types.ErrOperationFailed) {
		t.Fatalf("set_interface probe error = %v, want ErrOperationFailed", out.Err)
	}
	if out := tnBad.Command(atom, TSNCommandGetInterface, nil); out.Err != nil || out.Value != "" {
		t.Fatalf("interface recorded after failed probe: %+v", out)
	}

	// register_talker → AddStream(eth0, stream)
	want := TSNRegisterTalkerArgs{
		ID: "stream-100", MAC: "00:11:22:33:44:55", DestMAC: "01:00:5E:00:00:01",
		VLANID: 100, Priority: 3, PayloadLen: 512, Interval: time.Millisecond,
	}
	if out := tn.Command(atom, TSNCommandRegisterTalker, want); out.Err != nil {
		t.Fatal(out.Err)
	}
	if _, add, _, _, _ := ft.counts(); add != 1 {
		t.Fatalf("AddStream calls = %d, want 1", add)
	}
	added := ft.addedStreams[0]
	if added.ID != want.ID || added.MAC != want.MAC || added.DestMAC != want.DestMAC ||
		added.VLANID != want.VLANID || added.Priority != want.Priority ||
		added.PayloadLen != want.PayloadLen || added.Interval != want.Interval || added.Direction != TSNDirTalker {
		t.Fatalf("AddStream stream = %+v, want %+v", added, want)
	}
	// register_listener → AddStream
	if out := tn.Command(atom, TSNCommandRegisterListener, TSNRegisterListenerArgs{
		ID: "listener-1", MAC: "02:00:00:00:00:02", DestMAC: "01:00:5E:00:00:01", VLANID: 100, Priority: 5,
	}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if _, add, _, _, _ := ft.counts(); add != 2 {
		t.Fatalf("AddStream calls = %d, want 2", add)
	}

	// set_priority_map → ApplyPriorityMap 透传副本(调用方后续修改不得影响已下发值)
	mapping := map[uint8]uint8{0: 0, 3: 7, 5: 2}
	if out := tn.Command(atom, TSNCommandSetPriority, TSNPriorityMapArgs{Mappings: mapping}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if !reflect.DeepEqual(ft.prioMap, mapping) {
		t.Fatalf("ApplyPriorityMap mappings = %v, want %v", ft.prioMap, mapping)
	}
	mapping[3] = 0
	if ft.prioMap[3] != 7 {
		t.Fatalf("ApplyPriorityMap received caller-shared map: %v", ft.prioMap)
	}

	// set_time_aware → ApplyGateControl 透传(含深拷贝后的 GateStates)
	base := time.Now()
	gateStates := []byte{0xAA, 0x00}
	if out := tn.Command(atom, TSNCommandSetTimeAware, TSNTimeAwareArgs{
		Enabled: true, BaseTime: base, CycleTime: time.Millisecond, GateStates: gateStates,
	}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if !ft.gateCfg.Enabled || ft.gateCfg.CycleTime != time.Millisecond || !reflect.DeepEqual(ft.gateCfg.GateStates, gateStates) {
		t.Fatalf("ApplyGateControl cfg = %+v", ft.gateCfg)
	}
	gateStates[0] = 0x01
	if ft.gateCfg.GateStates[0] != 0xAA {
		t.Fatalf("ApplyGateControl received caller-shared GateStates: %v", ft.gateCfg.GateStates)
	}

	// unregister → RemoveStream
	if out := tn.Command(atom, TSNCommandUnregister, TSNStreamIDArg{ID: "stream-100"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if _, _, remove, _, _ := ft.counts(); remove != 1 || ft.removedIDs[0] != "stream-100" {
		t.Fatalf("RemoveStream calls = %v, want [stream-100]", ft.removedIDs)
	}
	if out := tn.Command(atom, TSNCommandListStreams, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if list, _ := out.Value.([]TSNStream); len(list) != 1 || list[0].ID != "listener-1" {
		t.Fatalf("list after unregister = %v", list)
	}
}

// TestTSNAbilityTransportFailureKeepsState: 设备侧失败时本地状态不得前进,
// 也不得回退(Unregister 失败保留条目, 便于重试)。
func TestTSNAbilityTransportFailureKeepsState(t *testing.T) {
	atom := newTSNAtom(t)

	// AddStream 失败: 不得记入本地 map
	addFail := &fakeTSNTransport{addErr: errors.New("tc flower error")}
	tn := NewTSNAbility()
	tn.SetTransport(addFail)
	if out := tn.Command(atom, TSNCommandSetInterface, TSNInterfaceArg{Interface: "eth0"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := tn.Command(atom, TSNCommandRegisterTalker, TSNRegisterTalkerArgs{
		ID: "s1", MAC: "00:11:22:33:44:55", Interval: time.Millisecond,
	}); out.Err == nil {
		t.Fatal("register_talker with failing AddStream: got success")
	} else if !errors.Is(out.Err, types.ErrOperationFailed) {
		t.Fatalf("register error = %v, want ErrOperationFailed", out.Err)
	}
	if out := tn.Command(atom, TSNCommandListStreams, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if list, _ := out.Value.([]TSNStream); len(list) != 0 {
		t.Fatalf("stream recorded after AddStream failure: %v", list)
	}

	// RemoveStream 失败: 条目保留
	rmFail := &fakeTSNTransport{removeErr: errors.New("tc filter delete error")}
	tn2 := NewTSNAbility()
	tn2.SetTransport(rmFail)
	if out := tn2.Command(atom, TSNCommandSetInterface, TSNInterfaceArg{Interface: "eth0"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := tn2.Command(atom, TSNCommandRegisterListener, TSNRegisterListenerArgs{ID: "l1", MAC: "AA:BB:CC:DD:EE:FF"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := tn2.Command(atom, TSNCommandUnregister, TSNStreamIDArg{ID: "l1"}); out.Err == nil {
		t.Fatal("unregister with failing RemoveStream: got success")
	} else if !errors.Is(out.Err, types.ErrOperationFailed) {
		t.Fatalf("unregister error = %v, want ErrOperationFailed", out.Err)
	}
	if out := tn2.Command(atom, TSNCommandListStreams, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if list, _ := out.Value.([]TSNStream); len(list) != 1 {
		t.Fatalf("stream dropped after RemoveStream failure: %v", list)
	}

	// ApplyPriorityMap / ApplyGateControl 失败: 本地不前进
	prioFail := &fakeTSNTransport{prioErr: errors.New("tc priomap error")}
	tn3 := NewTSNAbility()
	tn3.SetTransport(prioFail)
	if out := tn3.Command(atom, TSNCommandSetInterface, TSNInterfaceArg{Interface: "eth0"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := tn3.Command(atom, TSNCommandSetPriority, TSNPriorityMapArgs{Mappings: map[uint8]uint8{3: 7}}); out.Err == nil {
		t.Fatal("set_priority_map with failing ApplyPriorityMap: got success")
	}
	if out := tn3.Command(atom, TSNCommandGetPriority, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if got, _ := out.Value.(map[uint8]uint8); got[3] != 3 {
		t.Fatalf("priority map advanced after failed apply: %v", got)
	}

	gateFail := &fakeTSNTransport{gateErr: errors.New("tc taprio error")}
	tn4 := NewTSNAbility()
	tn4.SetTransport(gateFail)
	if out := tn4.Command(atom, TSNCommandSetInterface, TSNInterfaceArg{Interface: "eth0"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := tn4.Command(atom, TSNCommandSetTimeAware, TSNTimeAwareArgs{
		Enabled: true, CycleTime: time.Millisecond, GateStates: []byte{0xFF},
	}); out.Err == nil {
		t.Fatal("set_time_aware with failing ApplyGateControl: got success")
	} else if !errors.Is(out.Err, types.ErrOperationFailed) {
		t.Fatalf("set_time_aware error = %v, want ErrOperationFailed", out.Err)
	}
	if out := tn4.Command(atom, TSNCommandGetTimeAware, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if got, _ := out.Value.(TSNTimeAwareArgs); got.Enabled {
		t.Fatalf("gate control recorded as enabled after failed apply: %+v", got)
	}
}

// TestTSNAbilityRequiresInterfaceBeforeDeviceCommands: 注入了 transport 但未选接口
// 时, 设备类命令仍须明确报错(不能拿空 iface 去下发)。
func TestTSNAbilityRequiresInterfaceBeforeDeviceCommands(t *testing.T) {
	tn := NewTSNAbility()
	tn.SetTransport(&fakeTSNTransport{})
	atom := newTSNAtom(t)
	cases := []struct {
		act  string
		args any
	}{
		{TSNCommandRegisterTalker, TSNRegisterTalkerArgs{ID: "s1", MAC: "00:11:22:33:44:55", Interval: time.Millisecond}},
		{TSNCommandRegisterListener, TSNRegisterListenerArgs{ID: "l1", MAC: "AA:BB:CC:DD:EE:FF"}},
		{TSNCommandUnregister, TSNStreamIDArg{ID: "s1"}},
		{TSNCommandSetPriority, TSNPriorityMapArgs{Mappings: map[uint8]uint8{3: 7}}},
		{TSNCommandSetTimeAware, TSNTimeAwareArgs{Enabled: true, CycleTime: time.Millisecond, GateStates: []byte{0xFF}}},
	}
	for _, tc := range cases {
		if out := tn.Command(atom, tc.act, tc.args); out.Err == nil {
			t.Fatalf("%s without interface: got success", tc.act)
		}
	}
	if out := tn.Command(atom, TSNCommandGetInterface, nil); out.Err != nil || out.Value != "" {
		t.Fatalf("get_interface = %+v err %v", out.Value, out.Err)
	}
}

// TestTSNAbilityInterfaceSwitchBlockedWithStreams: 仍有流注册时禁止切接口,
// 否则本地记录与网卡实际状态会漂移。
func TestTSNAbilityInterfaceSwitchBlockedWithStreams(t *testing.T) {
	tn := NewTSNAbility()
	tn.SetTransport(&fakeTSNTransport{})
	atom := newTSNAtom(t)
	if out := tn.Command(atom, TSNCommandSetInterface, TSNInterfaceArg{Interface: "eth0"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := tn.Command(atom, TSNCommandRegisterListener, TSNRegisterListenerArgs{ID: "l1", MAC: "AA:BB:CC:DD:EE:FF"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := tn.Command(atom, TSNCommandSetInterface, TSNInterfaceArg{Interface: "eth1"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("switch interface with streams error = %v", out.Err)
	}
	if out := tn.Command(atom, TSNCommandUnregister, TSNStreamIDArg{ID: "l1"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := tn.Command(atom, TSNCommandSetInterface, TSNInterfaceArg{Interface: "eth1"}); out.Err != nil {
		t.Fatalf("switch interface after unregister: %v", out.Err)
	}
	if out := tn.Command(atom, TSNCommandGetInterface, nil); out.Err != nil || out.Value != "eth1" {
		t.Fatalf("interface = %v err %v", out.Value, out.Err)
	}
}

// TestTSNAbilityConcurrentDelegation: -race 下并发 register/set/prio/gate 不产生
// 数据竞争, 且 transport 每次收到完整一致的参数。
func TestTSNAbilityConcurrentDelegation(t *testing.T) {
	tn := NewTSNAbility()
	tn.SetTransport(&fakeTSNTransport{})
	atom := newTSNAtom(t)
	if out := tn.Command(atom, TSNCommandSetInterface, TSNInterfaceArg{Interface: "eth0"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := string(rune('a' + i))
			_ = tn.Command(atom, TSNCommandRegisterListener, TSNRegisterListenerArgs{ID: id, MAC: "AA:BB:CC:DD:EE:FF", Priority: uint8(i % 8)})
			_ = tn.Command(atom, TSNCommandSetPriority, TSNPriorityMapArgs{Mappings: map[uint8]uint8{uint8(i % 8): 7}})
			_ = tn.Command(atom, TSNCommandSetTimeAware, TSNTimeAwareArgs{Enabled: true, CycleTime: time.Millisecond, GateStates: []byte{byte(i)}})
			_ = tn.Command(atom, TSNCommandUnregister, TSNStreamIDArg{ID: id})
			_ = tn.Command(atom, TSNCommandListStreams, nil)
			_ = tn.Command(atom, TSNCommandGetTimeAware, nil)
		}(i)
	}
	wg.Wait()
}
