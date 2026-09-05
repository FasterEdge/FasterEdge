package ability

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FasterEdge/FasterEdge/data"
	"github.com/FasterEdge/FasterEdge/types"
)

type fakeK8sTransport struct {
	mu        sync.Mutex
	resources map[string]K8sResource
	applyErr  error
	scaleErr  error
	logs      string
	deleted   []string
	scaledTo  map[string]int32
}

func newFakeK8sTransport() *fakeK8sTransport {
	return &fakeK8sTransport{
		resources: make(map[string]K8sResource),
		scaledTo:  make(map[string]int32),
	}
}

func key(kind, ns, name string) string { return kind + "/" + ns + "/" + name }

func (f *fakeK8sTransport) Apply(_ context.Context, manifest string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.applyErr != nil {
		return f.applyErr
	}
	// 简化:直接当作 Pod "demo" 注册
	r := K8sResource{Kind: "Pod", Name: "demo", Namespace: "default", CreatedAt: time.Now()}
	f.resources[key(r.Kind, r.Namespace, r.Name)] = r
	return nil
}

func (f *fakeK8sTransport) Delete(_ context.Context, kind, name, ns string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.resources, key(kind, ns, name))
	f.deleted = append(f.deleted, kind+"/"+name)
	return nil
}

func (f *fakeK8sTransport) List(_ context.Context, kind, ns string) ([]K8sResource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]K8sResource, 0)
	for k, r := range f.resources {
		if r.Kind != kind {
			continue
		}
		if ns != "" && r.Namespace != ns {
			continue
		}
		out = append(out, r)
		_ = k
	}
	return out, nil
}

func (f *fakeK8sTransport) Get(_ context.Context, kind, name, ns string) (K8sResource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.resources[key(kind, ns, name)]
	if !ok {
		return K8sResource{}, errors.New("not found")
	}
	return r, nil
}

func (f *fakeK8sTransport) Scale(_ context.Context, deployment, ns string, replicas int32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.scaleErr != nil {
		return f.scaleErr
	}
	f.scaledTo[ns+"/"+deployment] = replicas
	return nil
}

func (f *fakeK8sTransport) Logs(_ context.Context, pod, ns string, tail int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logs, nil
}

func newK8sAtom(t *testing.T) *types.Atom {
	t.Helper()
	a := &types.Atom{}
	if err := a.AddData(&data.BaseData{}); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestK8sAbilityRejectsMissingDeps(t *testing.T) {
	k := NewK8sAbility()
	if out := k.Command(&types.Atom{}, K8sCommandGetContext, nil); !errors.Is(out.Err, types.ErrMissingDependency) {
		t.Fatalf("missing deps error = %v", out.Err)
	}
}

func TestK8sAbilitySetGetContext(t *testing.T) {
	k := NewK8sAbility()
	atom := newK8sAtom(t)
	if out := k.Command(atom, K8sCommandSetContext, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set wrong type error = %v", out.Err)
	}
	if out := k.Command(atom, K8sCommandSetContext, K8sContextArgs{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("empty cluster error = %v", out.Err)
	}
	if out := k.Command(atom, K8sCommandSetContext, K8sContextArgs{K8sContext: K8sContext{Cluster: "prod"}}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := k.Command(atom, K8sCommandGetContext, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get with args error = %v", out.Err)
	}
	if out := k.Command(atom, K8sCommandGetContext, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if ctx, _ := out.Value.(K8sContextView); ctx.Namespace != "default" {
		t.Fatalf("namespace = %q, want default", ctx.Namespace)
	}
	// 安全回归: set_context 携带含私钥的 kubeconfig 后, get_context/set_context 回执
	// 均不得回读 Kubeconfig 原文, 只暴露是否已配置。
	secretKC := "apiVersion: v1\nkind: Config\nusers:\n- name: x\n  user:\n    client-key-data: LS0tLS1CRUdJTiBSU0EgUFJJVkFURSBLRVktLS0tLQo="
	if out := k.Command(atom, K8sCommandSetContext, K8sContextArgs{K8sContext: K8sContext{Cluster: "prod", Namespace: "team-a", Kubeconfig: secretKC}}); out.Err != nil {
		t.Fatal(out.Err)
	} else if v, ok := out.Value.(K8sContextView); !ok || v.KubeconfigConfigured != true || v.Cluster != "prod" {
		t.Fatalf("set_context view = %#v, want configured view without raw kubeconfig", out.Value)
	}
	if out := k.Command(atom, K8sCommandGetContext, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if v, ok := out.Value.(K8sContextView); !ok || v.KubeconfigConfigured != true {
		t.Fatalf("get_context view = %#v, want configured=true", out.Value)
	} else if strings.Contains(fmt.Sprintf("%v", out.Value), "client-key-data") {
		t.Fatalf("get_context leaked raw kubeconfig: %v", out.Value)
	}
}

func TestK8sAbilityApply(t *testing.T) {
	k := NewK8sAbility()
	atom := newK8sAtom(t)
	if out := k.Command(atom, K8sCommandApply, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("apply wrong type error = %v", out.Err)
	}
	if out := k.Command(atom, K8sCommandApply, K8sApplyArgs{Manifest: ""}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("empty manifest error = %v", out.Err)
	}
	if out := k.Command(atom, K8sCommandApply, K8sApplyArgs{Manifest: "apiVersion: v1\nkind: Pod"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("no transport error = %v", out.Err)
	}
	ft := newFakeK8sTransport()
	k.SetTransport(ft)
	if out := k.Command(atom, K8sCommandApply, K8sApplyArgs{Manifest: "x"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	ft.applyErr = errors.New("apply fail")
	if out := k.Command(atom, K8sCommandApply, K8sApplyArgs{Manifest: "x"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("apply err = %v", out.Err)
	}
}

func TestK8sAbilityListGetDelete(t *testing.T) {
	k := NewK8sAbility()
	atom := newK8sAtom(t)
	ft := newFakeK8sTransport()
	ft.resources[key("Pod", "default", "demo")] = K8sResource{Kind: "Pod", Name: "demo", Namespace: "default"}
	ft.resources[key("Pod", "kube-system", "coredns")] = K8sResource{Kind: "Pod", Name: "coredns", Namespace: "kube-system"}
	k.SetTransport(ft)
	k.Command(atom, K8sCommandSetContext, K8sContextArgs{K8sContext: K8sContext{Cluster: "prod"}})

	// list 类型错误
	if out := k.Command(atom, K8sCommandList, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("list wrong type error = %v", out.Err)
	}
	if out := k.Command(atom, K8sCommandList, K8sListArgs{Kind: "!"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("list bad kind error = %v", out.Err)
	}
	if out := k.Command(atom, K8sCommandList, K8sListArgs{Kind: "Pod"}); out.Err != nil {
		t.Fatal(out.Err)
	} else if rs, _ := out.Value.([]K8sResource); len(rs) != 1 {
		t.Fatalf("list default ns = %v", rs)
	}
	if out := k.Command(atom, K8sCommandList, K8sListArgs{Kind: "Pod", Namespace: "kube-system"}); out.Err != nil {
		t.Fatal(out.Err)
	} else if rs, _ := out.Value.([]K8sResource); len(rs) != 1 || rs[0].Name != "coredns" {
		t.Fatalf("list kube-system = %v", rs)
	}
	// get
	if out := k.Command(atom, K8sCommandGet, K8sGetArgs{Kind: "Pod", Name: "demo"}); out.Err != nil {
		t.Fatal(out.Err)
	} else if r, _ := out.Value.(K8sResource); r.Name != "demo" {
		t.Fatalf("get = %+v", r)
	}
	if out := k.Command(atom, K8sCommandGet, K8sGetArgs{Kind: "Pod", Name: "missing"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get missing error = %v", out.Err)
	}
	// delete
	if out := k.Command(atom, K8sCommandDelete, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("delete wrong type error = %v", out.Err)
	}
	if out := k.Command(atom, K8sCommandDelete, K8sDeleteArgs{Kind: "Pod", Name: ""}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("delete empty error = %v", out.Err)
	}
	if out := k.Command(atom, K8sCommandDelete, K8sDeleteArgs{Kind: "!", Name: "x"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("delete bad kind error = %v", out.Err)
	}
	if out := k.Command(atom, K8sCommandDelete, K8sDeleteArgs{Kind: "Pod", Name: "demo"}); out.Err != nil {
		t.Fatal(out.Err)
	}
}

func TestK8sAbilityScale(t *testing.T) {
	k := NewK8sAbility()
	atom := newK8sAtom(t)
	ft := newFakeK8sTransport()
	k.SetTransport(ft)
	if out := k.Command(atom, K8sCommandScale, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("scale wrong type error = %v", out.Err)
	}
	if out := k.Command(atom, K8sCommandScale, K8sScaleArgs{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("scale empty error = %v", out.Err)
	}
	if out := k.Command(atom, K8sCommandScale, K8sScaleArgs{Deployment: "web", Replicas: -1}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("scale negative error = %v", out.Err)
	}
	if out := k.Command(atom, K8sCommandScale, K8sScaleArgs{Deployment: "web", Replicas: 3}); out.Err != nil {
		t.Fatal(out.Err)
	}
	ft.scaleErr = errors.New("scale fail")
	if out := k.Command(atom, K8sCommandScale, K8sScaleArgs{Deployment: "web", Replicas: 1}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("scale err = %v", out.Err)
	}
}

func TestK8sAbilityGetLogs(t *testing.T) {
	k := NewK8sAbility()
	atom := newK8sAtom(t)
	ft := newFakeK8sTransport()
	ft.logs = "log content"
	k.SetTransport(ft)
	if out := k.Command(atom, K8sCommandGetLogs, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("logs wrong type error = %v", out.Err)
	}
	if out := k.Command(atom, K8sCommandGetLogs, K8sLogsArgs{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("logs empty error = %v", out.Err)
	}
	if out := k.Command(atom, K8sCommandGetLogs, K8sLogsArgs{Pod: "p", Tail: -1}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("logs negative tail error = %v", out.Err)
	}
	if out := k.Command(atom, K8sCommandGetLogs, K8sLogsArgs{Pod: "p", Tail: 10}); out.Err != nil {
		t.Fatal(out.Err)
	} else if v, _ := out.Value.(string); v != "log content" {
		t.Fatalf("logs = %q", v)
	}
}

func TestK8sAbilityNoTransport(t *testing.T) {
	k := NewK8sAbility()
	atom := newK8sAtom(t)
	if out := k.Command(atom, K8sCommandList, K8sListArgs{Kind: "Pod"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("no transport list error = %v", out.Err)
	}
	if out := k.Command(atom, K8sCommandScale, K8sScaleArgs{Deployment: "x", Replicas: 1}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("no transport scale error = %v", out.Err)
	}
}

func TestK8sAbilityUnknownCommand(t *testing.T) {
	k := NewK8sAbility()
	atom := newK8sAtom(t)
	if out := k.Command(atom, "nope", nil); !errors.Is(out.Err, types.ErrUnsupportedCommand) {
		t.Fatalf("unknown error = %v", out.Err)
	}
}

func TestK8sIsValidKind(t *testing.T) {
	for _, ok := range []string{"Pod", "Deployment", "Service", "ConfigMap", "v1"} {
		if !isValidK8sKind(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "!", "@Pod", "Pod/Name"} {
		if isValidK8sKind(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}
