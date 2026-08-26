package data

import (
	"errors"
	"testing"

	"github.com/FasterEdge/FasterEdge/types"
)

func TestNetMapDataSetAndGetNodeName(t *testing.T) {
	d := NewNetMapData()
	for _, args := range []any{
		nil,
		NetMapSetNodeNameArgs{},
		NetMapSetNodeNameArgs{Name: "  "},
		NetMapSetNodeNameArgs{Name: " leading-space"},
	} {
		if out := d.Command(nil, NetMapCommandSetNodeName, args); !errors.Is(out.Err, types.ErrInvalidArguments) {
			t.Fatalf("set_node_name args %#v error = %v", args, out.Err)
		}
	}
	if out := d.Command(nil, NetMapCommandSetNodeName, NetMapSetNodeNameArgs{Name: "edge-01"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	out := d.Command(nil, NetMapCommandInfo, nil)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	info, ok := out.Value.(NetMapLocalInfo)
	if !ok {
		t.Fatalf("info type = %T", out.Value)
	}
	if info.NodeName != "edge-01" {
		t.Fatalf("node name = %q, want %q", info.NodeName, "edge-01")
	}
}

func TestNetMapDataInterfacesScanned(t *testing.T) {
	d := NewNetMapData()
	if err := d.Mount(nil); err != nil {
		t.Fatal(err)
	}
	if out := d.Command(nil, NetMapCommandInterfaces, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("interfaces with args error = %v", out.Err)
	}
	out := d.Command(nil, NetMapCommandInterfaces, nil)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	ifaces, ok := out.Value.([]NetMapInterface)
	if !ok {
		t.Fatalf("interfaces type = %T", out.Value)
	}
	if len(ifaces) == 0 {
		t.Skip("no up IPv4 interface in this test environment")
	}
	for _, iface := range ifaces {
		if iface.Name == "" {
			t.Fatalf("interface with empty name: %#v", iface)
		}
		if len(iface.IPv4) == 0 {
			t.Fatalf("interface %s has no IPv4 addresses", iface.Name)
		}
	}
}

func TestNetMapDataDefaultIfaceValidation(t *testing.T) {
	d := NewNetMapData()
	if err := d.Mount(nil); err != nil {
		t.Fatal(err)
	}
	// 非字符串参数
	if out := d.Command(nil, NetMapCommandSetDefaultIface, nil); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("nil args error = %v", out.Err)
	}
	if out := d.Command(nil, NetMapCommandSetDefaultIface, "plain-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("plain string args error = %v", out.Err)
	}
	if out := d.Command(nil, NetMapCommandSetDefaultIface, NetMapSetDefaultIfaceArgs{Name: ""}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("empty name error = %v", out.Err)
	}
	// 一定不存在的接口
	if out := d.Command(nil, NetMapCommandSetDefaultIface, NetMapSetDefaultIfaceArgs{Name: "definitely-no-such-iface-zzz"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("unknown iface error = %v", out.Err)
	}
	// 未知命令
	if out := d.Command(nil, "unknown", nil); !errors.Is(out.Err, types.ErrUnsupportedCommand) {
		t.Fatalf("unknown command error = %v", out.Err)
	}
}
