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
		cs, err := transport.List(all)
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %v", act, types.ErrInvalidArguments, err)}
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
		d.mu.RLock()
		transport := d.transport
		d.mu.RUnlock()
		if transport == nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no transport: %w", act, types.ErrInvalidArguments)}
		}
		if err := transport.Pull(strings.TrimSpace(typed.Reference)); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %v", act, types.ErrInvalidArguments, err)}
		}
		return types.CommandOutput{Name: act, Value: strings.TrimSpace(typed.Reference)}
	case DockerCommandCreate:
		typed, ok := args.(DockerCreateArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		if strings.TrimSpace(typed.Image) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: image required: %w", act, types.ErrInvalidArguments)}
		}
		d.mu.RLock()
		transport := d.transport
		d.mu.RUnlock()
		if transport == nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no transport: %w", act, types.ErrInvalidArguments)}
		}
		id, err := transport.Create(typed)
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %v", act, types.ErrInvalidArguments, err)}
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
	d.mu.RLock()
	transport := d.transport
	d.mu.RUnlock()
	if transport == nil {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no transport: %w", act, types.ErrInvalidArguments)}
	}
	switch act {
	case DockerCommandStart:
		if err := transport.Start(strings.TrimSpace(typed.IDOrName)); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %v", act, types.ErrInvalidArguments, err)}
		}
		return types.CommandOutput{Name: act, Value: typed.IDOrName}
	case DockerCommandStop:
		if err := transport.Stop(strings.TrimSpace(typed.IDOrName), typed.Timeout); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %v", act, types.ErrInvalidArguments, err)}
		}
		return types.CommandOutput{Name: act, Value: typed.IDOrName}
	case DockerCommandRestart:
		if err := transport.Restart(strings.TrimSpace(typed.IDOrName), typed.Timeout); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %v", act, types.ErrInvalidArguments, err)}
		}
		return types.CommandOutput{Name: act, Value: typed.IDOrName}
	case DockerCommandRemove:
		if err := transport.Remove(strings.TrimSpace(typed.IDOrName), false); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %v", act, types.ErrInvalidArguments, err)}
		}
		return types.CommandOutput{Name: act, Value: typed.IDOrName}
	case DockerCommandInspect:
		c, err := transport.Inspect(strings.TrimSpace(typed.IDOrName))
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %v", act, types.ErrInvalidArguments, err)}
		}
		return types.CommandOutput{Name: act, Value: c}
	case DockerCommandGetLogs:
		logs, err := transport.Logs(strings.TrimSpace(typed.IDOrName), 100)
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %v", act, types.ErrInvalidArguments, err)}
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
		// IPv6 字面量带方括号, 先剥掉再比较回环 (如 tcp://[::1]:2375)
		if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") && len(host) >= 2 {
			host = host[1 : len(host)-1]
		}
		if host == "localhost" || host == "127.0.0.1" || host == "0.0.0.0" || host == "::1" {
			return false
		}
		return true
	}
	return false
}
