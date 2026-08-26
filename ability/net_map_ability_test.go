package ability

import (
	"errors"
	"sync"
	"testing"

	"github.com/FasterEdge/FasterEdge/data"
	"github.com/FasterEdge/FasterEdge/types"
)

func newNetMapAtom(t *testing.T) *types.Atom {
	t.Helper()
	a := &types.Atom{}
	if err := a.AddData(&data.BaseData{}); err != nil {
		t.Fatal(err)
	}
	if err := a.AddData(data.NewNetMapData()); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestNetMapAbilityRejectsMissingDependencies(t *testing.T) {
	a := &NetMapAbility{}
	atom := &types.Atom{}
	// 缺 BaseData + NetMapData
	if out := a.Command(atom, NetMapCommandListPeers, nil); !errors.Is(out.Err, types.ErrMissingDependency) {
		t.Fatalf("missing deps error = %v", out.Err)
	}
}

func TestNetMapAbilityRegisterLookupUpdateUnregister(t *testing.T) {
	a := NewNetMapAbility()
	atom := newNetMapAtom(t)
	if out := a.Command(atom, NetMapCommandRegisterPeer, "not-typed"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("register wrong type error = %v", out.Err)
	}
	// 缺 Name
	if out := a.Command(atom, NetMapCommandRegisterPeer, NetMapRegisterPeerArgs{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("register blank name error = %v", out.Err)
	}
	// 非法地址
	if out := a.Command(atom, NetMapCommandRegisterPeer, NetMapRegisterPeerArgs{Name: "p1", Address: "http://bad"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("register bad address error = %v", out.Err)
	}
	// 正常注册
	out := a.Command(atom, NetMapCommandRegisterPeer, NetMapRegisterPeerArgs{Name: "edge-2", Address: "10.0.0.2:7000", Role: "edge"})
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	// 重复注册
	if out := a.Command(atom, NetMapCommandRegisterPeer, NetMapRegisterPeerArgs{Name: "edge-2", Address: "10.0.0.2:7000"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("register duplicate error = %v", out.Err)
	}
	// list
	listOut := a.Command(atom, NetMapCommandListPeers, struct{}{})
	if listOut.Err == nil || !errors.Is(listOut.Err, types.ErrInvalidArguments) {
		t.Fatalf("list with args should reject, got err=%v", listOut.Err)
	}
	listOut = a.Command(atom, NetMapCommandListPeers, nil)
	if listOut.Err != nil {
		t.Fatal(listOut.Err)
	}
	peers, ok := listOut.Value.([]NetMapPeer)
	if !ok || len(peers) != 1 {
		t.Fatalf("list peers = %#v", listOut.Value)
	}
	// lookup by name
	if out := a.Command(atom, NetMapCommandLookupPeer, NetMapLookupPeerArgs{Name: "edge-2"}); out.Err != nil {
		t.Fatalf("lookup by name: %v", out.Err)
	} else if got, _ := out.Value.(NetMapPeer); got.Role != "edge" {
		t.Fatalf("lookup by name role = %q", got.Role)
	}
	// lookup by address
	if out := a.Command(atom, NetMapCommandLookupPeer, NetMapLookupPeerArgs{Address: "10.0.0.2:7000"}); out.Err != nil {
		t.Fatalf("lookup by address: %v", out.Err)
	}
	// lookup miss
	if out := a.Command(atom, NetMapCommandLookupPeer, NetMapLookupPeerArgs{Name: "ghost"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("lookup miss error = %v", out.Err)
	}
	if out := a.Command(atom, NetMapCommandLookupPeer, NetMapLookupPeerArgs{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("lookup empty error = %v", out.Err)
	}
	if out := a.Command(atom, NetMapCommandLookupPeer, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("lookup wrong type error = %v", out.Err)
	}
	// update
	if out := a.Command(atom, NetMapCommandUpdatePeer, NetMapUpdatePeerArgs{Name: "edge-2", NewRole: "leader", TouchLastSeen: true}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := a.Command(atom, NetMapCommandLookupPeer, NetMapLookupPeerArgs{Name: "edge-2"}); out.Err != nil {
		t.Fatal(out.Err)
	} else if got, _ := out.Value.(NetMapPeer); got.Role != "leader" {
		t.Fatalf("after update role = %q", got.Role)
	}
	// update non-existent
	if out := a.Command(atom, NetMapCommandUpdatePeer, NetMapUpdatePeerArgs{Name: "ghost"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("update ghost error = %v", out.Err)
	}
	if out := a.Command(atom, NetMapCommandUpdatePeer, NetMapUpdatePeerArgs{Name: "edge-2", NewAddress: "bad addr"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("update bad address error = %v", out.Err)
	}
	if out := a.Command(atom, NetMapCommandUpdatePeer, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("update wrong type error = %v", out.Err)
	}
	// unregister
	if out := a.Command(atom, NetMapCommandUnregisterPeer, NetMapLookupPeerArgs{Name: "edge-2"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := a.Command(atom, NetMapCommandUnregisterPeer, NetMapLookupPeerArgs{Name: "edge-2"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("unregister missing error = %v", out.Err)
	}
	if out := a.Command(atom, NetMapCommandUnregisterPeer, nil); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("unregister nil args error = %v", out.Err)
	}
}

func TestNetMapAbilityGetTopology(t *testing.T) {
	a := NewNetMapAbility()
	atom := newNetMapAtom(t)
	nd, _ := atom.Data("NetMapData")
	if err := nd.Mount(atom); err != nil {
		t.Fatal(err)
	}
	if out := nd.Command(atom, data.NetMapCommandSetNodeName, data.NetMapSetNodeNameArgs{Name: "self-node"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := a.Command(atom, NetMapCommandRegisterPeer, NetMapRegisterPeerArgs{Name: "p1", Address: "10.0.0.1"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := a.Command(atom, NetMapCommandGetTopology, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get_topology with args error = %v", out.Err)
	}
	out := a.Command(atom, NetMapCommandGetTopology, nil)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	top, ok := out.Value.(NetMapTopology)
	if !ok {
		t.Fatalf("topology type = %T", out.Value)
	}
	if top.Self.NodeName != "self-node" {
		t.Fatalf("self node name = %q", top.Self.NodeName)
	}
	if len(top.Peers) != 1 || top.Peers[0].Name != "p1" {
		t.Fatalf("topology peers = %#v", top.Peers)
	}
}

func TestNetMapAbilityUnknownCommand(t *testing.T) {
	a := NewNetMapAbility()
	atom := newNetMapAtom(t)
	if out := a.Command(atom, "definitely_unknown", nil); !errors.Is(out.Err, types.ErrUnsupportedCommand) {
		t.Fatalf("unknown error = %v", out.Err)
	}
}

func TestNetMapAbilityConcurrentRegister(t *testing.T) {
	a := NewNetMapAbility()
	atom := newNetMapAtom(t)
	start := make(chan struct{})
	var wg sync.WaitGroup
	const N = 32
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			arg := NetMapRegisterPeerArgs{
				Name:    nameFromIndex(i),
				Address: addrFromIndex(i),
			}
			if out := a.Command(atom, NetMapCommandRegisterPeer, arg); out.Err != nil {
				t.Errorf("register #%d: %v", i, out.Err)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	out := a.Command(atom, NetMapCommandListPeers, nil)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	if peers, _ := out.Value.([]NetMapPeer); len(peers) != N {
		t.Fatalf("peers len = %d, want %d", len(peers), N)
	}
}

func nameFromIndex(i int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	if i < len(letters) {
		return "n-" + string(letters[i])
	}
	return "n-" + string(letters[i/len(letters)]) + string(letters[i%len(letters)])
}

func addrFromIndex(i int) string {
	return "10.0.0." + itoaSmall(i+1) + ":7000"
}

func itoaSmall(i int) string {
	if i == 0 {
		return "0"
	}
	var b [4]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}
