// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package ability

import (
	"fmt"
	"net"
	"net/netip"
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

func isValidDockerEndpoint(u string) bool {
	if u == "" {
		return false
	}
	lower := strings.ToLower(u)
	if strings.HasPrefix(lower, "unix://") {
		return true
	}
	if strings.HasPrefix(lower, "tcp://") || strings.HasPrefix(lower, "tls://") {
		rest := u
		if i := strings.Index(lower, "://"); i >= 0 {
			rest = u[i+3:]
		}
		host := rest
		if i := strings.IndexAny(rest, "/?#"); i >= 0 {
			host = rest[:i]
		}
		if i := strings.LastIndex(host, ":"); i >= 0 {
			host = host[:i]
		}
		if host == "" {
			return false
		}
		// userinfo 防护: 标准 URL 解析后 Hostname() 不含 userinfo
		// (tcp://alice@127.0.0.1:2375 的 Hostname 是 127.0.0.1 而非
		// "alice@127.0.0.1", 旧实现按冒号切分后两者都命中不了回环名单)。
		if parsed, perr := url.Parse(u); perr == nil && parsed.Hostname() != "" {
			host = parsed.Hostname()
		}
		// IPv6 字面量带方括号, 先剥掉 (如 tcp://[::1]:2375)
		if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") && len(host) >= 2 {
			host = host[1 : len(host)-1]
		}
		// IP 字面量: 用 netip 规范化后再判回环/私网等。
		// 关键: netip 可解析 IPv4-mapped IPv6(如 ::ffff:127.0.0.1),
		// Unmap() 后规范化为 127.0.0.1, IsLoopback() 命中 —— 旧实现仅与 "::1"
		// 字面比较, 该形态可绕过回环封锁。
		if addr, err := netip.ParseAddr(host); err == nil {
			addr = addr.Unmap()
			if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsMulticast() || addr.IsUnspecified() {
				return false
			}
			return true
		}
		// 主机名: 去掉尾点 FQDN 后比较回环别名, 并尽力解析验证非回环
		// (注意 DNS rebinding 的 TOCTOU 限制: 校验时解析与连接时解析可能不同,
		// 这里是尽力而为的纵深防御)。
		if strings.TrimSuffix(host, ".") == "localhost" {
			return false
		}
		if ips, err := net.LookupHost(host); err == nil {
			for _, ipStr := range ips {
				if addr, aerr := netip.ParseAddr(ipStr); aerr == nil {
					addr = addr.Unmap()
					if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsUnspecified() {
						return false
					}
				}
			}
		}
		return true
	}
	return false
}

// dockerBegin/dockerEnd 是并发 transport 调用闸门(对比 CmdAbility 16 基线)。
func (d *DockerAbility) dockerBegin(act string) types.CommandOutput {
	if d.running.Load() >= maxDockerConcurrency {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: too many concurrent calls: %w", act, types.ErrInvalidArguments)}
	}
	d.running.Add(1)
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
