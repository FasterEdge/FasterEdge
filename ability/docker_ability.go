// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package ability

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/FasterEdge/FasterEdge/types"
)

const (
	DockerCommandSetEndpoint    = "set_endpoint"
	DockerCommandGetEndpoint    = "get_endpoint"
	DockerCommandListContainers = "list_containers"
	DockerCommandStart          = "start_container"
	DockerCommandStop           = "stop_container"
	DockerCommandRestart        = "restart_container"
	DockerCommandRemove         = "remove_container"
	DockerCommandPullImage      = "pull_image"
	DockerCommandInspect        = "inspect_container"
	DockerCommandGetLogs        = "get_logs"
	DockerCommandCreate         = "create_container"

	// dockerMaxStopTimeout 是 stop/restart 的超时上界: 旧实现任意透传
	// (Timeout=24h), 与 CmdAbility 30s 基线不一致——钳到 5 分钟。
	dockerMaxStopTimeout = 5 * time.Minute

	// maxDockerConcurrency 是并发 transport 调用上限(对比 CmdAbility 16):
	// 无闸门时认证调用方可并发发起任意数量 daemon 请求耗尽连接。
	maxDockerConcurrency = 16
)

// DockerContainer 描述一个容器快照。
type DockerContainer struct {
	ID      string
	Name    string
	Image   string
	State   string
	Status  string
	Created time.Time
}

// DockerImage 描述一个镜像。
type DockerImage struct {
	RepoTags []string
	Size     int64
	Created  time.Time
}

// DockerEndpointArgs 是 set_endpoint 的参数。
type DockerEndpointArgs struct {
	URL string // unix:///var/run/docker.sock 或 tcp://host:2375
}

// DockerContainerArgs 是 start/stop/restart/remove/inspect/logs 的参数。
type DockerContainerArgs struct {
	IDOrName string
	Timeout  time.Duration
}

// DockerPullImageArgs 是 pull_image 的参数。
type DockerPullImageArgs struct {
	Reference string
}

// DockerCreateArgs 是 create_container 的参数。
type DockerCreateArgs struct {
	Name    string
	Image   string
	Command []string
	Env     []string
	Ports   []string
}

// DockerTransport 抽象出真实 Docker 客户端。
type DockerTransport interface {
	List(all bool) ([]DockerContainer, error)
	Start(idOrName string) error
	Stop(idOrName string, timeout time.Duration) error
	Restart(idOrName string, timeout time.Duration) error
	Remove(idOrName string, force bool) error
	Pull(reference string) error
	Inspect(idOrName string) (DockerContainer, error)
	Logs(idOrName string, tail int) (string, error)
	Create(args DockerCreateArgs) (string, error)
}

// DockerAbility 在 Transport 之上提供容器生命周期管理。
type DockerAbility struct {
	mu        sync.RWMutex
	endpoint  string
	transport DockerTransport
	running   atomic.Int32 // 并发 transport 调用闸门
}

func NewDockerAbility() *DockerAbility { return &DockerAbility{} }

func (d *DockerAbility) SetTransport(t DockerTransport) {
	d.mu.Lock()
	d.transport = t
	d.mu.Unlock()
}

func (d *DockerAbility) GetName() string { return "DockerAbility" }

func (d *DockerAbility) Describe() string {
	return "DockerAbility提供容器管理能力:list/start/stop/restart/remove/pull/inspect/logs/create,通过注入的 Transport 桥接真实 Docker daemon。"
}

func (d *DockerAbility) Check(atom *types.Atom) error {
	if atom == nil {
		return types.ErrMissingDependency
	}
	if _, ok := atom.Data("BaseData"); !ok {
		return types.ErrMissingDependency
	}
	return nil
}

func (d *DockerAbility) Mount(atom *types.Atom) error { return d.Check(atom) }

// Compile-time guarantee that DockerAbility satisfies CommandLister.
var _ types.CommandLister = (*DockerAbility)(nil)

// dockerAbilityCommands 是 DockerAbility 的命令目录, 与 Command 中 act 的
// switch 分支逐一对应(由 command_contract_test.go 解析源码交叉核对):
// 端点 2 条, list 1 条, 容器动作 6 条, 镜像 1 条, 创建 1 条。
var dockerAbilityCommands = []string{
	DockerCommandSetEndpoint,
	DockerCommandGetEndpoint,
	DockerCommandListContainers,
	DockerCommandStart,
	DockerCommandStop,
	DockerCommandRestart,
	DockerCommandRemove,
	DockerCommandInspect,
	DockerCommandGetLogs,
	DockerCommandPullImage,
	DockerCommandCreate,
}

// ListCommands 实现 types.CommandLister, 返回命令目录的副本(调用方持有所有权)。
func (d *DockerAbility) ListCommands() []string {
	return append([]string(nil), dockerAbilityCommands...)
}

func (d *DockerAbility) Command(atom *types.Atom, act string, args any) types.CommandOutput {
	if err := d.Check(atom); err != nil {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, err)}
	}
	switch act {
	case DockerCommandSetEndpoint:
		typed, ok := args.(DockerEndpointArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		url := strings.TrimSpace(typed.URL)
		if !isValidDockerEndpoint(url) {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: invalid endpoint: %w", act, types.ErrInvalidArguments)}
		}
		d.mu.Lock()
		d.endpoint = url
		d.mu.Unlock()
		return types.CommandOutput{Name: act, Value: url}
	case DockerCommandGetEndpoint:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		d.mu.RLock()
		defer d.mu.RUnlock()
		return types.CommandOutput{Name: act, Value: d.endpoint}
	case DockerCommandListContainers:
		all := false
		if args != nil {
			typed, ok := args.(struct{ All bool })
			if !ok {
				return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
			}
			all = typed.All
		}
		d.mu.RLock()
		transport := d.transport
		d.mu.RUnlock()
		if transport == nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no transport: %w", act, types.ErrInvalidArguments)}
		}
		if out := d.dockerBegin(act); out.Err != nil {
			return out
		}
		defer d.dockerEnd()
		cs, err := transport.List(all)
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %w", act, types.ErrOperationFailed, err)}
		}
		sort.Slice(cs, func(i, j int) bool { return cs[i].Name < cs[j].Name })
		return types.CommandOutput{Name: act, Value: cs}
	case DockerCommandStart, DockerCommandStop, DockerCommandRestart, DockerCommandRemove, DockerCommandInspect, DockerCommandGetLogs:
		return d.simpleContainerAction(act, args)
	case DockerCommandPullImage:
		typed, ok := args.(DockerPullImageArgs)
		if !ok || strings.TrimSpace(typed.Reference) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		ref := strings.TrimSpace(typed.Reference)
		// 镜像引用字符集白名单: 拒绝 "nginx&tag=1.2.3" 类 query 注入
		// (transport 把 reference 拼进 fromImage query, 未编码时 & 可注入
		// 额外参数、# 可截断——拉取与审计记录不符的镜像)。
		if !isValidDockerImageRef(ref) {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: invalid image reference: %w", act, types.ErrInvalidArguments)}
		}
		d.mu.RLock()
		transport := d.transport
		d.mu.RUnlock()
		if transport == nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no transport: %w", act, types.ErrInvalidArguments)}
		}
		if out := d.dockerBegin(act); out.Err != nil {
			return out
		}
		defer d.dockerEnd()
		if err := transport.Pull(ref); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %w", act, types.ErrOperationFailed, err)}
		}
		return types.CommandOutput{Name: act, Value: ref}
	case DockerCommandCreate:
		typed, ok := args.(DockerCreateArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		if strings.TrimSpace(typed.Image) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: image required: %w", act, types.ErrInvalidArguments)}
		}
		// 与 pull_image / simpleContainerAction 一致: create 的镜像引用与
		// 容器名同样走白名单——旧实现仅查 Image 非空, 同一能力内校验不一致
		// (JSON body 无 query 注入面, 但畸形引用/名称应尽早以参数错误拒绝)。
		if !isValidDockerImageRef(strings.TrimSpace(typed.Image)) {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: invalid image reference: %w", act, types.ErrInvalidArguments)}
		}
		if typed.Name != "" && !isValidDockerIDOrName(typed.Name) {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: invalid container name: %w", act, types.ErrInvalidArguments)}
		}
		// 端口声明校验: "host:container" 或 "container", 端口 1-65535 且为纯数字。
		// 旧实现任意透传("99999"/"0"/"host:80:extra" 均可), 可绑定任意宿主
		// 端口(含 22)构成宿主逃逸面。
		for _, spec := range typed.Ports {
			if !isValidDockerPortSpec(spec) {
				return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: invalid port spec %q: %w", act, spec, types.ErrInvalidArguments)}
			}
		}
		d.mu.RLock()
		transport := d.transport
		d.mu.RUnlock()
		if transport == nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no transport: %w", act, types.ErrInvalidArguments)}
		}
		if out := d.dockerBegin(act); out.Err != nil {
			return out
		}
		defer d.dockerEnd()
		id, err := transport.Create(typed)
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %w", act, types.ErrOperationFailed, err)}
		}
		return types.CommandOutput{Name: act, Value: id}
	}
	return types.CommandOutput{Name: act, Err: fmt.Errorf("command %s: %w", act, types.ErrUnsupportedCommand)}
}

func (d *DockerAbility) simpleContainerAction(act string, args any) types.CommandOutput {
	typed, ok := args.(DockerContainerArgs)
	if !ok || strings.TrimSpace(typed.IDOrName) == "" {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
	}
	id := strings.TrimSpace(typed.IDOrName)
	// 名称/ID 白名单: docker 名称规则 [a-zA-Z0-9][a-zA-Z0-9_.-]*。旧实现原样
	// 透传 transport——transport 把它拼进 URL path, "../volumes/myapp-data"
	// 经 daemon 路径清理成 /volumes/... 即删除命名卷(数据丢失), 同理可删
	// 镜像/网络。白名单直接拒绝所有穿越与 query 注入面。
	if !isValidDockerIDOrName(id) {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: invalid id or name: %w", act, types.ErrInvalidArguments)}
	}
	d.mu.RLock()
	transport := d.transport
	d.mu.RUnlock()
	if transport == nil {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no transport: %w", act, types.ErrInvalidArguments)}
	}
	if out := d.dockerBegin(act); out.Err != nil {
		return out
	}
	defer d.dockerEnd()
	switch act {
	case DockerCommandStart:
		if err := transport.Start(id); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %w", act, types.ErrOperationFailed, err)}
		}
		return types.CommandOutput{Name: act, Value: id}
	case DockerCommandStop:
		// 超时钳制: 24h 类输入会指示 daemon 长时间挂起容器
		if typed.Timeout > dockerMaxStopTimeout {
			typed.Timeout = dockerMaxStopTimeout
		}
		if err := transport.Stop(id, typed.Timeout); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %w", act, types.ErrOperationFailed, err)}
		}
		return types.CommandOutput{Name: act, Value: id}
	case DockerCommandRestart:
		if typed.Timeout > dockerMaxStopTimeout {
			typed.Timeout = dockerMaxStopTimeout
		}
		if err := transport.Restart(id, typed.Timeout); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %w", act, types.ErrOperationFailed, err)}
		}
		return types.CommandOutput{Name: act, Value: id}
	case DockerCommandRemove:
		if err := transport.Remove(id, false); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %w", act, types.ErrOperationFailed, err)}
		}
		return types.CommandOutput{Name: act, Value: id}
	case DockerCommandInspect:
		c, err := transport.Inspect(id)
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %w", act, types.ErrOperationFailed, err)}
		}
		return types.CommandOutput{Name: act, Value: c}
	case DockerCommandGetLogs:
		logs, err := transport.Logs(id, 100)
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %w", act, types.ErrOperationFailed, err)}
		}
		return types.CommandOutput{Name: act, Value: logs}
	}
	return types.CommandOutput{Name: act, Err: fmt.Errorf("command %s: %w", act, types.ErrUnsupportedCommand)}
}

// isValidDockerEndpoint 只做**语法**把关: 协议白名单(unix/tcp/tls)、
// userinfo 拒绝、端口/空白纪律。地址语义(回环/私网/链路本地/组播/十进制写法)
// 全部委托 ability/address_policy.go 的 dockerEndpointPolicy
// (等级 PublicOnly: daemon API 通常跨主机或经公网暴露, 私网一并拒绝,
// 与 InfluxDBData/ekuiper 的历史策略一致), 避免"每个能力一套判定"。
// 注意这里不再做存储期 DNS 解析: 旧实现的 3s LookupHost 只挡住"解析时
// 恰为回环"的名字, 挡不住 DNS rebinding(校验与拨号两次解析可不同),
// 且让 set_endpoint 白白阻塞最多 3 秒。真正的防线在拨号层:
// AddressGuard.DialContext 对解析出的实际 IP 逐个二次校验。
func isValidDockerEndpoint(u string) bool {
	if u == "" {
		return false
	}
	lower := strings.ToLower(u)
	if strings.HasPrefix(lower, "unix://") {
		return true
	}
	if !strings.HasPrefix(lower, "tcp://") && !strings.HasPrefix(lower, "tls://") {
		return false
	}
	// 标准解析: 拒绝畸形 URL/带 userinfo/无端口, 并按 Hostname() 取主机
	// (tcp://alice@127.0.0.1:2375 的 Hostname 是 127.0.0.1, 旧实现按冒号
	// 切分会得到 "alice@127.0.0.1", 回环名单整个失配)。
	parsed, err := url.Parse(u)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Port() == "" {
		return false
	}
	host := parsed.Hostname()
	// IPv6 字面量 Hostname() 带方括号([::1]), 剥掉再交策略判定
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") && len(host) >= 2 {
		host = host[1 : len(host)-1]
	}
	return dockerEndpointPolicy.CheckHost(host) == nil
}

// dockerEndpointPolicy 是 Docker daemon 端点的策略等级(PublicOnly)。
var dockerEndpointPolicy = NewAddressGuard(AddressPolicyPublicOnly)

// dockerBegin/dockerEnd 是并发 transport 调用闸门(对比 CmdAbility 16 基线)。
// 原子 Add 后比较: 旧实现 Load+Add 之间有窗口, 突发并发可临时超上限
// (限流是硬的——与 CmdAbility 的 running.Add(1) > maxConc 同型)。
func (d *DockerAbility) dockerBegin(act string) types.CommandOutput {
	if d.running.Add(1) > maxDockerConcurrency {
		d.running.Add(-1)
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: too many concurrent calls: %w", act, types.ErrInvalidArguments)}
	}
	return types.CommandOutput{}
}

func (d *DockerAbility) dockerEnd() {
	d.running.Add(-1)
}

// isValidDockerIDOrName 校验容器 ID/名称: docker 名称规则
// [a-zA-Z0-9][a-zA-Z0-9_.-]*。拒绝 / \ .. 与 query 分隔符——
// 该值会被 transport 拼进 URL path/query, 白名单是路径穿越删卷的第一道闸。
func isValidDockerIDOrName(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '_' || r == '.' || r == '-' {
			continue
		}
		return false
	}
	return true
}

// isValidDockerImageRef 校验镜像引用字符集: [a-zA-Z0-9._/-:@]。
// registry/path/tag/digest 字符集满足; "&" "?" "#" "=" " " 拒绝——
// 防 query 参数注入(transport 把 reference 拼进 fromImage)。
func isValidDockerImageRef(s string) bool {
	if s == "" || len(s) > 512 {
		return false
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '.' || r == '_' || r == '/' || r == ':' || r == '@' || r == '-' {
			continue
		}
		return false
	}
	return true
}

// isValidDockerPortSpec 校验 "host:container" 或 "container" 端口声明:
// 每段为 1-65535 的纯数字, 至多两段。禁 0/负值/超长/多余段——
// 防任意宿主端口绑定(含 22)与畸形请求。
func isValidDockerPortSpec(s string) bool {
	parts := strings.Split(s, ":")
	if len(parts) < 1 || len(parts) > 2 {
		return false
	}
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	return true
}
