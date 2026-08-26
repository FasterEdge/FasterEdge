package ability

import (
	"errors"
	"testing"

	"github.com/FasterEdge/FasterEdge/data"
	"github.com/FasterEdge/FasterEdge/types"
)

func newEdgeRoleAtom(t *testing.T) *types.Atom {
	t.Helper()
	a := &types.Atom{}
	if err := a.AddData(&data.BaseData{}); err != nil {
		t.Fatal(err)
	}
	if err := a.AddAbility(&RoleAbility{}); err != nil {
		t.Fatal(err)
	}
	if err := a.AddAbility(NewEdgeRoleAbility()); err != nil {
		t.Fatal(err)
	}
	roleAb, _ := a.Ability("RoleAbility")
	if out := roleAb.Command(a, CommandSetRole, RoleAbilityArgs{Role: "edge"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	return a
}

func TestEdgeRoleAbilityRejectsNonEdgeRole(t *testing.T) {
	e := NewEdgeRoleAbility()
	a := &types.Atom{}
	a.AddData(&data.BaseData{})
	if out := e.Command(a, EdgeRoleCommandGetZone, nil); !errors.Is(out.Err, types.ErrMissingDependency) {
		t.Fatalf("missing role ability error = %v", out.Err)
	}
	a.AddAbility(&RoleAbility{})
	// role 默认空,不是 "edge"
	if out := e.Command(a, EdgeRoleCommandGetZone, nil); !errors.Is(out.Err, types.ErrMissingDependency) {
		t.Fatalf("wrong role error = %v", out.Err)
	}
	// 设为 "cloud" 仍不对
	roleAb, _ := a.Ability("RoleAbility")
	roleAb.Command(a, CommandSetRole, RoleAbilityArgs{Role: "cloud"})
	if out := e.Command(a, EdgeRoleCommandGetZone, nil); !errors.Is(out.Err, types.ErrMissingDependency) {
		t.Fatalf("cloud role error = %v", out.Err)
	}
}

func TestEdgeRoleAbilityZoneAndCapabilities(t *testing.T) {
	e := NewEdgeRoleAbility()
	atom := newEdgeRoleAtom(t)
	// 类型错误
	if out := e.Command(atom, EdgeRoleCommandSetZone, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set zone wrong type error = %v", out.Err)
	}
	// 空 zone
	if out := e.Command(atom, EdgeRoleCommandSetZone, EdgeRoleSetZoneArgs{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set zone empty error = %v", out.Err)
	}
	if out := e.Command(atom, EdgeRoleCommandSetZone, EdgeRoleSetZoneArgs{Zone: "   "}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set zone blank error = %v", out.Err)
	}
	if out := e.Command(atom, EdgeRoleCommandSetZone, EdgeRoleSetZoneArgs{Zone: "zone-a"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// get with args
	if out := e.Command(atom, EdgeRoleCommandGetZone, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get zone with args error = %v", out.Err)
	}
	if out := e.Command(atom, EdgeRoleCommandGetZone, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if out.Value != "zone-a" {
		t.Fatalf("get zone = %q", out.Value)
	}
	// add capability
	if out := e.Command(atom, EdgeRoleCommandAddCap, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("add cap wrong type error = %v", out.Err)
	}
	if out := e.Command(atom, EdgeRoleCommandAddCap, EdgeRoleCapabilityArg{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("add cap empty error = %v", out.Err)
	}
	if out := e.Command(atom, EdgeRoleCommandAddCap, EdgeRoleCapabilityArg{Name: "modbus"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := e.Command(atom, EdgeRoleCommandAddCap, EdgeRoleCapabilityArg{Name: "mqtt"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// list caps
	if out := e.Command(atom, EdgeRoleCommandListCaps, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("list with args error = %v", out.Err)
	}
	listOut := e.Command(atom, EdgeRoleCommandListCaps, nil)
	if listOut.Err != nil {
		t.Fatal(listOut.Err)
	}
	caps, ok := listOut.Value.([]string)
	if !ok || len(caps) != 2 {
		t.Fatalf("caps = %#v", listOut.Value)
	}
	// set_caps 整体覆盖
	if out := e.Command(atom, EdgeRoleCommandSetCaps, EdgeRoleSetCapabilitiesArgs{Capabilities: []string{"opcua", ""}}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set caps with empty error = %v", out.Err)
	}
	if out := e.Command(atom, EdgeRoleCommandSetCaps, EdgeRoleSetCapabilitiesArgs{Capabilities: []string{"opcua", "serial"}}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// remove cap
	if out := e.Command(atom, EdgeRoleCommandRemoveCap, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("remove cap wrong type error = %v", out.Err)
	}
	if out := e.Command(atom, EdgeRoleCommandRemoveCap, EdgeRoleCapabilityArg{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("remove cap empty error = %v", out.Err)
	}
	if out := e.Command(atom, EdgeRoleCommandRemoveCap, EdgeRoleCapabilityArg{Name: "opcua"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := e.Command(atom, EdgeRoleCommandRemoveCap, EdgeRoleCapabilityArg{Name: "opcua"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("remove cap missing error = %v", out.Err)
	}
}

func TestEdgeRoleAbilityLatencyAndOnline(t *testing.T) {
	e := NewEdgeRoleAbility()
	atom := newEdgeRoleAtom(t)
	// 类型错误
	if out := e.Command(atom, EdgeRoleCommandRecordLatency, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("record latency wrong type error = %v", out.Err)
	}
	// 负值
	if out := e.Command(atom, EdgeRoleCommandRecordLatency, EdgeRoleRecordLatencyArgs{LatencyMs: -1}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("record latency negative error = %v", out.Err)
	}
	// 0 合法
	if out := e.Command(atom, EdgeRoleCommandRecordLatency, EdgeRoleRecordLatencyArgs{LatencyMs: 0}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// 多次记录
	for _, v := range []float64{10, 20, 30} {
		if out := e.Command(atom, EdgeRoleCommandRecordLatency, EdgeRoleRecordLatencyArgs{LatencyMs: v}); out.Err != nil {
			t.Fatal(out.Err)
		}
	}
	// get_metrics
	if out := e.Command(atom, EdgeRoleCommandGetMetrics, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get metrics with args error = %v", out.Err)
	}
	mOut := e.Command(atom, EdgeRoleCommandGetMetrics, nil)
	if mOut.Err != nil {
		t.Fatal(mOut.Err)
	}
	m, ok := mOut.Value.(EdgeRoleMetrics)
	if !ok {
		t.Fatalf("metrics type = %T", mOut.Value)
	}
	if m.LatencySamples != 4 {
		t.Fatalf("samples = %d, want 4", m.LatencySamples)
	}
	wantAvg := (0 + 10 + 20 + 30) / 4.0
	if m.AvgLatencyMs != wantAvg {
		t.Fatalf("avg latency = %v, want %v", m.AvgLatencyMs, wantAvg)
	}
	// set_online
	if out := e.Command(atom, EdgeRoleCommandSetOnline, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set online wrong type error = %v", out.Err)
	}
	if out := e.Command(atom, EdgeRoleCommandSetOnline, EdgeRoleSetOnlineArgs{Online: true}); out.Err != nil {
		t.Fatal(out.Err)
	}
	mOut = e.Command(atom, EdgeRoleCommandGetMetrics, nil)
	m = mOut.Value.(EdgeRoleMetrics)
	if !m.Online {
		t.Fatal("online should be true")
	}
}

func TestEdgeRoleAbilityDescribe(t *testing.T) {
	e := NewEdgeRoleAbility()
	atom := newEdgeRoleAtom(t)
	if err := atom.MountAll(); err != nil {
		t.Fatal(err)
	}
	if out := e.Command(atom, EdgeRoleCommandSetZone, EdgeRoleSetZoneArgs{Zone: "cn-east-1"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := e.Command(atom, EdgeRoleCommandAddCap, EdgeRoleCapabilityArg{Name: "modbus"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := e.Command(atom, EdgeRoleCommandSetOnline, EdgeRoleSetOnlineArgs{Online: true}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := e.Command(atom, EdgeRoleCommandDescribe, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("describe with args error = %v", out.Err)
	}
	out := e.Command(atom, EdgeRoleCommandDescribe, nil)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	d, ok := out.Value.(EdgeRoleDescription)
	if !ok {
		t.Fatalf("describe type = %T", out.Value)
	}
	if d.Role != "edge" {
		t.Fatalf("role = %q", d.Role)
	}
	if d.Zone != "cn-east-1" {
		t.Fatalf("zone = %q", d.Zone)
	}
	if !d.Online {
		t.Fatal("expected online = true")
	}
	if len(d.Caps) != 1 || d.Caps[0] != "modbus" {
		t.Fatalf("caps = %#v", d.Caps)
	}
}

func TestEdgeRoleAbilityUnknownCommand(t *testing.T) {
	e := NewEdgeRoleAbility()
	atom := newEdgeRoleAtom(t)
	if out := e.Command(atom, "nope", nil); !errors.Is(out.Err, types.ErrUnsupportedCommand) {
		t.Fatalf("unknown error = %v", out.Err)
	}
}
