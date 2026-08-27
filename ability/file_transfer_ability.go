package ability

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/FasterEdge/FasterEdge/types"
)

const (
	FileTransferCommandSetTarget     = "set_target"
	FileTransferCommandGetTarget     = "get_target"
	FileTransferCommandUpload        = "upload"
	FileTransferCommandDownload      = "download"
	FileTransferCommandList          = "list"
	FileTransferCommandGet           = "get_transfer"
	FileTransferCommandCancel        = "cancel"
	FileTransferCommandClearFinished = "clear_finished"
)

// FileTransferDirection 标识传输方向。
type FileTransferDirection string

const (
	FileTransferDirectionUpload   FileTransferDirection = "upload"
	FileTransferDirectionDownload FileTransferDirection = "download"
)

// FileTransferStatus 标识传输状态。
type FileTransferStatus string

const (
	FileTransferStatusPending   FileTransferStatus = "pending"
	FileTransferStatusRunning   FileTransferStatus = "running"
	FileTransferStatusCompleted FileTransferStatus = "completed"
	FileTransferStatusFailed    FileTransferStatus = "failed"
	FileTransferStatusCanceled  FileTransferStatus = "canceled"
)

// FileTransfer 描述一次传输的元数据。
type FileTransfer struct {
	ID         string
	Direction  FileTransferDirection
	Target     string // 对等节点名(从 NetMapAbility 解析)
	LocalPath  string
	RemotePath string
	Size       int64
	StartedAt  time.Time
	FinishedAt time.Time
	Status     FileTransferStatus
	Error      string
	cancel     chan struct{} // 内部使用,cancel 命令通过它通知 Transport
}

// FileTransferTargetArgs 是 set_target 命令的参数。
type FileTransferTargetArgs struct {
	PeerName string
}

// FileTransferUploadArgs 是 upload 命令的参数。
// Target 为可选字段:非空时本次上传直接投递到指定对端,不修改共享 target。
type FileTransferUploadArgs struct {
	LocalPath  string
	RemotePath string
	Target     string // 可选,非空表示覆盖本次传输的目标对端
}

// FileTransferDownloadArgs 是 download 命令的参数。
// Target 为可选字段:非空时本次下载从指定对端拉取,不修改共享 target。
type FileTransferDownloadArgs struct {
	RemotePath string
	LocalPath  string
	Target     string // 可选,非空表示覆盖本次传输的目标对端
}

// FileTransferIDArg 是 get_transfer / cancel / clear_finished 用的参数。
type FileTransferIDArg struct {
	ID string
}

// FileTransferTransport 抽象出节点间文件传输的实现,允许用户注入 HTTP/gRPC/MQTT/...
// 骨架不内置任何真实传输;返回 error 即视为传输失败,ErrCanceled 表示被 Cancel() 触发。
type FileTransferTransport interface {
	Upload(ctx FileTransferContext) error
	Download(ctx FileTransferContext) error
}

// FileTransferContext 透传给 Transport 的运行时上下文。
type FileTransferContext struct {
	Transfer FileTransfer
	cancel   chan struct{}
}

// Cancel 返回的通道在 Close 后被关闭,Transport 应在 select 中监听。
func (c *FileTransferContext) Cancel() <-chan struct{} { return c.cancel }

// fileTransferClosingError 表示能力已被 Unmount 关闭后再次接收新命令。
var fileTransferClosingError = errors.New("FileTransferAbility is closing")

// FileTransferAbility 管理文件传输的元数据与任务调度,实际字节流由注入的 Transport 完成。
// 依赖:BaseData + NetMapData(用于解析对等节点地址)。
type FileTransferAbility struct {
	mu        sync.RWMutex
	closing   bool
	target    string
	transfers map[string]*FileTransfer
	transport FileTransferTransport
	seq       uint64
	wg        sync.WaitGroup
	running   atomic.Int32
}

func NewFileTransferAbility() *FileTransferAbility {
	return &FileTransferAbility{transfers: make(map[string]*FileTransfer)}
}

// SetTransport 注入传输实现(允许 nil 表示骨架模式,仅记录元数据但不实际传输)。
func (a *FileTransferAbility) SetTransport(t FileTransferTransport) {
	a.mu.Lock()
	a.transport = t
	a.mu.Unlock()
}

func (a *FileTransferAbility) GetName() string { return "FileTransferAbility" }
func (a *FileTransferAbility) Dependencies() []types.Dependency {
	return []types.Dependency{{Kind: types.DependencyData, Name: "BaseData"}, {Kind: types.DependencyData, Name: "NetMapData"}, {Kind: types.DependencyAbility, Name: "NetMapAbility"}}
}

func (a *FileTransferAbility) Describe() string {
	return "FileTransferAbility提供节点间文件传输能力,基于可插拔的 Transport 接口,跟踪每个传输的状态、大小与错误。"
}

func (a *FileTransferAbility) Check(atom *types.Atom) error {
	if atom == nil {
		return types.ErrMissingDependency
	}
	if _, ok := atom.Data("BaseData"); !ok {
		return types.ErrMissingDependency
	}
	if _, ok := atom.Data("NetMapData"); !ok {
		return types.ErrMissingDependency
	}
	return nil
}

func (a *FileTransferAbility) Mount(atom *types.Atom) error { return a.Check(atom) }

// beginShutdown 把 ability 标记为 closing。返回是否成功。
func (a *FileTransferAbility) beginShutdown() bool {
	a.mu.Lock()
	if a.closing {
		a.mu.Unlock()
		return false
	}
	a.closing = true
	a.mu.Unlock()
	return true
}

// Unmount marks the ability as closing, cancels every active transfer and
// waits for in-flight workers to finish (bounded by ctx).
//
// All transfer reads/writes happen under a.mu. The WaitGroup is incremented
// for new transfers while !closing; closing is set before Wait so a concurrent
// wg.Add on a still-running transfer cannot race a wg.Wait with count==0.
func (a *FileTransferAbility) Unmount(ctx context.Context, _ *types.Atom) error {
	if ctx == nil {
		return types.ErrNilContext
	}
	if !a.beginShutdown() {
		return nil
	}
	a.mu.Lock()
	channels := make([]chan struct{}, 0, len(a.transfers))
	for _, transfer := range a.transfers {
		if transfer.Status == FileTransferStatusRunning || transfer.Status == FileTransferStatusPending {
			transfer.Status = FileTransferStatusCanceled
			transfer.FinishedAt = time.Now()
			channels = append(channels, transfer.cancel)
		}
	}
	a.mu.Unlock()
	for _, cancel := range channels {
		select {
		case <-cancel:
		default:
			close(cancel)
		}
	}
	done := make(chan struct{})
	go func() {
		a.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *FileTransferAbility) rejectIfClosing(act string) *types.CommandOutput {
	a.mu.RLock()
	closing := a.closing
	a.mu.RUnlock()
	if closing {
		out := types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, fileTransferClosingError)}
		return &out
	}
	return nil
}

func (a *FileTransferAbility) Command(atom *types.Atom, act string, args any) types.CommandOutput {
	if err := a.Check(atom); err != nil {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, err)}
	}
	switch act {
	case FileTransferCommandSetTarget:
		typed, ok := args.(FileTransferTargetArgs)
		if !ok || strings.TrimSpace(typed.PeerName) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		// 通过 NetMapAbility 验证对端存在
		if _, ok := atom.Ability("NetMapAbility"); !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: NetMapAbility not registered: %w", act, types.ErrMissingDependency)}
		}
		nm, _ := atom.Ability("NetMapAbility")
		if out := nm.Command(atom, NetMapCommandLookupPeer, NetMapLookupPeerArgs{Name: strings.TrimSpace(typed.PeerName)}); out.Err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: target lookup: %w", act, out.Err)}
		}
		a.mu.Lock()
		a.target = strings.TrimSpace(typed.PeerName)
		a.mu.Unlock()
		return types.CommandOutput{Name: act, Value: a.target}
	case FileTransferCommandGetTarget:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		a.mu.RLock()
		defer a.mu.RUnlock()
		return types.CommandOutput{Name: act, Value: a.target}
	case FileTransferCommandUpload, FileTransferCommandDownload:
		if out := a.rejectIfClosing(act); out != nil {
			return *out
		}
		var local, remote, callTarget string
		switch act {
		case FileTransferCommandUpload:
			typed, ok := args.(FileTransferUploadArgs)
			if !ok {
				return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
			}
			local, remote = strings.TrimSpace(typed.LocalPath), strings.TrimSpace(typed.RemotePath)
			callTarget = strings.TrimSpace(typed.Target)
		default:
			typed, ok := args.(FileTransferDownloadArgs)
			if !ok {
				return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
			}
			remote, local = strings.TrimSpace(typed.RemotePath), strings.TrimSpace(typed.LocalPath)
			callTarget = strings.TrimSpace(typed.Target)
		}
		if local == "" || remote == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: empty path: %w", act, types.ErrInvalidArguments)}
		}
		// 解析本次调用的目标对端:优先取本次入参,其次取共享 target。
		// 共享 target 字段仅在 set_target 时变更;per-call target 不会修改它,避免并发竞争。
		a.mu.RLock()
		sharedTarget := a.target
		transport := a.transport
		closing := a.closing
		a.mu.RUnlock()
		if closing {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, fileTransferClosingError)}
		}
		target := callTarget
		if target == "" {
			target = sharedTarget
		}
		if target == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no target set: %w", act, types.ErrInvalidArguments)}
		}
		if callTarget != "" {
			// per-call 目标必须经过 NetMapAbility 验证存在;但不要写回共享 target。
			if _, ok := atom.Ability("NetMapAbility"); !ok {
				return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: NetMapAbility not registered: %w", act, types.ErrMissingDependency)}
			}
			nm, _ := atom.Ability("NetMapAbility")
			if out := nm.Command(atom, NetMapCommandLookupPeer, NetMapLookupPeerArgs{Name: target}); out.Err != nil {
				return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: target lookup: %w", act, out.Err)}
			}
		}
		direction := FileTransferDirectionUpload
		if act == FileTransferCommandDownload {
			direction = FileTransferDirectionDownload
		}
		size := int64(0)
		if direction == FileTransferDirectionUpload {
			if info, err := os.Stat(local); err == nil {
				size = info.Size()
			} else {
				return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: stat local: %w: %v", act, types.ErrInvalidArguments, err)}
			}
		}
		// Reserve worker slot under the write lock so Unmount cannot observe
		// a state where workers are launched after Wait has been entered.
		a.mu.Lock()
		if a.closing {
			a.mu.Unlock()
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, fileTransferClosingError)}
		}
		a.seq++
		id := fmt.Sprintf("tx-%d", a.seq)
		transfer := &FileTransfer{
			ID:         id,
			Direction:  direction,
			Target:     target,
			LocalPath:  local,
			RemotePath: remote,
			Size:       size,
			StartedAt:  time.Now(),
			Status:     FileTransferStatusRunning,
			cancel:     make(chan struct{}),
		}
		a.transfers[id] = transfer
		var launched bool
		if transport != nil {
			a.wg.Add(1)
			a.running.Add(1)
			launched = true
		}
		a.mu.Unlock()

		if !launched {
			// 骨架模式:直接标记完成,但不实际传输字节
			a.mu.Lock()
			transfer.Status = FileTransferStatusCompleted
			transfer.FinishedAt = time.Now()
			a.mu.Unlock()
			return types.CommandOutput{Name: act, Value: transfer.ID}
		}
		go func(t *FileTransfer, tr FileTransferTransport, isUpload bool) {
			defer a.wg.Done()
			defer a.running.Add(-1)
			a.run(t, tr, isUpload)
		}(transfer, transport, act == FileTransferCommandUpload)
		return types.CommandOutput{Name: act, Value: transfer.ID}
	case FileTransferCommandList:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: a.snapshotList()}
	case FileTransferCommandGet:
		typed, ok := args.(FileTransferIDArg)
		if !ok || strings.TrimSpace(typed.ID) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		if t, ok := a.lookup(strings.TrimSpace(typed.ID)); ok {
			return types.CommandOutput{Name: act, Value: t}
		}
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %s not found: %w", act, typed.ID, types.ErrInvalidArguments)}
	case FileTransferCommandCancel:
		typed, ok := args.(FileTransferIDArg)
		if !ok || strings.TrimSpace(typed.ID) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		if !a.cancel(strings.TrimSpace(typed.ID)) {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %s not found or already finished: %w", act, typed.ID, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: typed.ID}
	case FileTransferCommandClearFinished:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: a.clearFinished()}
	}
	return types.CommandOutput{Name: act, Err: fmt.Errorf("command %s: %w", act, types.ErrUnsupportedCommand)}
}

func (a *FileTransferAbility) lookup(id string) (FileTransfer, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	t, ok := a.transfers[id]
	if !ok {
		return FileTransfer{}, false
	}
	return *t, true
}

// Lookup 是 lookup 的对外暴露版本,供同包内的协作 ability(AlgDistAbility)做状态同步,
// 避免在 watcher 中伪造 atom 调用 Command。
func (a *FileTransferAbility) Lookup(id string) (FileTransfer, bool) { return a.lookup(id) }

func (a *FileTransferAbility) snapshotList() []FileTransfer {
	a.mu.RLock()
	out := make([]FileTransfer, 0, len(a.transfers))
	for _, t := range a.transfers {
		out = append(out, *t)
	}
	a.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (a *FileTransferAbility) cancel(id string) bool {
	a.mu.Lock()
	t, ok := a.transfers[id]
	if !ok {
		a.mu.Unlock()
		return false
	}
	if t.Status != FileTransferStatusRunning && t.Status != FileTransferStatusPending {
		a.mu.Unlock()
		return false
	}
	t.Status = FileTransferStatusCanceled
	t.FinishedAt = time.Now()
	closeCh := t.cancel
	a.mu.Unlock()
	if closeCh != nil {
		select {
		case <-closeCh:
		default:
			close(closeCh)
		}
	}
	return true
}

func (a *FileTransferAbility) clearFinished() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for id, t := range a.transfers {
		if t.Status == FileTransferStatusCompleted || t.Status == FileTransferStatusFailed || t.Status == FileTransferStatusCanceled {
			delete(a.transfers, id)
			n++
		}
	}
	return n
}

func (a *FileTransferAbility) run(t *FileTransfer, transport FileTransferTransport, isUpload bool) {
	a.mu.RLock()
	ctx := FileTransferContext{Transfer: *t, cancel: t.cancel}
	a.mu.RUnlock()
	var runErr error
	if isUpload {
		runErr = transport.Upload(ctx)
	} else {
		runErr = transport.Download(ctx)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	cur, ok := a.transfers[t.ID]
	if !ok {
		return
	}
	cur.FinishedAt = time.Now()
	// 若 cancel 通道已被关闭,优先保持 canceled 状态,即使 transport 返回成功。
	select {
	case <-cur.cancel:
		cur.Status = FileTransferStatusCanceled
	default:
		switch {
		case runErr == nil:
			cur.Status = FileTransferStatusCompleted
		case cur.Status == FileTransferStatusCanceled:
			// 已被 cancel
		default:
			cur.Status = FileTransferStatusFailed
			cur.Error = runErr.Error()
		}
	}
}

// Compile-time guarantee that FileTransferAbility satisfies Unmounter.
var _ types.Unmounter = (*FileTransferAbility)(nil)
