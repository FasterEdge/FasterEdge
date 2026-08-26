package ability

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/FasterEdge/FasterEdge/data"
	"github.com/FasterEdge/FasterEdge/types"
)

// fakeTransport 记录调用次数并可注入错误,用于测试。
type fakeTransport struct {
	mu          sync.Mutex
	uploadCalls int
	dlCalls     int
	uploadErr   error
	dlErr       error
	block       chan struct{}
}

func (f *fakeTransport) Upload(_ FileTransferContext) error {
	f.mu.Lock()
	f.uploadCalls++
	block := f.block
	uploadErr := f.uploadErr
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	return uploadErr
}

func (f *fakeTransport) Download(_ FileTransferContext) error {
	f.mu.Lock()
	f.dlCalls++
	block := f.block
	dlErr := f.dlErr
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	return dlErr
}

func (f *fakeTransport) Calls() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.uploadCalls, f.dlCalls
}

func newFileTransferAtom(t *testing.T) *types.Atom {
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
	return a
}

func registerPeer(t *testing.T, atom *types.Atom, name, addr string) {
	t.Helper()
	nm, _ := atom.Ability("NetMapAbility")
	if out := nm.Command(atom, NetMapCommandRegisterPeer, NetMapRegisterPeerArgs{Name: name, Address: addr}); out.Err != nil {
		t.Fatal(out.Err)
	}
}

func TestFileTransferAbilityRejectsMissingDeps(t *testing.T) {
	f := NewFileTransferAbility()
	if out := f.Command(&types.Atom{}, FileTransferCommandGetTarget, nil); !errors.Is(out.Err, types.ErrMissingDependency) {
		t.Fatalf("missing deps error = %v", out.Err)
	}
	atom := &types.Atom{}
	atom.AddData(&data.BaseData{})
	if out := f.Command(atom, FileTransferCommandGetTarget, nil); !errors.Is(out.Err, types.ErrMissingDependency) {
		t.Fatalf("missing NetMapData error = %v", out.Err)
	}
}

func TestFileTransferAbilitySetGetTarget(t *testing.T) {
	f := NewFileTransferAbility()
	atom := newFileTransferAtom(t)
	registerPeer(t, atom, "edge-2", "10.0.0.2:7000")
	// 类型错误
	if out := f.Command(atom, FileTransferCommandSetTarget, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set target wrong type error = %v", out.Err)
	}
	// 空 peer
	if out := f.Command(atom, FileTransferCommandSetTarget, FileTransferTargetArgs{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set target empty error = %v", out.Err)
	}
	// 不存在的 peer
	if out := f.Command(atom, FileTransferCommandSetTarget, FileTransferTargetArgs{PeerName: "ghost"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set target missing peer error = %v", out.Err)
	}
	if out := f.Command(atom, FileTransferCommandSetTarget, FileTransferTargetArgs{PeerName: "edge-2"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := f.Command(atom, FileTransferCommandGetTarget, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get with args error = %v", out.Err)
	}
	if out := f.Command(atom, FileTransferCommandGetTarget, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if out.Value != "edge-2" {
		t.Fatalf("target = %q", out.Value)
	}
}

func TestFileTransferAbilityRequiresNetMapAbility(t *testing.T) {
	f := NewFileTransferAbility()
	atom := &types.Atom{}
	atom.AddData(&data.BaseData{})
	atom.AddData(data.NewNetMapData())
	// 没注册 NetMapAbility → set_target 报缺
	if out := f.Command(atom, FileTransferCommandSetTarget, FileTransferTargetArgs{PeerName: "x"}); !errors.Is(out.Err, types.ErrMissingDependency) {
		t.Fatalf("missing NetMapAbility error = %v", out.Err)
	}
}

func TestFileTransferAbilitySkeletonUpload(t *testing.T) {
	f := NewFileTransferAbility()
	atom := newFileTransferAtom(t)
	registerPeer(t, atom, "edge-2", "10.0.0.2:7000")
	f.Command(atom, FileTransferCommandSetTarget, FileTransferTargetArgs{PeerName: "edge-2"})
	// 没有 transport,直接走骨架路径(标记完成但不实际传)
	// 先准备一个本地文件
	tmp := filepath.Join(t.TempDir(), "upload.txt")
	if err := os.WriteFile(tmp, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 类型错误
	if out := f.Command(atom, FileTransferCommandUpload, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("upload wrong type error = %v", out.Err)
	}
	// 空路径
	if out := f.Command(atom, FileTransferCommandUpload, FileTransferUploadArgs{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("upload empty path error = %v", out.Err)
	}
	// 本地文件不存在
	if out := f.Command(atom, FileTransferCommandUpload, FileTransferUploadArgs{LocalPath: "/no/such/file", RemotePath: "/dst"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("upload missing file error = %v", out.Err)
	}
	// 正常
	out := f.Command(atom, FileTransferCommandUpload, FileTransferUploadArgs{LocalPath: tmp, RemotePath: "/dst/file"})
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	// 骨架模式下返回的是 transfer 指针的 ID(字符串)
	id, ok := out.Value.(string)
	if !ok || id == "" {
		t.Fatalf("upload value = %#v", out.Value)
	}
	// get
	if out := f.Command(atom, FileTransferCommandGet, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get wrong type error = %v", out.Err)
	}
	if out := f.Command(atom, FileTransferCommandGet, FileTransferIDArg{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get empty id error = %v", out.Err)
	}
	if out := f.Command(atom, FileTransferCommandGet, FileTransferIDArg{ID: "tx-9999"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get missing id error = %v", out.Err)
	}
	getOut := f.Command(atom, FileTransferCommandGet, FileTransferIDArg{ID: id})
	if getOut.Err != nil {
		t.Fatal(getOut.Err)
	}
	if transfer, _ := getOut.Value.(FileTransfer); transfer.Status != FileTransferStatusCompleted {
		t.Fatalf("status = %q, want completed", transfer.Status)
	}
	// list
	if out := f.Command(atom, FileTransferCommandList, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("list with args error = %v", out.Err)
	}
	listOut := f.Command(atom, FileTransferCommandList, nil)
	if listOut.Err != nil {
		t.Fatal(listOut.Err)
	}
	// clear
	if out := f.Command(atom, FileTransferCommandClearFinished, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("clear with args error = %v", out.Err)
	}
	clearOut := f.Command(atom, FileTransferCommandClearFinished, nil)
	if clearOut.Err != nil {
		t.Fatal(clearOut.Err)
	}
	if n, _ := clearOut.Value.(int); n == 0 {
		t.Fatalf("clear count = 0")
	}
}

func TestFileTransferAbilityRequiresTarget(t *testing.T) {
	f := NewFileTransferAbility()
	atom := newFileTransferAtom(t)
	// 没 set_target → 拒绝
	tmp := filepath.Join(t.TempDir(), "x.txt")
	os.WriteFile(tmp, []byte("x"), 0o644)
	if out := f.Command(atom, FileTransferCommandUpload, FileTransferUploadArgs{LocalPath: tmp, RemotePath: "/r"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("no target error = %v", out.Err)
	}
}

func TestFileTransferAbilityDownloadSkeleton(t *testing.T) {
	f := NewFileTransferAbility()
	atom := newFileTransferAtom(t)
	registerPeer(t, atom, "edge-2", "10.0.0.2:7000")
	f.Command(atom, FileTransferCommandSetTarget, FileTransferTargetArgs{PeerName: "edge-2"})
	// 类型错误
	if out := f.Command(atom, FileTransferCommandDownload, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("download wrong type error = %v", out.Err)
	}
	// 空路径
	if out := f.Command(atom, FileTransferCommandDownload, FileTransferDownloadArgs{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("download empty error = %v", out.Err)
	}
	// 正常(骨架)
	if out := f.Command(atom, FileTransferCommandDownload, FileTransferDownloadArgs{RemotePath: "/src", LocalPath: filepath.Join(t.TempDir(), "out")}); out.Err != nil {
		t.Fatal(out.Err)
	}
}

func TestFileTransferAbilityCancel(t *testing.T) {
	f := NewFileTransferAbility()
	atom := newFileTransferAtom(t)
	registerPeer(t, atom, "edge-2", "10.0.0.2:7000")
	f.Command(atom, FileTransferCommandSetTarget, FileTransferTargetArgs{PeerName: "edge-2"})

	transport := &fakeTransport{block: make(chan struct{})}
	f.SetTransport(transport)
	tmp := filepath.Join(t.TempDir(), "x.txt")
	os.WriteFile(tmp, []byte("x"), 0o644)
	out := f.Command(atom, FileTransferCommandUpload, FileTransferUploadArgs{LocalPath: tmp, RemotePath: "/r"})
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	id := out.Value.(string)
	// 类型错误
	if out := f.Command(atom, FileTransferCommandCancel, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("cancel wrong type error = %v", out.Err)
	}
	if out := f.Command(atom, FileTransferCommandCancel, FileTransferIDArg{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("cancel empty id error = %v", out.Err)
	}
	if out := f.Command(atom, FileTransferCommandCancel, FileTransferIDArg{ID: "tx-9999"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("cancel missing error = %v", out.Err)
	}
	// 取消运行中的 job
	if out := f.Command(atom, FileTransferCommandCancel, FileTransferIDArg{ID: id}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// 二次取消(已完成)应失败
	if out := f.Command(atom, FileTransferCommandCancel, FileTransferIDArg{ID: id}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("re-cancel error = %v", out.Err)
	}
	// 解除阻塞,让 transport 返回
	close(transport.block)
	// 等待状态写入
	for i := 0; i < 50; i++ {
		getOut := f.Command(atom, FileTransferCommandGet, FileTransferIDArg{ID: id})
		if getOut.Err == nil {
			if t2, _ := getOut.Value.(FileTransfer); t2.Status == FileTransferStatusCanceled {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("transfer did not reach canceled status in time")
}

func TestFileTransferAbilityTransportError(t *testing.T) {
	f := NewFileTransferAbility()
	atom := newFileTransferAtom(t)
	registerPeer(t, atom, "edge-2", "10.0.0.2:7000")
	f.Command(atom, FileTransferCommandSetTarget, FileTransferTargetArgs{PeerName: "edge-2"})

	transport := &fakeTransport{uploadErr: errors.New("connection refused")}
	f.SetTransport(transport)
	tmp := filepath.Join(t.TempDir(), "x.txt")
	os.WriteFile(tmp, []byte("x"), 0o644)
	out := f.Command(atom, FileTransferCommandUpload, FileTransferUploadArgs{LocalPath: tmp, RemotePath: "/r"})
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	id := out.Value.(string)
	for i := 0; i < 50; i++ {
		getOut := f.Command(atom, FileTransferCommandGet, FileTransferIDArg{ID: id})
		if getOut.Err == nil {
			if t2, _ := getOut.Value.(FileTransfer); t2.Status == FileTransferStatusFailed {
				if t2.Error == "" {
					t.Fatal("expected error message set on failure")
				}
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("transfer did not reach failed status in time")
}

func TestFileTransferAbilityUnknownCommand(t *testing.T) {
	f := NewFileTransferAbility()
	atom := newFileTransferAtom(t)
	if out := f.Command(atom, "nope", nil); !errors.Is(out.Err, types.ErrUnsupportedCommand) {
		t.Fatalf("unknown error = %v", out.Err)
	}
}
