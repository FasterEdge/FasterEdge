package ability

import (
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
type AlgDistAbility struct {
	mu       sync.RWMutex
	algs     map[string]*AlgDistAlgorithm // key = "name@version"
	jobs     map[string]*AlgDistJob
	transfer *FileTransferAbility
	seq      uint64
}

func NewAlgDistAbility() *AlgDistAbility {
	return &AlgDistAbility{
		algs:     make(map[string]*AlgDistAlgorithm),
		jobs:     make(map[string]*AlgDistJob),
		transfer: NewFileTransferAbility(),
	}
}

// NewAlgDistAbilityWithTransfer 注入外部 FileTransferAbility(便于共享传输层)。
func NewAlgDistAbilityWithTransfer(ft *FileTransferAbility) *AlgDistAbility {
	if ft == nil {
		ft = NewFileTransferAbility()
	}
	return &AlgDistAbility{
		algs:     make(map[string]*AlgDistAlgorithm),
		jobs:     make(map[string]*AlgDistJob),
		transfer: ft,
	}
}

// SetTransport 透传到底层 FileTransferAbility。
func (a *AlgDistAbility) SetTransport(t FileTransferTransport) {
	a.transfer.SetTransport(t)
}

func (a *AlgDistAbility) GetName() string { return "AlgorithmDistributionAbility" }

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
	return nil
}

func (a *AlgDistAbility) Mount(atom *types.Atom) error { return a.Check(atom) }

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
		if remote == "" {
			remote = "/algorithms/" + name + "-" + version
		}
		a.mu.RLock()
		alg, ok := a.algs[algKey(name, version)]
		a.mu.RUnlock()
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: algorithm %s@%s not found: %w", act, name, version, types.ErrInvalidArguments)}
		}
		// 通过 FileTransferAbility 投递
		if out := a.transfer.Command(atom, FileTransferCommandSetTarget, FileTransferTargetArgs{PeerName: target}); out.Err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: set target: %w", act, out.Err)}
		}
		txOut := a.transfer.Command(atom, FileTransferCommandUpload, FileTransferUploadArgs{
			LocalPath:  alg.SourcePath,
			RemotePath: remote,
		})
		if txOut.Err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: transfer: %w", act, txOut.Err)}
		}
		transferID, _ := txOut.Value.(string)
		job := a.newJob(name, version, target, remote, transferID)
		a.storeJob(job)
		// 骨架模式(无 transport)下,FileTransfer 立即完成,这里直接同步 job 状态。
		getOut := a.transfer.Command(atom, FileTransferCommandGet, FileTransferIDArg{ID: transferID})
		if getOut.Err == nil {
			if tf, ok := getOut.Value.(FileTransfer); ok && tf.Status == FileTransferStatusCompleted {
				a.mu.Lock()
				job.Status = AlgDistStatusCompleted
				job.FinishedAt = time.Now()
				a.mu.Unlock()
			}
		}
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
	if transferID != "" {
		a.transfer.Command(atom, FileTransferCommandCancel, FileTransferIDArg{ID: transferID})
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	j.Status = AlgDistStatusCanceled
	j.FinishedAt = time.Now()
	return true
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
