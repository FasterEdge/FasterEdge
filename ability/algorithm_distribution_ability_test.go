package ability

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/FasterEdge/FasterEdge/data"
	"github.com/FasterEdge/FasterEdge/types"
)

func newAlgDistAtom(t *testing.T) *types.Atom {
	t.Helper()
	a := &types.Atom{}
	if err := a.AddData(&data.BaseData{}); err != nil {
		t.Fatal(err)
	}
	if err := a.AddData(data.NewNetMapData()); err != nil {
		t.Fatal(err)
	}
	if err := a.AddAbility(NewNetMapAbility()); err != nil {
		t.Fatal(err)
	}
	if err := a.AddAbility(NewFileTransferAbility()); err != nil {
		t.Fatal(err)
	}
	if err := a.AddAbility(NewAlgDistAbility()); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestAlgDistAbilityRejectsMissingDeps(t *testing.T) {
	d := NewAlgDistAbility()
	if out := d.Command(&types.Atom{}, AlgDistCommandList, nil); !errors.Is(out.Err, types.ErrMissingDependency) {
		t.Fatalf("missing deps error = %v", out.Err)
	}
}

func TestAlgDistAbilityRegisterAndList(t *testing.T) {
	d := NewAlgDistAbility()
	atom := newAlgDistAtom(t)
	// 类型错误
	if out := d.Command(atom, AlgDistCommandRegister, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("register wrong type error = %v", out.Err)
	}
	// 缺字段
	if out := d.Command(atom, AlgDistCommandRegister, AlgDistRegisterArgs{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("register empty error = %v", out.Err)
	}
	// 正常注册
	if out := d.Command(atom, AlgDistCommandRegister, AlgDistRegisterArgs{
		Name: "edge-detector", Version: "1.0.0", SourcePath: "/tmp/edge-detector.so", ContentType: "application/wasm",
	}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// 重复
	if out := d.Command(atom, AlgDistCommandRegister, AlgDistRegisterArgs{
		Name: "edge-detector", Version: "1.0.0", SourcePath: "/tmp/x",
	}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("register duplicate error = %v", out.Err)
	}
	// list
	if out := d.Command(atom, AlgDistCommandList, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("list with args error = %v", out.Err)
	}
	listOut := d.Command(atom, AlgDistCommandList, nil)
	if listOut.Err != nil {
		t.Fatal(listOut.Err)
	}
	algs, _ := listOut.Value.([]AlgDistAlgorithm)
	if len(algs) != 1 {
		t.Fatalf("list = %#v", listOut.Value)
	}
	// get
	if out := d.Command(atom, AlgDistCommandGet, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get wrong type error = %v", out.Err)
	}
	if out := d.Command(atom, AlgDistCommandGet, AlgDistAlgorithmRef{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get empty error = %v", out.Err)
	}
	if out := d.Command(atom, AlgDistCommandGet, AlgDistAlgorithmRef{Name: "edge-detector", Version: "1.0.0"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := d.Command(atom, AlgDistCommandGet, AlgDistAlgorithmRef{Name: "ghost", Version: "0.0.1"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get missing error = %v", out.Err)
	}
	// unregister
	if out := d.Command(atom, AlgDistCommandUnregister, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("unregister wrong type error = %v", out.Err)
	}
	if out := d.Command(atom, AlgDistCommandUnregister, AlgDistAlgorithmRef{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("unregister empty error = %v", out.Err)
	}
	if out := d.Command(atom, AlgDistCommandUnregister, AlgDistAlgorithmRef{Name: "ghost", Version: "0"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("unregister missing error = %v", out.Err)
	}
	if out := d.Command(atom, AlgDistCommandUnregister, AlgDistAlgorithmRef{Name: "edge-detector", Version: "1.0.0"}); out.Err != nil {
		t.Fatal(out.Err)
	}
}

func TestAlgDistAbilityDistributeSkeleton(t *testing.T) {
	d := NewAlgDistAbility()
	atom := newAlgDistAtom(t)
	// 注册对端
	nm, _ := atom.Ability("NetMapAbility")
	if out := nm.Command(atom, NetMapCommandRegisterPeer, NetMapRegisterPeerArgs{Name: "edge-2", Address: "10.0.0.2:7000"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// 注册算法
	tmp := filepath.Join(t.TempDir(), "algo.bin")
	os.WriteFile(tmp, []byte("payload"), 0o644)
	if out := d.Command(atom, AlgDistCommandRegister, AlgDistRegisterArgs{
		Name: "edge-detector", Version: "1.0.0", SourcePath: tmp,
	}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// distribute
	if out := d.Command(atom, AlgDistCommandDistribute, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("distribute wrong type error = %v", out.Err)
	}
	// 缺字段
	if out := d.Command(atom, AlgDistCommandDistribute, AlgDistDistributeArgs{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("distribute empty error = %v", out.Err)
	}
	// 算法不存在
	if out := d.Command(atom, AlgDistCommandDistribute, AlgDistDistributeArgs{
		Name: "ghost", Version: "0", Target: "edge-2",
	}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("distribute missing algo error = %v", out.Err)
	}
	// 正常
	if out := d.Command(atom, AlgDistCommandDistribute, AlgDistDistributeArgs{
		Name: "edge-detector", Version: "1.0.0", Target: "edge-2",
	}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// list distributions
	if out := d.Command(atom, AlgDistCommandListDistribute, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("list dist with args error = %v", out.Err)
	}
	if out := d.Command(atom, AlgDistCommandListDistribute, nil); out.Err != nil {
		t.Fatal(out.Err)
	}
	// cancel
	if out := d.Command(atom, AlgDistCommandCancel, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("cancel wrong type error = %v", out.Err)
	}
	if out := d.Command(atom, AlgDistCommandCancel, AlgDistIDArg{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("cancel empty error = %v", out.Err)
	}
	if out := d.Command(atom, AlgDistCommandCancel, AlgDistIDArg{ID: "alg-9999"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("cancel missing error = %v", out.Err)
	}
	if out := d.Command(atom, AlgDistCommandClearFinished, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("clear with args error = %v", out.Err)
	}
	if out := d.Command(atom, AlgDistCommandClearFinished, nil); out.Err != nil {
		t.Fatal(out.Err)
	}
}

func TestAlgDistAbilityUnknownCommand(t *testing.T) {
	d := NewAlgDistAbility()
	atom := newAlgDistAtom(t)
	if out := d.Command(atom, "nope", nil); !errors.Is(out.Err, types.ErrUnsupportedCommand) {
		t.Fatalf("unknown error = %v", out.Err)
	}
}

func TestAlgDistAbilityCancelAfterCompletion(t *testing.T) {
	d := NewAlgDistAbility()
	atom := newAlgDistAtom(t)
	nm, _ := atom.Ability("NetMapAbility")
	nm.Command(atom, NetMapCommandRegisterPeer, NetMapRegisterPeerArgs{Name: "edge-2", Address: "10.0.0.2:7000"})
	tmp := filepath.Join(t.TempDir(), "a.bin")
	os.WriteFile(tmp, []byte("x"), 0o644)
	d.Command(atom, AlgDistCommandRegister, AlgDistRegisterArgs{Name: "a", Version: "1", SourcePath: tmp})
	distOut := d.Command(atom, AlgDistCommandDistribute, AlgDistDistributeArgs{Name: "a", Version: "1", Target: "edge-2"})
	if distOut.Err != nil {
		t.Fatal(distOut.Err)
	}
	// 骨架模式下传输立即完成,cancel 应当失败
	time.Sleep(50 * time.Millisecond)
	if out := d.Command(atom, AlgDistCommandCancel, AlgDistIDArg{ID: distOut.Value.(string)}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("cancel after completion error = %v", out.Err)
	}
}
