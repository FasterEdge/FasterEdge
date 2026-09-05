package ability

import (
	"errors"
	"testing"
	"time"

	"github.com/FasterEdge/FasterEdge/data"
	"github.com/FasterEdge/FasterEdge/types"
)

func newCloudRoleAtom(t *testing.T) *types.Atom {
	t.Helper()
	a := &types.Atom{}
	if err := a.AddData(&data.BaseData{}); err != nil {
		t.Fatal(err)
	}
	if err := a.AddAbility(&RoleAbility{}); err != nil {
		t.Fatal(err)
	}
	if err := a.AddAbility(&BaseAbility{}); err != nil {
		t.Fatal(err)
	}
	if err := a.AddAbility(NewCloudRoleAbility()); err != nil {
		t.Fatal(err)
	}
	// 注入 role = "cloud"
	roleAb, _ := a.Ability("RoleAbility")
	if out := roleAb.Command(a, CommandSetRole, RoleAbilityArgs{Role: "cloud"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	return a
}

func TestCloudRoleAbilityRejectsMissingRole(t *testing.T) {
	c := NewCloudRoleAbility()
	a := &types.Atom{}
	a.AddData(&data.BaseData{})
	// 缺 RoleAbility
	if out := c.Command(a, CloudRoleCommandGetStatus, nil); !errors.Is(out.Err, types.ErrMissingDependency) {
		t.Fatalf("missing role ability error = %v", out.Err)
	}
	a.AddAbility(&RoleAbility{})
	// role 仍为默认(空),不是 "cloud"
	if out := c.Command(a, CloudRoleCommandGetStatus, nil); !errors.Is(out.Err, types.ErrMissingDependency) {
		t.Fatalf("wrong role error = %v", out.Err)
	}
}

func TestCloudRoleAbilitySetAndGetController(t *testing.T) {
	c := NewCloudRoleAbility()
	atom := newCloudRoleAtom(t)
	for _, bad := range []any{
		nil,
		"raw-string",
		CloudRoleSetControllerArgs{URL: ""},
		CloudRoleSetControllerArgs{URL: "ftp://x.example.com"},
		CloudRoleSetControllerArgs{URL: "http://localhost:8080"},
		CloudRoleSetControllerArgs{URL: "http://127.0.0.1"},
		CloudRoleSetControllerArgs{URL: "http://0.0.0.0/path"},
		CloudRoleSetControllerArgs{URL: "http://[::1]:8080"},
		// 安全回归: 私网/链路本地/IPv4-mapped/userinfo 必须拒绝
		CloudRoleSetControllerArgs{URL: "http://192.168.1.1"},
		CloudRoleSetControllerArgs{URL: "http://10.0.0.5/api"},
		CloudRoleSetControllerArgs{URL: "http://169.254.169.254/latest/meta-data/"},
		CloudRoleSetControllerArgs{URL: "http://[::ffff:127.0.0.1]:8080"},
		CloudRoleSetControllerArgs{URL: "http://evil@127.0.0.1/"},
		CloudRoleSetControllerArgs{URL: "http://127.0.0.1@evil.example/"},
	} {
		if out := c.Command(atom, CloudRoleCommandSetController, bad); !errors.Is(out.Err, types.ErrInvalidArguments) {
			t.Fatalf("bad controller %#v error = %v", bad, out.Err)
		}
	}
	if out := c.Command(atom, CloudRoleCommandSetController, CloudRoleSetControllerArgs{URL: "https://ctrl.example.com/api"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := c.Command(atom, CloudRoleCommandGetController, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get with args error = %v", out.Err)
	}
	if out := c.Command(atom, CloudRoleCommandGetController, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if out.Value != "https://ctrl.example.com/api" {
		t.Fatalf("get controller = %q", out.Value)
	}
}

func TestCloudRoleAbilityServicesCRUD(t *testing.T) {
	c := NewCloudRoleAbility()
	atom := newCloudRoleAtom(t)
	// 类型错误
	if out := c.Command(atom, CloudRoleCommandRegister, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("register wrong type error = %v", out.Err)
	}
	// 缺 name
	if out := c.Command(atom, CloudRoleCommandRegister, CloudRoleRegisterServiceArgs{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("register blank name error = %v", out.Err)
	}
	if out := c.Command(atom, CloudRoleCommandRegister, CloudRoleRegisterServiceArgs{Name: " leading-space"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("register bad name error = %v", out.Err)
	}
	// 正常注册
	out := c.Command(atom, CloudRoleCommandRegister, CloudRoleRegisterServiceArgs{
		Name: "ingress", Version: "1.2.0", Endpoint: "https://node/ingress",
	})
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	// 再次注册同名 service(覆盖)
	if out := c.Command(atom, CloudRoleCommandRegister, CloudRoleRegisterServiceArgs{
		Name: "ingress", Version: "1.3.0",
	}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// list
	if out := c.Command(atom, CloudRoleCommandListServices, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("list with args error = %v", out.Err)
	}
	listOut := c.Command(atom, CloudRoleCommandListServices, nil)
	if listOut.Err != nil {
		t.Fatal(listOut.Err)
	}
	svcs, ok := listOut.Value.([]CloudRoleService)
	if !ok || len(svcs) != 1 {
		t.Fatalf("list = %#v", listOut.Value)
	}
	if svcs[0].Version != "1.3.0" {
		t.Fatalf("after re-register version = %q", svcs[0].Version)
	}
	// unregister 类型错误
	if out := c.Command(atom, CloudRoleCommandUnregister, nil); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("unregister nil error = %v", out.Err)
	}
	if out := c.Command(atom, CloudRoleCommandUnregister, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("unregister wrong type error = %v", out.Err)
	}
	if out := c.Command(atom, CloudRoleCommandUnregister, CloudRoleUnregisterServiceArgs{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("unregister blank name error = %v", out.Err)
	}
	// unregister 不存在的
	if out := c.Command(atom, CloudRoleCommandUnregister, CloudRoleUnregisterServiceArgs{Name: "ghost"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("unregister missing error = %v", out.Err)
	}
	// unregister 正常
	if out := c.Command(atom, CloudRoleCommandUnregister, CloudRoleUnregisterServiceArgs{Name: "ingress"}); out.Err != nil {
		t.Fatal(out.Err)
	}
}

func TestCloudRoleAbilityStatusAndHeartbeat(t *testing.T) {
	c := NewCloudRoleAbility()
	atom := newCloudRoleAtom(t)
	// 初始 unknown
	getOut := c.Command(atom, CloudRoleCommandGetStatus, struct{}{})
	if getOut.Err == nil {
		t.Fatalf("get_status with args should reject")
	}
	getOut = c.Command(atom, CloudRoleCommandGetStatus, nil)
	if getOut.Err != nil {
		t.Fatal(getOut.Err)
	}
	if st, _ := getOut.Value.(CloudRoleStatus); st != CloudRoleStatusUnknown {
		t.Fatalf("initial status = %q", st)
	}
	// set_status 类型错误
	if out := c.Command(atom, CloudRoleCommandSetStatus, nil); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set nil error = %v", out.Err)
	}
	if out := c.Command(atom, CloudRoleCommandSetStatus, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set wrong type error = %v", out.Err)
	}
	// set_status 无效值
	if out := c.Command(atom, CloudRoleCommandSetStatus, CloudRoleSetStatusArgs{Status: "weird"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set invalid status error = %v", out.Err)
	}
	// 正常
	if out := c.Command(atom, CloudRoleCommandSetStatus, CloudRoleSetStatusArgs{Status: "healthy"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	getOut = c.Command(atom, CloudRoleCommandGetStatus, nil)
	if getOut.Value.(CloudRoleStatus) != CloudRoleStatusHealthy {
		t.Fatalf("status = %q", getOut.Value)
	}
	// 大小写无关
	if out := c.Command(atom, CloudRoleCommandSetStatus, CloudRoleSetStatusArgs{Status: "DEGRADED"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// heartbeat
	if out := c.Command(atom, CloudRoleCommandHeartbeat, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("heartbeat with args error = %v", out.Err)
	}
	prev := time.Now()
	if out := c.Command(atom, CloudRoleCommandHeartbeat, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if beat, _ := out.Value.(time.Time); beat.Before(prev) {
		t.Fatalf("heartbeat time %s before start %s", beat, prev)
	}
}

func TestCloudRoleAbilityDescribe(t *testing.T) {
	c := NewCloudRoleAbility()
	atom := newCloudRoleAtom(t)
	if err := atom.MountAll(); err != nil {
		t.Fatal(err)
	}
	if out := c.Command(atom, CloudRoleCommandSetController, CloudRoleSetControllerArgs{URL: "https://ctrl.example.com"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := c.Command(atom, CloudRoleCommandRegister, CloudRoleRegisterServiceArgs{Name: "ingress"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := c.Command(atom, CloudRoleCommandHeartbeat, nil); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := c.Command(atom, CloudRoleCommandDescribe, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("describe with args error = %v", out.Err)
	}
	out := c.Command(atom, CloudRoleCommandDescribe, nil)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	d, ok := out.Value.(CloudRoleDescription)
	if !ok {
		t.Fatalf("describe type = %T", out.Value)
	}
	if d.Role != "cloud" {
		t.Fatalf("role in describe = %q", d.Role)
	}
	if d.Controller != "https://ctrl.example.com" {
		t.Fatalf("controller in describe = %q", d.Controller)
	}
	if d.Heartbeats != 1 {
		t.Fatalf("heartbeats = %d", d.Heartbeats)
	}
	if len(d.Services) != 1 || d.Services[0].Name != "ingress" {
		t.Fatalf("services = %#v", d.Services)
	}
}

func TestCloudRoleAbilityUnknownCommand(t *testing.T) {
	c := NewCloudRoleAbility()
	atom := newCloudRoleAtom(t)
	if out := c.Command(atom, "nope", nil); !errors.Is(out.Err, types.ErrUnsupportedCommand) {
		t.Fatalf("unknown error = %v", out.Err)
	}
}
