// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package ability

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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

	// k8sMaxLogTail 是 get_logs 的日志行数上界,防 transport 无限拉取。
	k8sMaxLogTail = 1000

	// k8sMaxPayloadBytes 是 set_context 的 kubeconfig 与 apply 的 manifest
	// 的字节上限(对齐 CmdAbility 1MiB 输出上限): 防认证调用方灌入任意大
	// manifest/kubeconfig 常驻内存放大。
	k8sMaxPayloadBytes = 1 << 20

	// k8sMaxOutputBytes 是 get_logs/list/get 返回值的字节上限(截断阈值)。
	k8sMaxOutputBytes = 1 << 20

	// k8sCallTimeout 是单次 transport 调用的超时(对比 CmdAbility 30s 基线):
	// 慢 API server 挂起时旧实现永久阻塞调用方 goroutine。
	k8sCallTimeout = 30 * time.Second

	// maxK8sConcurrency 是并发 transport 调用上限(对比 CmdAbility 16 基线):
	// 无闸门时认证调用方可并发发起任意数量挂起请求耗尽 goroutine/连接。
	maxK8sConcurrency = 16
)

// K8sContext 描述 K8s 集群上下文。
type K8sContext struct {
	Cluster    string
	Namespace  string
	Kubeconfig string
}

// K8sContextView 是 get_context/set_context 对外暴露的安全视图:
// Kubeconfig 原文(通常含 client-certificate-data/client-key-data/token)永不回读。
type K8sContextView struct {
	Cluster              string
	Namespace            string
	KubeconfigConfigured bool
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
// 所有方法接收 ctx: 调用方对每次调用设 30s 超时, 进行中的请求可被中断——
// 旧接口无 ctx 参数, API server 挂起时调用永久阻塞。
type K8sTransport interface {
	Apply(ctx context.Context, manifest string) error
	Delete(ctx context.Context, kind, name, namespace string) error
	List(ctx context.Context, kind, namespace string) ([]K8sResource, error)
	Get(ctx context.Context, kind, name, namespace string) (K8sResource, error)
	Scale(ctx context.Context, deployment, namespace string, replicas int32) error
	Logs(ctx context.Context, pod, namespace string, tail int) (string, error)
}

// K8sAbility 在 Transport 之上提供 K8s 资源管理。
type K8sAbility struct {
	mu        sync.RWMutex
	ctx       K8sContext
	transport K8sTransport
	running   atomic.Int32 // 并发 transport 调用闸门
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
		ns := strings.TrimSpace(typed.Namespace)
		if ns == "" {
			ns = "default"
		}
		if len(typed.Kubeconfig) > k8sMaxPayloadBytes {
			// 超大 kubeconfig 常驻内存, 只能被下次 set_context 覆盖——设上限
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: kubeconfig exceeds %d bytes: %w", act, k8sMaxPayloadBytes, types.ErrInvalidArguments)}
		}
		// 本地变量构造: 写锁内赋值、放锁后只读本地值——旧实现放锁后回读
		// k.ctx 字段, 与并发 set_context 的整体替换构成数据竞争。
		ctx := K8sContext{Cluster: strings.TrimSpace(typed.Cluster), Namespace: ns, Kubeconfig: typed.Kubeconfig}
		k.mu.Lock()
		k.ctx = ctx
		k.mu.Unlock()
		return types.CommandOutput{Name: act, Value: K8sContextView{
			Cluster:              ctx.Cluster,
			Namespace:            ctx.Namespace,
			KubeconfigConfigured: strings.TrimSpace(ctx.Kubeconfig) != "",
		}}
	case K8sCommandGetContext:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		k.mu.RLock()
		defer k.mu.RUnlock()
		return types.CommandOutput{Name: act, Value: K8sContextView{
			Cluster:              k.ctx.Cluster,
			Namespace:            k.ctx.Namespace,
			KubeconfigConfigured: strings.TrimSpace(k.ctx.Kubeconfig) != "",
		}}
	case K8sCommandApply:
		typed, ok := args.(K8sApplyArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		if strings.TrimSpace(typed.Manifest) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: empty manifest: %w", act, types.ErrInvalidArguments)}
		}
		if len(typed.Manifest) > k8sMaxPayloadBytes {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: manifest exceeds %d bytes: %w", act, k8sMaxPayloadBytes, types.ErrInvalidArguments)}
		}
		k.mu.RLock()
		transport := k.transport
		k.mu.RUnlock()
		if transport == nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no transport: %w", act, types.ErrInvalidArguments)}
		}
		ctx, done, reject := k.k8sBegin(act)
		if reject.Err != nil {
			return reject
		}
		defer done()
		if err := transport.Apply(ctx, typed.Manifest); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %w", act, types.ErrOperationFailed, err)}
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
		ctx, done, reject := k.k8sBegin(act)
		if reject.Err != nil {
			return reject
		}
		defer done()
		if err := transport.Delete(ctx, strings.TrimSpace(typed.Kind), strings.TrimSpace(typed.Name), ns); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %w", act, types.ErrOperationFailed, err)}
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
		ctx, done, reject := k.k8sBegin(act)
		if reject.Err != nil {
			return reject
		}
		defer done()
		rs, err := transport.List(ctx, strings.TrimSpace(typed.Kind), ns)
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %w", act, types.ErrOperationFailed, err)}
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
		ctx, done, reject := k.k8sBegin(act)
		if reject.Err != nil {
			return reject
		}
		defer done()
		r, err := transport.Get(ctx, strings.TrimSpace(typed.Kind), strings.TrimSpace(typed.Name), ns)
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %w", act, types.ErrOperationFailed, err)}
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
		ctx, done, reject := k.k8sBegin(act)
		if reject.Err != nil {
			return reject
		}
		defer done()
		if err := transport.Scale(ctx, strings.TrimSpace(typed.Deployment), ns, typed.Replicas); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %w", act, types.ErrOperationFailed, err)}
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
		// Tail 钳制上界: 避免 0(或任意大值)被 transport 解释为"全部日志"导致
		// 无限日志拉取(内存/带宽 DoS)。默认取 100 行,与 docker 侧一致。
		tail := typed.Tail
		if tail == 0 {
			tail = 100
		}
		if tail > k8sMaxLogTail {
			tail = k8sMaxLogTail
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
		ctx, done, reject := k.k8sBegin(act)
		if reject.Err != nil {
			return reject
		}
		defer done()
		logs, err := transport.Logs(ctx, strings.TrimSpace(typed.Pod), ns, tail)
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %w", act, types.ErrOperationFailed, err)}
		}
		// 字节截断: Tail 只限行数, 单行日志(如 base64 blob)可任意长——
		// 返回值钳到 1MiB, 防内存/回显放大。
		if len(logs) > k8sMaxOutputBytes {
			logs = logs[:k8sMaxOutputBytes]
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

// k8sBegin 为一次 transport 调用分配并发闸门与 30s 超时 ctx。
// 调用方必须在调用结束后执行返回的 done()(defer 即可)。
// 原子 Add 后比较: 旧实现 Load+Add 之间有窗口, 突发并发可临时超上限。
func (k *K8sAbility) k8sBegin(act string) (context.Context, func(), types.CommandOutput) {
	if k.running.Add(1) > maxK8sConcurrency {
		k.running.Add(-1)
		return nil, nil, types.CommandOutput{Name: act, Err: fmt.Errorf("%s: too many concurrent calls: %w", act, types.ErrInvalidArguments)}
	}
	ctx, cancel := context.WithTimeout(context.Background(), k8sCallTimeout)
	return ctx, func() { cancel(); k.running.Add(-1) }, types.CommandOutput{}
}

func isValidK8sKind(s string) bool {
	if s == "" || len(s) > 253 {
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
