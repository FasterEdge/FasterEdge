package ability

import (
	"errors"
	"sync"
	"testing"

	"github.com/FasterEdge/FasterEdge/data"
	"github.com/FasterEdge/FasterEdge/types"
)

type fakeEKuiperTransport struct {
	mu        sync.Mutex
	createErr error
	startErr  error
	streams   map[string]string
	rules     map[string]EKuiperRule
}

func newFakeEKuiperTransport() *fakeEKuiperTransport {
	return &fakeEKuiperTransport{
		streams: make(map[string]string),
		rules:   make(map[string]EKuiperRule),
	}
}

func (f *fakeEKuiperTransport) CreateStream(name, sql string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return f.createErr
	}
	f.streams[name] = sql
	return nil
}

func (f *fakeEKuiperTransport) DropStream(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.streams, name)
	return nil
}

func (f *fakeEKuiperTransport) CreateRule(id, sql string, actions []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules[id] = EKuiperRule{ID: id, SQL: sql, Actions: actions, Status: EKuiperStatusStopped}
	return nil
}

func (f *fakeEKuiperTransport) DropRule(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.rules, id)
	return nil
}

func (f *fakeEKuiperTransport) StartRule(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return f.startErr
	}
	r := f.rules[id]
	r.Status = EKuiperStatusRunning
	f.rules[id] = r
	return nil
}

func (f *fakeEKuiperTransport) StopRule(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.rules[id]
	r.Status = EKuiperStatusStopped
	f.rules[id] = r
	return nil
}

func (f *fakeEKuiperTransport) Ping() error { return nil }

func newEKuiperAtom(t *testing.T) *types.Atom {
	t.Helper()
	a := &types.Atom{}
	if err := a.AddData(&data.BaseData{}); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestEKuiperAbilityRejectsMissingDeps(t *testing.T) {
	e := NewEKuiperAbility()
	if out := e.Command(&types.Atom{}, EKuiperCommandGetEndpoint, nil); !errors.Is(out.Err, types.ErrMissingDependency) {
		t.Fatalf("missing deps error = %v", out.Err)
	}
}

func TestEKuiperAbilitySetGetEndpoint(t *testing.T) {
	e := NewEKuiperAbility()
	atom := newEKuiperAtom(t)
	if out := e.Command(atom, EKuiperCommandSetEndpoint, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set wrong type error = %v", out.Err)
	}
	if out := e.Command(atom, EKuiperCommandSetEndpoint, EKuiperEndpointArgs{URL: "ftp://x"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("bad url error = %v", out.Err)
	}
	if out := e.Command(atom, EKuiperCommandSetEndpoint, EKuiperEndpointArgs{URL: "https://ekuiper.example.com:9081"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := e.Command(atom, EKuiperCommandGetEndpoint, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get with args error = %v", out.Err)
	}
	if out := e.Command(atom, EKuiperCommandGetEndpoint, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if out.Value != "https://ekuiper.example.com:9081" {
		t.Fatalf("endpoint = %q", out.Value)
	}
}

func TestEKuiperAbilityStreams(t *testing.T) {
	e := NewEKuiperAbility()
	atom := newEKuiperAtom(t)
	ft := newFakeEKuiperTransport()
	e.SetTransport(ft)
	if out := e.Command(atom, EKuiperCommandCreateStream, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("create wrong type error = %v", out.Err)
	}
	if out := e.Command(atom, EKuiperCommandCreateStream, EKuiperStreamArgs{Name: "has space", SQL: "x"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("bad name error = %v", out.Err)
	}
	if out := e.Command(atom, EKuiperCommandCreateStream, EKuiperStreamArgs{Name: "s1", SQL: "  "}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("empty sql error = %v", out.Err)
	}
	if out := e.Command(atom, EKuiperCommandCreateStream, EKuiperStreamArgs{Name: "s1", SQL: "CREATE STREAM ..."}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// 重复
	if out := e.Command(atom, EKuiperCommandCreateStream, EKuiperStreamArgs{Name: "s1", SQL: "x"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("dup error = %v", out.Err)
	}
	// list
	if out := e.Command(atom, EKuiperCommandListStreams, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("list with args error = %v", out.Err)
	}
	if out := e.Command(atom, EKuiperCommandListStreams, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if list, _ := out.Value.([]EKuiperStream); len(list) != 1 {
		t.Fatalf("list = %v", list)
	}
	// get
	if out := e.Command(atom, EKuiperCommandGetStream, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get wrong type error = %v", out.Err)
	}
	if out := e.Command(atom, EKuiperCommandGetStream, EKuiperStreamRef{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get empty error = %v", out.Err)
	}
	if out := e.Command(atom, EKuiperCommandGetStream, EKuiperStreamRef{Name: "missing"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get missing error = %v", out.Err)
	}
	if out := e.Command(atom, EKuiperCommandGetStream, EKuiperStreamRef{Name: "s1"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// drop
	if out := e.Command(atom, EKuiperCommandDropStream, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("drop wrong type error = %v", out.Err)
	}
	if out := e.Command(atom, EKuiperCommandDropStream, EKuiperStreamRef{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("drop empty error = %v", out.Err)
	}
	if out := e.Command(atom, EKuiperCommandDropStream, EKuiperStreamRef{Name: "missing"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("drop missing error = %v", out.Err)
	}
	if out := e.Command(atom, EKuiperCommandDropStream, EKuiperStreamRef{Name: "s1"}); out.Err != nil {
		t.Fatal(out.Err)
	}
}

func TestEKuiperAbilityRules(t *testing.T) {
	e := NewEKuiperAbility()
	atom := newEKuiperAtom(t)
	ft := newFakeEKuiperTransport()
	e.SetTransport(ft)
	if out := e.Command(atom, EKuiperCommandCreateRule, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("create wrong type error = %v", out.Err)
	}
	if out := e.Command(atom, EKuiperCommandCreateRule, EKuiperCreateRuleArgs{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("empty error = %v", out.Err)
	}
	if out := e.Command(atom, EKuiperCommandCreateRule, EKuiperCreateRuleArgs{ID: "r1", SQL: "SELECT * FROM s"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// 重复
	if out := e.Command(atom, EKuiperCommandCreateRule, EKuiperCreateRuleArgs{ID: "r1", SQL: "x"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("dup error = %v", out.Err)
	}
	// show
	if out := e.Command(atom, EKuiperCommandShowRules, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("show with args error = %v", out.Err)
	}
	if out := e.Command(atom, EKuiperCommandShowRules, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if list, _ := out.Value.([]EKuiperRule); len(list) != 1 {
		t.Fatalf("list = %v", list)
	}
	// start
	if out := e.Command(atom, EKuiperCommandStartRule, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("start wrong type error = %v", out.Err)
	}
	if out := e.Command(atom, EKuiperCommandStartRule, EKuiperRuleIDArg{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("start empty error = %v", out.Err)
	}
	if out := e.Command(atom, EKuiperCommandStartRule, EKuiperRuleIDArg{ID: "missing"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("start missing error = %v", out.Err)
	}
	if out := e.Command(atom, EKuiperCommandStartRule, EKuiperRuleIDArg{ID: "r1"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// status
	if out := e.Command(atom, EKuiperCommandGetRuleStatus, EKuiperRuleIDArg{ID: "r1"}); out.Err != nil {
		t.Fatal(out.Err)
	} else if st, _ := out.Value.(EKuiperRuleStatus); st != EKuiperStatusRunning {
		t.Fatalf("status = %q", st)
	}
	// stop
	if out := e.Command(atom, EKuiperCommandStopRule, EKuiperRuleIDArg{ID: "r1"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// drop
	if out := e.Command(atom, EKuiperCommandDropRule, EKuiperRuleIDArg{ID: "r1"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := e.Command(atom, EKuiperCommandDropRule, EKuiperRuleIDArg{ID: "r1"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("drop missing error = %v", out.Err)
	}
}

func TestEKuiperAbilityNoTransport(t *testing.T) {
	e := NewEKuiperAbility()
	atom := newEKuiperAtom(t)
	if out := e.Command(atom, EKuiperCommandCreateStream, EKuiperStreamArgs{Name: "s", SQL: "x"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("no transport create error = %v", out.Err)
	}
	if out := e.Command(atom, EKuiperCommandCreateRule, EKuiperCreateRuleArgs{ID: "r", SQL: "x"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("no transport rule error = %v", out.Err)
	}
}

func TestEKuiperAbilityUnknownCommand(t *testing.T) {
	e := NewEKuiperAbility()
	atom := newEKuiperAtom(t)
	if out := e.Command(atom, "nope", nil); !errors.Is(out.Err, types.ErrUnsupportedCommand) {
		t.Fatalf("unknown error = %v", out.Err)
	}
}

// TestEKuiperAbilityTransportErrors 补 fake 定义了却从未注入的 createErr/startErr
// 分支: CreateStream/StartRule 的 transport 失败路径原零测试(只测过成功与参数校验)。
func TestEKuiperAbilityTransportErrors(t *testing.T) {
	e := NewEKuiperAbility()
	atom := newEKuiperAtom(t)
	ft := newFakeEKuiperTransport()
	e.SetTransport(ft)
	ft.createErr = errors.New("create down")
	if out := e.Command(atom, EKuiperCommandCreateStream, EKuiperStreamArgs{Name: "s1", SQL: "CREATE STREAM ..."}); !errors.Is(out.Err, types.ErrOperationFailed) {
		t.Fatalf("create transport err = %v", out.Err)
	}
	ft.createErr = nil
	if out := e.Command(atom, EKuiperCommandCreateRule, EKuiperCreateRuleArgs{ID: "r1", SQL: "SELECT * FROM s"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	ft.startErr = errors.New("start down")
	if out := e.Command(atom, EKuiperCommandStartRule, EKuiperRuleIDArg{ID: "r1"}); !errors.Is(out.Err, types.ErrOperationFailed) {
		t.Fatalf("start transport err = %v", out.Err)
	}
}
