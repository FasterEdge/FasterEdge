package ability

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/FasterEdge/FasterEdge/data"
	"github.com/FasterEdge/FasterEdge/types"
)

type fakeDockerTransport struct {
	mu         sync.Mutex
	containers map[string]DockerContainer
	listErr    error
	startErr   error
	createErr  error
	pulled     []string
}

func newFakeDockerTransport() *fakeDockerTransport {
	return &fakeDockerTransport{containers: make(map[string]DockerContainer)}
}

func (f *fakeDockerTransport) List(_ bool) ([]DockerContainer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]DockerContainer, 0, len(f.containers))
	for _, c := range f.containers {
		out = append(out, c)
	}
	return out, nil
}

func (f *fakeDockerTransport) Start(idOrName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return f.startErr
	}
	c, ok := f.containers[idOrName]
	if !ok {
		return errors.New("not found")
	}
	c.State = "running"
	f.containers[idOrName] = c
	return nil
}

func (f *fakeDockerTransport) Stop(idOrName string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[idOrName]
	if !ok {
		return errors.New("not found")
	}
	c.State = "exited"
	f.containers[idOrName] = c
	return nil
}

func (f *fakeDockerTransport) Restart(idOrName string, t time.Duration) error {
	return f.Stop(idOrName, t)
}

func (f *fakeDockerTransport) Remove(idOrName string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.containers, idOrName)
	return nil
}

func (f *fakeDockerTransport) Pull(reference string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulled = append(f.pulled, reference)
	return nil
}

func (f *fakeDockerTransport) Inspect(idOrName string) (DockerContainer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[idOrName]
	if !ok {
		return DockerContainer{}, errors.New("not found")
	}
	return c, nil
}

func (f *fakeDockerTransport) Logs(idOrName string, _ int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return "logs-" + idOrName, nil
}

func (f *fakeDockerTransport) Create(args DockerCreateArgs) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return "", f.createErr
	}
	id := "container-" + args.Name
	f.containers[id] = DockerContainer{ID: id, Name: args.Name, Image: args.Image, State: "created", Created: time.Now()}
	return id, nil
}

func newDockerAtom(t *testing.T) *types.Atom {
	t.Helper()
	a := &types.Atom{}
	if err := a.AddData(&data.BaseData{}); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestDockerAbilityRejectsMissingDeps(t *testing.T) {
	d := NewDockerAbility()
	if out := d.Command(&types.Atom{}, DockerCommandGetEndpoint, nil); !errors.Is(out.Err, types.ErrMissingDependency) {
		t.Fatalf("missing deps error = %v", out.Err)
	}
}

func TestDockerAbilitySetGetEndpoint(t *testing.T) {
	d := NewDockerAbility()
	atom := newDockerAtom(t)
	if out := d.Command(atom, DockerCommandSetEndpoint, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set wrong type error = %v", out.Err)
	}
	for _, bad := range []string{"", "ftp://x", "tcp://localhost:2375", "tcp://127.0.0.1:2375", "tcp://[::1]:2375", "tcp://0.0.0.0:2375", "tcp://::1:2375", "http://host:2375"} {
		if out := d.Command(atom, DockerCommandSetEndpoint, DockerEndpointArgs{URL: bad}); !errors.Is(out.Err, types.ErrInvalidArguments) {
			t.Errorf("bad endpoint %q should reject, got %v", bad, out.Err)
		}
	}
	if out := d.Command(atom, DockerCommandSetEndpoint, DockerEndpointArgs{URL: "unix:///var/run/docker.sock"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := d.Command(atom, DockerCommandSetEndpoint, DockerEndpointArgs{URL: "tcp://docker.example.com:2375"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := d.Command(atom, DockerCommandGetEndpoint, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get with args error = %v", out.Err)
	}
	if out := d.Command(atom, DockerCommandGetEndpoint, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if out.Value != "tcp://docker.example.com:2375" {
		t.Fatalf("endpoint = %q", out.Value)
	}
}

func TestDockerAbilityList(t *testing.T) {
	d := NewDockerAbility()
	atom := newDockerAtom(t)
	if out := d.Command(atom, DockerCommandListContainers, nil); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("no transport error = %v", out.Err)
	}
	ft := newFakeDockerTransport()
	ft.containers["c1"] = DockerContainer{ID: "c1", Name: "web", Image: "nginx", State: "running"}
	ft.containers["c2"] = DockerContainer{ID: "c2", Name: "db", Image: "postgres", State: "exited"}
	d.SetTransport(ft)
	// args is allowed nil
	if out := d.Command(atom, DockerCommandListContainers, struct{ All bool }{All: true}); out.Err != nil {
		t.Fatal(out.Err)
	} else if list, _ := out.Value.([]DockerContainer); len(list) != 2 {
		t.Fatalf("list = %v", list)
	}
	// 错误类型
	if out := d.Command(atom, DockerCommandListContainers, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("list wrong type error = %v", out.Err)
	}
	// 错误
	ft.listErr = errors.New("daemon down")
	if out := d.Command(atom, DockerCommandListContainers, nil); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("list err = %v", out.Err)
	}
}

func TestDockerAbilityContainerActions(t *testing.T) {
	d := NewDockerAbility()
	atom := newDockerAtom(t)
	ft := newFakeDockerTransport()
	ft.containers["c1"] = DockerContainer{ID: "c1", Name: "web", Image: "nginx", State: "running"}
	d.SetTransport(ft)
	// start/stop/restart/remove/inspect/logs 类型错误
	for _, act := range []string{DockerCommandStart, DockerCommandStop, DockerCommandRestart, DockerCommandRemove, DockerCommandInspect, DockerCommandGetLogs} {
		if out := d.Command(atom, act, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
			t.Errorf("%s wrong type error = %v", act, out.Err)
		}
		if out := d.Command(atom, act, DockerContainerArgs{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
			t.Errorf("%s empty error = %v", act, out.Err)
		}
	}
	// start 成功
	if out := d.Command(atom, DockerCommandStart, DockerContainerArgs{IDOrName: "c1"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// stop 成功
	if out := d.Command(atom, DockerCommandStop, DockerContainerArgs{IDOrName: "c1"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// restart
	if out := d.Command(atom, DockerCommandRestart, DockerContainerArgs{IDOrName: "c1"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// inspect
	if out := d.Command(atom, DockerCommandInspect, DockerContainerArgs{IDOrName: "c1"}); out.Err != nil {
		t.Fatal(out.Err)
	} else if c, _ := out.Value.(DockerContainer); c.Name != "web" {
		t.Fatalf("inspect = %+v", c)
	}
	// logs
	if out := d.Command(atom, DockerCommandGetLogs, DockerContainerArgs{IDOrName: "c1"}); out.Err != nil {
		t.Fatal(out.Err)
	} else if v, _ := out.Value.(string); v != "logs-c1" {
		t.Fatalf("logs = %q", v)
	}
	// remove
	if out := d.Command(atom, DockerCommandRemove, DockerContainerArgs{IDOrName: "c1"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// start 缺失
	if out := d.Command(atom, DockerCommandStart, DockerContainerArgs{IDOrName: "missing"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("start missing error = %v", out.Err)
	}
}

func TestDockerAbilityPullAndCreate(t *testing.T) {
	d := NewDockerAbility()
	atom := newDockerAtom(t)
	ft := newFakeDockerTransport()
	d.SetTransport(ft)
	// pull
	if out := d.Command(atom, DockerCommandPullImage, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("pull wrong type error = %v", out.Err)
	}
	if out := d.Command(atom, DockerCommandPullImage, DockerPullImageArgs{Reference: ""}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("pull empty error = %v", out.Err)
	}
	if out := d.Command(atom, DockerCommandPullImage, DockerPullImageArgs{Reference: "nginx:latest"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// create
	if out := d.Command(atom, DockerCommandCreate, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("create wrong type error = %v", out.Err)
	}
	if out := d.Command(atom, DockerCommandCreate, DockerCreateArgs{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("create no image error = %v", out.Err)
	}
	if out := d.Command(atom, DockerCommandCreate, DockerCreateArgs{Name: "web", Image: "nginx:latest"}); out.Err != nil {
		t.Fatal(out.Err)
	} else if id, _ := out.Value.(string); id != "container-web" {
		t.Fatalf("id = %q", id)
	}
	// 无 transport
	d2 := NewDockerAbility()
	if out := d2.Command(atom, DockerCommandPullImage, DockerPullImageArgs{Reference: "x"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("no transport pull error = %v", out.Err)
	}
}

func TestDockerAbilityUnknownCommand(t *testing.T) {
	d := NewDockerAbility()
	atom := newDockerAtom(t)
	if out := d.Command(atom, "nope", nil); !errors.Is(out.Err, types.ErrUnsupportedCommand) {
		t.Fatalf("unknown error = %v", out.Err)
	}
}
