// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package ability

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/FasterEdge/FasterEdge/types"
)

const (
	AlgDistCommandRegister       = "register_algorithm"
	AlgDistCommandUnregister     = "unregister_algorithm"
	AlgDistCommandList           = "list_algorithms"
	AlgDistCommandGet            = "get_algorithm"
	AlgDistCommandDistribute     = "distribute"
	AlgDistCommandCancel         = "cancel"
	AlgDistCommandListDistribute = "list_distributions"
	AlgDistCommandClearFinished  = "clear_finished"
)

// AlgDistAlgorithm 描述一个已注册的算法。
type AlgDistAlgorithm struct {
	Name         string
	Version      string
	SourcePath   string
	ContentType  string
	RegisteredAt time.Time
}

// AlgDistStatus 标识分发任务状态。
type AlgDistStatus string

const (
	AlgDistStatusPending   AlgDistStatus = "pending"
	AlgDistStatusRunning   AlgDistStatus = "running"
	AlgDistStatusCompleted AlgDistStatus = "completed"
	AlgDistStatusFailed    AlgDistStatus = "failed"
	AlgDistStatusCanceled  AlgDistStatus = "canceled"
)

// AlgDistJob 描述一次算法分发任务。
type AlgDistJob struct {
	ID         string
	Algorithm  string
	Version    string
	Target     string
	RemotePath string
	StartedAt  time.Time
	FinishedAt time.Time
	Status     AlgDistStatus
	TransferID string // 关联的 FileTransfer ID(若使用底层传输)
	Error      string
}

// AlgDistRegisterArgs 是 register_algorithm 命令的参数。
type AlgDistRegisterArgs struct {
	Name        string
	Version     string
	SourcePath  string
	ContentType string
}

// AlgDistAlgorithmRef 是 unregister / get_algorithm 的通用参数。
type AlgDistAlgorithmRef struct {
	Name    string
	Version string
}

// AlgDistDistributeArgs 是 distribute 命令的参数。
type AlgDistDistributeArgs struct {
	Name       string
	Version    string
	Target     string
	RemotePath string
}

// AlgDistIDArg 是 cancel / clear_finished 用的参数。
type AlgDistIDArg struct {
	ID string
}

// AlgDistAbility 在 FileTransferAbility 之上提供"算法分发"语义。
// 它将算法文件 + 版本元数据关联起来,然后通过 FileTransferAbility 把源文件投递到目标节点。
//
// FileTransferAbility 的解析顺序:
//  1. 构造函数注入(NewAlgDistAbilityWithTransfer)显式提供
//  2. 注册到 atom 的 FileTransferAbility("FileTransferAbility") 实例
//
// 一旦解析成功,该 FileTransferAbility 视为必需依赖,会随 AlgDistAbility 一起被查询/校验。
// 不再创建私有的隐式 FileTransferAbility,以避免状态分叉与共享 target 竞争。
type AlgDistAbility struct {
	mu       sync.RWMutex
	algs     map[string]*AlgDistAlgorithm // key = "name@version"
	jobs     map[string]*AlgDistJob
	transfer *FileTransferAbility
	// pendingTransport 用于在 FileTransferAbility 尚未解析时暂存 transport,resolve 后注入。
	pendingTransport FileTransferTransport
	// watcherWG 跟踪所有进行中的状态同步 watcher,Unmount 时用于等待。
	watcherWG sync.WaitGroup
	seq       uint64
}

// NewAlgDistAbility 创建一个算法分发能力。
// FileTransferAbility 由调用方通过 NewAlgDistAbilityWithTransfer 注入,或在 Command 时由 atom 解析。
// 不再默认构造私有 FileTransferAbility。
func NewAlgDistAbility() *AlgDistAbility {
	return &AlgDistAbility{
		algs: make(map[string]*AlgDistAlgorithm),
		jobs: make(map[string]*AlgDistJob),
	}
}

// NewAlgDistAbilityWithTransfer 注入外部 FileTransferAbility(便于共享传输层)。
// 传 nil 表示完全依赖 atom 中注册的 FileTransferAbility。
func NewAlgDistAbilityWithTransfer(ft *FileTransferAbility) *AlgDistAbility {
	a := &AlgDistAbility{
		algs: make(map[string]*AlgDistAlgorithm),
		jobs: make(map[string]*AlgDistJob),
	}
	if ft != nil {
		a.transfer = ft
	}
	return a
}

// resolveTransfer 返回底层 FileTransferAbility。
// 优先使用构造注入,否则从 atom 解析;两者皆缺失则视为缺少依赖。
// resolve 后,如果有 pendingTransport,会自动注入到 transfer。
func (a *AlgDistAbility) resolveTransfer(atom *types.Atom) (*FileTransferAbility, error) {
	a.mu.RLock()
	ft := a.transfer
	pending := a.pendingTransport
	a.mu.RUnlock()
	if ft != nil {
		if pending != nil {
			ft.SetTransport(pending)
			a.mu.Lock()
			a.pendingTransport = nil
			a.mu.Unlock()
		}
		return ft, nil
	}
	if atom == nil {
		return nil, types.ErrMissingDependency
	}
	ab, ok := atom.Ability("FileTransferAbility")
	if !ok || ab == nil {
		return nil, types.ErrMissingDependency
	}
	ft, ok = ab.(*FileTransferAbility)
	if !ok || ft == nil {
		return nil, fmt.Errorf("%s: registered ability %q is not a FileTransferAbility: %w", "AlgDistAbility", ab.GetName(), types.ErrMissingDependency)
	}
	a.mu.Lock()
	a.transfer = ft
	pending = a.pendingTransport
	a.pendingTransport = nil
	a.mu.Unlock()
	if pending != nil {
		ft.SetTransport(pending)
	}
	return ft, nil
}

// SetTransport 透传到底层 FileTransferAbility。
// 若 FileTransferAbility 尚未解析,transport 暂存,resolveTransfer 命中后自动注入。
func (a *AlgDistAbility) SetTransport(t FileTransferTransport) {
	a.mu.Lock()
	ft := a.transfer
	if ft == nil {
		a.pendingTransport = t
		a.mu.Unlock()
		return
	}
	a.mu.Unlock()
	ft.SetTransport(t)
}

func (a *AlgDistAbility) GetName() string { return "AlgorithmDistributionAbility" }
func (a *AlgDistAbility) Dependencies() []types.Dependency {
	return []types.Dependency{{Kind: types.DependencyData, Name: "BaseData"}, {Kind: types.DependencyData, Name: "NetMapData"}, {Kind: types.DependencyAbility, Name: "FileTransferAbility"}}
}

func (a *AlgDistAbility) Describe() string {
	return "AlgorithmDistributionAbility在FileTransferAbility之上提供算法分发能力:注册/查询算法元数据,把指定版本推送到目标对等节点。"
}

func (a *AlgDistAbility) Check(atom *types.Atom) error {
	if atom == nil {
		return types.ErrMissingDependency
	}
	if _, ok := atom.Data("BaseData"); !ok {
		return types.ErrMissingDependency
	}
	if _, ok := atom.Data("NetMapData"); !ok {
		return types.ErrMissingDependency
	}
	// FileTransferAbility 在 distribute/cancel 时通过 resolveTransfer 严格校验。
	// 这里 Check 做宽松校验:如果 atom 中已注册同名 ability 或构造注入了 transfer,
	// 顺便预解析一次,把 transfer 字段缓存下来,避免每次 distribute 重复解析。
	// 不存在也不在 Check 阶段抛错,留给 resolveTransfer 在实际 distribute/cancel
	// 时返回 ErrMissingDependency。
	if a.transfer == nil && atom != nil {
		if ab, ok := atom.Ability("FileTransferAbility"); ok && ab != nil {
			if ft, isFT := ab.(*FileTransferAbility); isFT && ft != nil {
				a.mu.Lock()
				if a.transfer == nil {
					a.transfer = ft
				}
				pending := a.pendingTransport
				a.pendingTransport = nil
				a.mu.Unlock()
				if pending != nil {
					ft.SetTransport(pending)
				}
			}
		}
	}
	return nil
}

func (a *AlgDistAbility) Mount(atom *types.Atom) error { return a.Check(atom) }

// Unmount 等待所有状态同步 watcher 退出,保证 AlgDistJob 状态收敛。
// isValidAlgDistComponent 校验算法 name/version: 仅字母数字._-, 拒绝 "/" ".."
// 等路径穿越字符与 "@"(键分隔符)等污染字符。
func isValidAlgDistComponent(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// isValidAlgDistRemotePath 校验远端目标路径: 必须绝对路径且无 ".." 组件
// (防远端写路径穿越, 如 name 含 "/" 或 remote 含 "../" 逃出目标目录)。
func isValidAlgDistRemotePath(p string) bool {
	if !strings.HasPrefix(p, "/") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}

func (a *AlgDistAbility) Unmount(_ context.Context, _ *types.Atom) error {
	a.watcherWG.Wait()
	return nil
}

var _ types.Unmounter = (*AlgDistAbility)(nil)

func (a *AlgDistAbility) Command(atom *types.Atom, act string, args any) types.CommandOutput {
	if err := a.Check(atom); err != nil {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, err)}
	}
	switch act {
	case AlgDistCommandRegister:
		typed, ok := args.(AlgDistRegisterArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		name := strings.TrimSpace(typed.Name)
		version := strings.TrimSpace(typed.Version)
		source := strings.TrimSpace(typed.SourcePath)
		if name == "" || version == "" || source == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: name/version/source required: %w", act, types.ErrInvalidArguments)}
		}
		// 组件名白名单: 仅字母数字._-(拒绝 "/" 与 "..", 防远端路径穿越/键污染)
		if !isValidAlgDistComponent(name) || !isValidAlgDistComponent(version) {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: name/version may only contain letters, digits, '.', '_', '-': %w", act, types.ErrInvalidArguments)}
		}
		key := algKey(name, version)
		alg := &AlgDistAlgorithm{
			Name:         name,
			Version:      version,
			SourcePath:   source,
			ContentType:  strings.TrimSpace(typed.ContentType),
			RegisteredAt: time.Now(),
		}
		a.mu.Lock()
		if _, exists := a.algs[key]; exists {
			a.mu.Unlock()
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %s already exists: %w", act, key, types.ErrInvalidArguments)}
		}
		a.algs[key] = alg
		a.mu.Unlock()
		return types.CommandOutput{Name: act, Value: *alg}
	case AlgDistCommandUnregister:
		typed, ok := args.(AlgDistAlgorithmRef)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		name := strings.TrimSpace(typed.Name)
		version := strings.TrimSpace(typed.Version)
		if name == "" || version == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: name/version required: %w", act, types.ErrInvalidArguments)}
		}
		key := algKey(name, version)
		a.mu.Lock()
		alg, ok := a.algs[key]
		if !ok {
			a.mu.Unlock()
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %s not found: %w", act, key, types.ErrInvalidArguments)}
		}
		delete(a.algs, key)
		a.mu.Unlock()
		return types.CommandOutput{Name: act, Value: *alg}
	case AlgDistCommandList:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		a.mu.RLock()
		out := make([]AlgDistAlgorithm, 0, len(a.algs))
		for _, alg := range a.algs {
			out = append(out, *alg)
		}
		a.mu.RUnlock()
		sort.Slice(out, func(i, j int) bool { return out[i].Name+"@"+out[i].Version < out[j].Name+"@"+out[j].Version })
		return types.CommandOutput{Name: act, Value: out}
	case AlgDistCommandGet:
		typed, ok := args.(AlgDistAlgorithmRef)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		if strings.TrimSpace(typed.Name) == "" || strings.TrimSpace(typed.Version) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		a.mu.RLock()
		alg, ok := a.algs[algKey(strings.TrimSpace(typed.Name), strings.TrimSpace(typed.Version))]
		a.mu.RUnlock()
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: not found: %w", act, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: *alg}
	case AlgDistCommandDistribute:
		typed, ok := args.(AlgDistDistributeArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		name := strings.TrimSpace(typed.Name)
		version := strings.TrimSpace(typed.Version)
		target := strings.TrimSpace(typed.Target)
		remote := strings.TrimSpace(typed.RemotePath)
		if name == "" || version == "" || target == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: name/version/target required: %w", act, types.ErrInvalidArguments)}
		}
		if !isValidAlgDistComponent(name) || !isValidAlgDistComponent(version) {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: name/version may only contain letters, digits, '.', '_', '-': %w", act, types.ErrInvalidArguments)}
		}
		if remote == "" {
			remote = "/algorithms/" + name + "-" + version
		} else if !isValidAlgDistRemotePath(remote) {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: remote path must be absolute and contain no '..' components: %w", act, types.ErrInvalidArguments)}
		}
		a.mu.RLock()
		alg, ok := a.algs[algKey(name, version)]
		a.mu.RUnlock()
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: algorithm %s@%s not found: %w", act, name, version, types.ErrInvalidArguments)}
		}
		transfer, err := a.resolveTransfer(atom)
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: resolve transfer: %w", act, err)}
		}
		// 通过 per-call Target 投递,避免写共享 target 字段;这修复了多 target 并发竞争。
		txOut := transfer.Command(atom, FileTransferCommandUpload, FileTransferUploadArgs{
			LocalPath:  alg.SourcePath,
			RemotePath: remote,
			Target:     target,
		})
		if txOut.Err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: transfer: %w", act, txOut.Err)}
		}
		transferID, _ := txOut.Value.(string)
		job := a.newJob(name, version, target, remote, transferID)
		a.storeJob(job)
		// 启动 watcher 把 FileTransfer 终态对齐到 AlgDistJob,异步处理延迟成功/失败/取消。
		a.startJobWatcher(job.ID, transferID)
		return types.CommandOutput{Name: act, Value: job.ID}
	case AlgDistCommandCancel:
		typed, ok := args.(AlgDistIDArg)
		if !ok || strings.TrimSpace(typed.ID) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		if !a.cancelJob(strings.TrimSpace(typed.ID), atom) {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %s not found or already finished: %w", act, typed.ID, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: typed.ID}
	case AlgDistCommandListDistribute:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: a.snapshotJobs()}
	case AlgDistCommandClearFinished:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: a.clearFinished()}
	}
	return types.CommandOutput{Name: act, Err: fmt.Errorf("command %s: %w", act, types.ErrUnsupportedCommand)}
}

func algKey(name, version string) string { return name + "@" + version }

func (a *AlgDistAbility) newJob(name, version, target, remote, transferID string) *AlgDistJob {
	a.mu.Lock()
	a.seq++
	id := fmt.Sprintf("alg-%d", a.seq)
	a.mu.Unlock()
	return &AlgDistJob{
		ID:         id,
		Algorithm:  name,
		Version:    version,
		Target:     target,
		RemotePath: remote,
		StartedAt:  time.Now(),
		Status:     AlgDistStatusRunning,
		TransferID: transferID,
	}
}

func (a *AlgDistAbility) storeJob(j *AlgDistJob) {
	a.mu.Lock()
	a.jobs[j.ID] = j
	a.mu.Unlock()
}

func (a *AlgDistAbility) snapshotJobs() []AlgDistJob {
	a.mu.RLock()
	out := make([]AlgDistJob, 0, len(a.jobs))
	for _, j := range a.jobs {
		out = append(out, *j)
	}
	a.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (a *AlgDistAbility) cancelJob(id string, atom *types.Atom) bool {
	a.mu.Lock()
	j, ok := a.jobs[id]
	if !ok {
		a.mu.Unlock()
		return false
	}
	if j.Status != AlgDistStatusRunning && j.Status != AlgDistStatusPending {
		a.mu.Unlock()
		return false
	}
	transferID := j.TransferID
	a.mu.Unlock()
	transfer, err := a.resolveTransfer(atom)
	if err == nil && transfer != nil && transferID != "" {
		transfer.Command(atom, FileTransferCommandCancel, FileTransferIDArg{ID: transferID})
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	// 二次校验防止并发取消已结束的 job
	if cur, ok := a.jobs[id]; ok {
		if cur.Status != AlgDistStatusRunning && cur.Status != AlgDistStatusPending {
			return false
		}
		cur.Status = AlgDistStatusCanceled
		cur.FinishedAt = time.Now()
		return true
	}
	return false
}

// startJobWatcher 启动一个后台 watcher,把 FileTransfer 终态传播到 AlgDistJob。
// 骨架模式下 transfer 立即处于终态,watcher 也会在下一次轮询中收敛。
func (a *AlgDistAbility) startJobWatcher(jobID, transferID string) {
	if jobID == "" || transferID == "" {
		return
	}
	a.watcherWG.Add(1)
	go func() {
		defer a.watcherWG.Done()
		a.watchJob(jobID, transferID)
	}()
}

// watchJob 周期性拉取 FileTransfer 状态,直到终态对齐到 AlgDistJob。
// 骨架模式下首次拉取即收敛;带 transport 时按轮询间隔收敛。
// 轮询退避策略:50ms → 100ms → 200ms → 500ms,上限 500ms,最长 30s。
func (a *AlgDistAbility) watchJob(jobID, transferID string) {
	const maxBackoff = 500 * time.Millisecond
	const maxTotal = 30 * time.Second
	start := time.Now()
	delay := 50 * time.Millisecond
	for time.Since(start) < maxTotal {
		// 已取消或已完成直接退出
		a.mu.RLock()
		j := a.jobs[jobID]
		var status AlgDistStatus
		if j != nil {
			status = j.Status
		}
		a.mu.RUnlock()
		if j == nil {
			return
		}
		if isTerminalAlgDist(status) {
			return
		}
		// 从当前已解析的 transfer 取状态;不强制使用 atom。
		a.mu.RLock()
		ft := a.transfer
		a.mu.RUnlock()
		if ft == nil {
			time.Sleep(delay)
			if delay < maxBackoff {
				delay *= 2
			}
			continue
		}
		// 通过 Lookup 直接读 FileTransfer 状态,避免伪造 atom 调用 Command。
		transfer, ok := ft.Lookup(transferID)
		if !ok {
			// transfer 不存在 → 视为失败/取消(已清理或被 Unmount 移除)
			a.mu.Lock()
			if cur, ok := a.jobs[jobID]; ok {
				cur.Status = AlgDistStatusFailed
				cur.Error = "underlying transfer missing"
				cur.FinishedAt = time.Now()
			}
			a.mu.Unlock()
			return
		}
		// FileTransfer 终态已达成 → 对齐 AlgDistJob
		if isTerminalFileTransfer(transfer.Status) {
			a.mu.Lock()
			cur, exists := a.jobs[jobID]
			a.mu.Unlock()
			if !exists {
				return
			}
			a.mu.Lock()
			switch transfer.Status {
			case FileTransferStatusCompleted:
				cur.Status = AlgDistStatusCompleted
			case FileTransferStatusFailed:
				cur.Status = AlgDistStatusFailed
				cur.Error = transfer.Error
			case FileTransferStatusCanceled:
				cur.Status = AlgDistStatusCanceled
			}
			cur.FinishedAt = time.Now()
			a.mu.Unlock()
			return
		}
		time.Sleep(delay)
		if delay < maxBackoff {
			delay *= 2
		}
	}
}

// isTerminalAlgDist reports whether s is a final AlgDistStatus.
func isTerminalAlgDist(s AlgDistStatus) bool {
	switch s {
	case AlgDistStatusCompleted, AlgDistStatusFailed, AlgDistStatusCanceled:
		return true
	}
	return false
}

// isTerminalFileTransfer reports whether s is a final FileTransferStatus.
func isTerminalFileTransfer(s FileTransferStatus) bool {
	switch s {
	case FileTransferStatusCompleted, FileTransferStatusFailed, FileTransferStatusCanceled:
		return true
	}
	return false
}

func (a *AlgDistAbility) clearFinished() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for id, j := range a.jobs {
		if j.Status == AlgDistStatusCompleted || j.Status == AlgDistStatusFailed || j.Status == AlgDistStatusCanceled {
			delete(a.jobs, id)
			n++
		}
	}
	return n
}
