package ability

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
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
type FileTransferUploadArgs struct {
	LocalPath  string
	RemotePath string
}

// FileTransferDownloadArgs 是 download 命令的参数。
type FileTransferDownloadArgs struct {
	RemotePath string
	LocalPath  string
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

// FileTransferAbility 管理文件传输的元数据与任务调度,实际字节流由注入的 Transport 完成。
// 依赖:BaseData + NetMapData(用于解析对等节点地址)。
type FileTransferAbility struct {
	mu        sync.RWMutex
	target    string
	transfers map[string]*FileTransfer
	transport FileTransferTransport
	seq       uint64
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
		var local, remote string
		switch act {
		case FileTransferCommandUpload:
			typed, ok := args.(FileTransferUploadArgs)
			if !ok {
				return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
			}
			local, remote = strings.TrimSpace(typed.LocalPath), strings.TrimSpace(typed.RemotePath)
		default:
			typed, ok := args.(FileTransferDownloadArgs)
			if !ok {
				return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
			}
			remote, local = strings.TrimSpace(typed.RemotePath), strings.TrimSpace(typed.LocalPath)
		}
		if local == "" || remote == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: empty path: %w", act, types.ErrInvalidArguments)}
		}
		a.mu.RLock()
		target := a.target
		transport := a.transport
		a.mu.RUnlock()
		if target == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no target set: %w", act, types.ErrInvalidArguments)}
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
		transfer := a.newTransfer(direction, target, local, remote, size)
		if transport == nil {
			// 骨架模式:直接标记完成,但不实际传输字节
			transfer.Status = FileTransferStatusCompleted
			transfer.FinishedAt = time.Now()
			a.store(transfer)
			return types.CommandOutput{Name: act, Value: transfer.ID}
		}
		a.store(transfer)
		go a.run(transfer, transport, act == FileTransferCommandUpload)
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

func (a *FileTransferAbility) newTransfer(direction FileTransferDirection, target, local, remote string, size int64) *FileTransfer {
	a.mu.Lock()
	a.seq++
	id := fmt.Sprintf("tx-%d", a.seq)
	a.mu.Unlock()
	return &FileTransfer{
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
}

func (a *FileTransferAbility) store(t *FileTransfer) {
	a.mu.Lock()
	a.transfers[t.ID] = t
	a.mu.Unlock()
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
