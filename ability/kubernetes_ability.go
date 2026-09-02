// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
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
	K8sCommandSetContext = "set_context"
	K8sCommandGetContext = "get_context"
	K8sCommandApply      = "apply"
	K8sCommandDelete     = "delete"
	K8sCommandList       = "list"
	K8sCommandGet        = "get"
	K8sCommandScale      = "scale"
	K8sCommandGetLogs    = "get_logs"
)

// K8sContext 描述 K8s 集群上下文。
type K8sContext struct {
	Cluster    string
	Namespace  string
	Kubeconfig string
}

// K8sContextArgs 是 set_context 的参数。
type K8sContextArgs struct {
	K8sContext
}

// K8sApplyArgs 是 apply 的参数。
type K8sApplyArgs struct {
	Manifest string
}

// K8sDeleteArgs 是 delete 的参数。
type K8sDeleteArgs struct {
	Kind      string
	Name      string
	Namespace string
}

// K8sListArgs 是 list 的参数。
type K8sListArgs struct {
	Kind      string
	Namespace string
}

// K8sGetArgs 是 get 的参数。
type K8sGetArgs struct {
	Kind      string
	Name      string
	Namespace string
}

// K8sScaleArgs 是 scale 的参数。
type K8sScaleArgs struct {
	Deployment string
	Namespace  string
	Replicas   int32
}

// K8sLogsArgs 是 get_logs 的参数。
type K8sLogsArgs struct {
	Pod       string
	Namespace string
	Tail      int
}

// K8sResource 描述 K8s 资源快照。
type K8sResource struct {
	Kind      string
	Name      string
	Namespace string
	CreatedAt time.Time
	Status    map[string]string
}

// K8sTransport 抽象出真实 K8s 客户端(client-go 或 rest mapper)。
type K8sTransport interface {
	Apply(manifest string) error
	Delete(kind, name, namespace string) error
	List(kind, namespace string) ([]K8sResource, error)
	Get(kind, name, namespace string) (K8sResource, error)
	Scale(deployment, namespace string, replicas int32) error
	Logs(pod, namespace string, tail int) (string, error)
}

// K8sAbility 在 Transport 之上提供 K8s 资源管理。
type K8sAbility struct {
	mu        sync.RWMutex
	ctx       K8sContext
	transport K8sTransport
}

func NewK8sAbility() *K8sAbility { return &K8sAbility{} }

func (k *K8sAbility) SetTransport(t K8sTransport) {
	k.mu.Lock()
	k.transport = t
	k.mu.Unlock()
}

func (k *K8sAbility) GetName() string { return "KubernetesAbility" }

func (k *K8sAbility) Describe() string {
	return "KubernetesAbility提供 K8s 资源管理能力:apply/delete/list/get/scale/get_logs,通过注入的 Transport 桥接真实 K8s API。"
}

func (k *K8sAbility) Check(atom *types.Atom) error {
	if atom == nil {
		return types.ErrMissingDependency
	}
	if _, ok := atom.Data("BaseData"); !ok {
		return types.ErrMissingDependency
	}
	return nil
}

func (k *K8sAbility) Mount(atom *types.Atom) error { return k.Check(atom) }

func (k *K8sAbility) Command(atom *types.Atom, act string, args any) types.CommandOutput {
	if err := k.Check(atom); err != nil {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, err)}
	}
	switch act {
	case K8sCommandSetContext:
		typed, ok := args.(K8sContextArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		if strings.TrimSpace(typed.Cluster) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: cluster required: %w", act, types.ErrInvalidArguments)}
		}
		if strings.TrimSpace(typed.Namespace) == "" {
			typed.Namespace = "default"
		}
		k.mu.Lock()
		k.ctx = K8sContext{Cluster: strings.TrimSpace(typed.Cluster), Namespace: typed.Namespace, Kubeconfig: typed.Kubeconfig}
		k.mu.Unlock()
		return types.CommandOutput{Name: act, Value: k.ctx}
	case K8sCommandGetContext:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		k.mu.RLock()
		defer k.mu.RUnlock()
		return types.CommandOutput{Name: act, Value: k.ctx}
	case K8sCommandApply:
		typed, ok := args.(K8sApplyArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		if strings.TrimSpace(typed.Manifest) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: empty manifest: %w", act, types.ErrInvalidArguments)}
		}
		k.mu.RLock()
		transport := k.transport
		k.mu.RUnlock()
		if transport == nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no transport: %w", act, types.ErrInvalidArguments)}
		}
		if err := transport.Apply(typed.Manifest); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %v", act, types.ErrInvalidArguments, err)}
		}
		return types.CommandOutput{Name: act, Value: true}
	case K8sCommandDelete:
		typed, ok := args.(K8sDeleteArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		if !isValidK8sKind(typed.Kind) || strings.TrimSpace(typed.Name) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: invalid kind/name: %w", act, types.ErrInvalidArguments)}
		}
		ns := strings.TrimSpace(typed.Namespace)
		if ns == "" {
			ns = k.currentNamespace()
		}
		k.mu.RLock()
		transport := k.transport
		k.mu.RUnlock()
		if transport == nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no transport: %w", act, types.ErrInvalidArguments)}
		}
		if err := transport.Delete(strings.TrimSpace(typed.Kind), strings.TrimSpace(typed.Name), ns); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %v", act, types.ErrInvalidArguments, err)}
		}
		return types.CommandOutput{Name: act, Value: typed.Name}
	case K8sCommandList:
		typed, ok := args.(K8sListArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		if !isValidK8sKind(typed.Kind) {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: invalid kind: %w", act, types.ErrInvalidArguments)}
		}
		ns := strings.TrimSpace(typed.Namespace)
		if ns == "" {
			ns = k.currentNamespace()
		}
		k.mu.RLock()
		transport := k.transport
		k.mu.RUnlock()
		if transport == nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no transport: %w", act, types.ErrInvalidArguments)}
		}
		rs, err := transport.List(strings.TrimSpace(typed.Kind), ns)
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %v", act, types.ErrInvalidArguments, err)}
		}
		sort.Slice(rs, func(i, j int) bool { return rs[i].Name < rs[j].Name })
		return types.CommandOutput{Name: act, Value: rs}
	case K8sCommandGet:
		typed, ok := args.(K8sGetArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		if !isValidK8sKind(typed.Kind) || strings.TrimSpace(typed.Name) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: invalid kind/name: %w", act, types.ErrInvalidArguments)}
		}
		ns := strings.TrimSpace(typed.Namespace)
		if ns == "" {
			ns = k.currentNamespace()
		}
		k.mu.RLock()
		transport := k.transport
		k.mu.RUnlock()
		if transport == nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no transport: %w", act, types.ErrInvalidArguments)}
		}
		r, err := transport.Get(strings.TrimSpace(typed.Kind), strings.TrimSpace(typed.Name), ns)
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %v", act, types.ErrInvalidArguments, err)}
		}
		return types.CommandOutput{Name: act, Value: r}
	case K8sCommandScale:
		typed, ok := args.(K8sScaleArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		if strings.TrimSpace(typed.Deployment) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: deployment required: %w", act, types.ErrInvalidArguments)}
		}
		if typed.Replicas < 0 {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: replicas must be non-negative: %w", act, types.ErrInvalidArguments)}
		}
		ns := strings.TrimSpace(typed.Namespace)
		if ns == "" {
			ns = k.currentNamespace()
		}
		k.mu.RLock()
		transport := k.transport
		k.mu.RUnlock()
		if transport == nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no transport: %w", act, types.ErrInvalidArguments)}
		}
		if err := transport.Scale(strings.TrimSpace(typed.Deployment), ns, typed.Replicas); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %v", act, types.ErrInvalidArguments, err)}
		}
		return types.CommandOutput{Name: act, Value: typed.Replicas}
	case K8sCommandGetLogs:
		typed, ok := args.(K8sLogsArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		if strings.TrimSpace(typed.Pod) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: pod required: %w", act, types.ErrInvalidArguments)}
		}
		if typed.Tail < 0 {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: tail must be non-negative: %w", act, types.ErrInvalidArguments)}
		}
		ns := strings.TrimSpace(typed.Namespace)
		if ns == "" {
			ns = k.currentNamespace()
		}
		k.mu.RLock()
		transport := k.transport
		k.mu.RUnlock()
		if transport == nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no transport: %w", act, types.ErrInvalidArguments)}
		}
		logs, err := transport.Logs(strings.TrimSpace(typed.Pod), ns, typed.Tail)
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %v", act, types.ErrInvalidArguments, err)}
		}
		return types.CommandOutput{Name: act, Value: logs}
	}
	return types.CommandOutput{Name: act, Err: fmt.Errorf("command %s: %w", act, types.ErrUnsupportedCommand)}
}

func (k *K8sAbility) currentNamespace() string {
	k.mu.RLock()
	defer k.mu.RUnlock()
	if k.ctx.Namespace == "" {
		return "default"
	}
	return k.ctx.Namespace
}

func isValidK8sKind(s string) bool {
	if s == "" {
		return false
	}
	parts := strings.Split(s, "")
	if len(parts) == 0 {
		return false
	}
	// 至少一个字母数字,允许 - . ,不能是纯符号
	hasAlpha := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			hasAlpha = true
		} else if r == '-' || r == '.' {
			continue
		} else {
			return false
		}
	}
	return hasAlpha
}
