package ability

import (
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/FasterEdge/FasterEdge/data"
	"github.com/FasterEdge/FasterEdge/types"
)

func newCmdAtom(t *testing.T) *types.Atom {
	t.Helper()
	a := &types.Atom{}
	if err := a.AddData(&data.BaseData{}); err != nil {
		t.Fatal(err)
	}
	return a
}

// pickShell 返回当前平台上肯定存在的 shell 名称用于 allowlist 测试。
// CI 兼容性:在 linux/darwin 上使用 "sh";其他平台跳过需要真实 shell 的测试。
func pickShell() (string, bool) {
	switch runtime.GOOS {
	case "linux", "darwin":
		return "sh", true
	}
	return "", false
}

func TestCmdAbilityAllowlistCRUD(t *testing.T) {
	c := NewCmdAbility()
	atom := newCmdAtom(t)
	// 类型错误
	if out := c.Command(atom, CmdCommandSetAllowlist, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set allowlist wrong type error = %v", out.Err)
	}
	// 设置空 allowlist
	if out := c.Command(atom, CmdCommandSetAllowlist, CmdSetAllowlistArgs{}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// get 拒绝带参
	if out := c.Command(atom, CmdCommandGetAllowlist, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get with args error = %v", out.Err)
	}
	getOut := c.Command(atom, CmdCommandGetAllowlist, nil)
	if getOut.Err != nil {
		t.Fatal(getOut.Err)
	}
	allow, ok := getOut.Value.([]CmdAllowlistEntry)
	if !ok || len(allow) != 0 {
		t.Fatalf("initial allowlist = %#v", getOut.Value)
	}
	// 添加一条
	if out := c.Command(atom, CmdCommandSetAllowlist, CmdSetAllowlistArgs{Entries: []CmdAllowlistEntry{
		{Name: "echo", ArgsPrefix: []string{"hello"}, MaxArgs: 1},
	}}); out.Err != nil {
		t.Fatal(out.Err)
	}
}

func TestCmdAbilityRunRejectsNotAllowed(t *testing.T) {
	c := NewCmdAbility()
	atom := newCmdAtom(t)
	// 类型错误
	if out := c.Command(atom, CmdCommandRun, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("run wrong type error = %v", out.Err)
	}
	// 空 name
	if out := c.Command(atom, CmdCommandRun, CmdRunArgs{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("empty name error = %v", out.Err)
	}
	// allowlist 为空 → 一律拒绝
	if out := c.Command(atom, CmdCommandRun, CmdRunArgs{Name: "echo", Args: []string{"hi"}}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("empty allowlist error = %v", out.Err)
	}
}

func TestCmdAbilityRunEcho(t *testing.T) {
	shell, ok := pickShell()
	if !ok {
		t.Skip("no standard shell on this platform")
	}
	c := NewCmdAbility()
	atom := newCmdAtom(t)
	// echo hello
	c.SetAllowlist([]CmdAllowlistEntry{
		{Name: shell, ArgsPrefix: []string{"-c"}, MaxArgs: 2},
	})
	out := c.Command(atom, CmdCommandRun, CmdRunArgs{
		Name: shell,
		Args: []string{"-c", "printf hello-world"},
	})
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	res, ok := out.Value.(CmdResult)
	if !ok {
		t.Fatalf("result type = %T", out.Value)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit code = %d, stderr=%q", res.ExitCode, res.Stderr)
	}
	if res.Stdout != "hello-world" {
		t.Fatalf("stdout = %q", res.Stdout)
	}
	// args prefix 不匹配应被拒
	if out := c.Command(atom, CmdCommandRun, CmdRunArgs{
		Name: shell,
		Args: []string{"-l", "echo bad"},
	}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("prefix mismatch should reject, got err=%v", out.Err)
	}
	// 参数过多
	if out := c.Command(atom, CmdCommandRun, CmdRunArgs{
		Name: shell,
		Args: []string{"-c", "echo", "extra"},
	}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("max args should reject, got err=%v", out.Err)
	}
}

// TestCmdAbilityFailClosedEmptyAllowlist 验证 fail-closed:
// 空 allowlist 时 run/start 一律以 ErrAllowlistEmpty 拒绝(旧实现此处放行),
// 且错误信息明确给出 opt-in 路径。
func TestCmdAbilityFailClosedEmptyAllowlist(t *testing.T) {
	c := NewCmdAbility()
	atom := newCmdAtom(t)
	for _, act := range []string{CmdCommandRun, CmdCommandStart} {
		out := c.Command(atom, act, CmdRunArgs{Name: "echo", Args: []string{"hi"}})
		if !errors.Is(out.Err, ErrAllowlistEmpty) {
			t.Fatalf("%s with empty allowlist: err=%v (want ErrAllowlistEmpty)", act, out.Err)
		}
		if !strings.Contains(out.Err.Error(), "set_allowlist") {
			t.Fatalf("%s 错误未给出 opt-in 指引: %v", act, out.Err)
		}
	}
	// set_allowlist 空列表 → 仍 fail-closed
	if out := c.Command(atom, CmdCommandSetAllowlist, CmdSetAllowlistArgs{}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := c.Command(atom, CmdCommandRun, CmdRunArgs{Name: "echo", Args: []string{"hi"}}); !errors.Is(out.Err, ErrAllowlistEmpty) {
		t.Fatalf("run after set empty allowlist: err=%v (want ErrAllowlistEmpty)", out.Err)
	}
}

// TestCmdAbilitySetAllowlistAny 验证显式 opt-in 命令:
// set_allowlist_any(true) 放行任意命令, false 回到 fail-closed。
// 平台适配: 用 go 自身作为可执行名(任何装了 Go 工具链的平台都在 PATH 上),
// 这样 opt-in 的真实执行路径不必因"本机无 sh"而跳过。
func TestCmdAbilitySetAllowlistAny(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}
	c := NewCmdAbility()
	atom := newCmdAtom(t)
	if out := c.Command(atom, CmdCommandSetAllowAny, "raw"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set_allowlist_any wrong type: %v", out.Err)
	}
	// opt-in 前拒绝
	if out := c.Command(atom, CmdCommandRun, CmdRunArgs{Name: goBin, Args: []string{"version"}}); !errors.Is(out.Err, ErrAllowlistEmpty) {
		t.Fatalf("run before allow-any: err=%v (want ErrAllowlistEmpty)", out.Err)
	}
	// opt-in 后任意命令放行
	if out := c.Command(atom, CmdCommandSetAllowAny, CmdSetAllowAnyArgs{Any: true}); out.Err != nil {
		t.Fatalf("set_allowlist_any(true): %v", out.Err)
	}
	if !c.AllowAny() {
		t.Fatal("AllowAny() = false, want true")
	}
	out := c.Command(atom, CmdCommandRun, CmdRunArgs{Name: goBin, Args: []string{"version"}, Timeout: 10 * time.Second})
	if out.Err != nil {
		t.Fatalf("run after allow-any: %v", out.Err)
	}
	if res, _ := out.Value.(CmdResult); res.ExitCode != 0 || !strings.Contains(res.Stdout, "go version") {
		t.Fatalf("stdout = %q (exit=%d), want go version banner", res.Stdout, res.ExitCode)
	}
	// 未列入白名单的可执行名也放行(通配规则生效)
	out = c.Command(atom, CmdCommandRun, CmdRunArgs{Name: "go", Args: []string{"version"}, Timeout: 10 * time.Second})
	if out.Err != nil {
		t.Fatalf("run go after allow-any: %v", out.Err)
	}
	// get_allowlist 明确标识通配规则
	snap := c.Command(atom, CmdCommandGetAllowlist, nil).Value.([]CmdAllowlistEntry)
	if len(snap) != 1 || snap[0].Name != "*" {
		t.Fatalf("get_allowlist after allow-any = %+v (want single * entry)", snap)
	}
	// 关闭 → fail-closed 回归
	if out := c.Command(atom, CmdCommandSetAllowAny, CmdSetAllowAnyArgs{Any: false}); out.Err != nil {
		t.Fatalf("set_allowlist_any(false): %v", out.Err)
	}
	if out := c.Command(atom, CmdCommandRun, CmdRunArgs{Name: goBin, Args: []string{"version"}}); !errors.Is(out.Err, ErrAllowlistEmpty) {
		t.Fatalf("run after opt-out: err=%v (want ErrAllowlistEmpty)", out.Err)
	}
}

// TestCmdAbilitySetAllowAnyRemoteRestriction 锁定 set_allowlist_any 的远程限制。
//
// 背景: set_allowlist 早已被 restrictedRemoteCommand 列为"仅本地进程内调用"
// (它是能力授予面: 远程改写白名单后可经 run 执行任意命令)。fail-closed 改造
// 新增了等价的授予面 set_allowlist_any, 它一步就能把白名单换成"允许任意命令",
// 若不进同一份清单, 远程对端用 set_allowlist_any{Any:true} 即可抹掉 fail-closed
// 的全部收益。
//
// 这条断言直接查 restrictedRemoteCommand 的返回值, 而不是只比对命令名——名字
// 对了但清单没覆盖, 恰恰是这个缺口原本的形态。
func TestCmdAbilitySetAllowAnyRemoteRestriction(t *testing.T) {
	if CmdCommandSetAllowAny == CmdCommandSetAllowlist {
		t.Fatal("set_allowlist_any 必须与 set_allowlist 是不同命令名, 否则远程限制清单会误判")
	}
	for _, act := range []string{CmdCommandSetAllowAny, ShCommandSetAllowAny, BashCommandSetAllowAny} {
		if act != "set_allowlist_any" {
			t.Fatalf("%q 的命令名必须稳定为 set_allowlist_any, 否则远程限制清单无法按名匹配", act)
		}
	}
	// 三个 shell 能力都必须被判为"仅本地": component 不同但命令名相同。
	for _, component := range []string{"CmdAbility", "ShAbility", "BashAbility"} {
		for _, command := range []string{CmdCommandSetAllowlist, CmdCommandSetAllowAny} {
			local, hit := restrictedRemoteCommand(component, command)
			if !local || hit != command {
				t.Fatalf("%s/%s 必须是仅本地调用 (local=%v hit=%q)", component, command, local, hit)
			}
		}
		// run 仍须保持远程可达: 否则远程编排能力全废, 且这不是本缺口要修的东西。
		if local, _ := restrictedRemoteCommand(component, CmdCommandRun); local {
			t.Fatalf("%s/run 不应被加入本地限制清单", component)
		}
	}
}

func TestCmdAbilityRunNonZeroExit(t *testing.T) {
	shell, ok := pickShell()
	if !ok {
		t.Skip("no standard shell on this platform")
	}
	c := NewCmdAbility()
	atom := newCmdAtom(t)
	c.SetAllowlist([]CmdAllowlistEntry{
		{Name: shell, ArgsPrefix: []string{"-c"}, MaxArgs: 2},
	})
	out := c.Command(atom, CmdCommandRun, CmdRunArgs{
		Name:    shell,
		Args:    []string{"-c", "exit 7"},
		Timeout: 5 * time.Second,
	})
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	res := out.Value.(CmdResult)
	if res.ExitCode != 7 {
		t.Fatalf("exit code = %d, want 7", res.ExitCode)
	}
}

func TestCmdAbilityRunTimeout(t *testing.T) {
	shell, ok := pickShell()
	if !ok {
		t.Skip("no standard shell on this platform")
	}
	c := NewCmdAbility()
	atom := newCmdAtom(t)
	c.SetAllowlist([]CmdAllowlistEntry{
		{Name: shell, ArgsPrefix: []string{"-c"}, MaxArgs: 2},
	})
	out := c.Command(atom, CmdCommandRun, CmdRunArgs{
		Name:    shell,
		Args:    []string{"-c", "sleep 5"},
		Timeout: 200 * time.Millisecond,
	})
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	res := out.Value.(CmdResult)
	if res.ExitCode == 0 {
		t.Fatalf("expected non-zero exit on timeout, got 0")
	}
}

func TestCmdAbilityStartWaitKill(t *testing.T) {
	shell, ok := pickShell()
	if !ok {
		t.Skip("no standard shell on this platform")
	}
	c := NewCmdAbility()
	atom := newCmdAtom(t)
	c.SetAllowlist([]CmdAllowlistEntry{
		{Name: shell, ArgsPrefix: []string{"-c"}, MaxArgs: 2},
	})
	// start
	startOut := c.Command(atom, CmdCommandStart, CmdRunArgs{
		Name: shell,
		Args: []string{"-c", "sleep 0.2; printf done"},
	})
	if startOut.Err != nil {
		t.Fatal(startOut.Err)
	}
	jobID, ok := startOut.Value.(string)
	if !ok || !strings.HasPrefix(jobID, "job-") {
		t.Fatalf("start value = %#v", startOut.Value)
	}
	// wait 0 → 阻塞直到完成
	waitOut := c.Command(atom, CmdCommandWait, CmdWaitArgs{JobID: jobID})
	if waitOut.Err != nil {
		t.Fatal(waitOut.Err)
	}
	res := waitOut.Value.(CmdResult)
	if res.ExitCode != 0 || res.Stdout != "done" {
		t.Fatalf("wait result = %+v", res)
	}
	// wait 不存在的 job
	if out := c.Command(atom, CmdCommandWait, CmdWaitArgs{JobID: "job-9999"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("wait missing job error = %v", out.Err)
	}
	// wait 空 ID
	if out := c.Command(atom, CmdCommandWait, CmdWaitArgs{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("wait empty id error = %v", out.Err)
	}
	// wait 负 timeout
	if out := c.Command(atom, CmdCommandWait, CmdWaitArgs{JobID: jobID, Wait: -1}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("wait negative error = %v", out.Err)
	}
	// get_job
	getOut := c.Command(atom, CmdCommandGetJob, CmdJobIDArg{JobID: jobID})
	if getOut.Err != nil {
		t.Fatal(getOut.Err)
	}
	// get_job 不存在
	if out := c.Command(atom, CmdCommandGetJob, CmdJobIDArg{JobID: "job-9999"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get missing job error = %v", out.Err)
	}
	// get_job 空(旧实现 t.Logf 代替断言——实现回归为接受空 ID 时测试仍 PASS)
	if out := c.Command(atom, CmdCommandGetJob, CmdJobIDArg{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get empty job error = %v", out.Err)
	}
	// kill 已完成 job
	if out := c.Command(atom, CmdCommandKill, CmdKillArgs{JobID: jobID}); out.Err != nil {
		t.Fatalf("kill finished job: %v", out.Err)
	}
	// kill 空
	if out := c.Command(atom, CmdCommandKill, CmdKillArgs{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("kill empty error = %v", out.Err)
	}
	// kill 不存在
	if out := c.Command(atom, CmdCommandKill, CmdKillArgs{JobID: "job-9999"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("kill missing error = %v", out.Err)
	}
	// list
	if out := c.Command(atom, CmdCommandList, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("list with args error = %v", out.Err)
	}
	listOut := c.Command(atom, CmdCommandList, nil)
	if listOut.Err != nil {
		t.Fatal(listOut.Err)
	}
	if jobs, _ := listOut.Value.([]CmdJob); len(jobs) == 0 {
		t.Fatalf("list should not be empty after run")
	}
	// clear_finished
	if out := c.Command(atom, CmdCommandClearJobs, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("clear with args error = %v", out.Err)
	}
	clearOut := c.Command(atom, CmdCommandClearJobs, nil)
	if clearOut.Err != nil {
		t.Fatal(clearOut.Err)
	}
	if n, _ := clearOut.Value.(int); n == 0 {
		t.Fatalf("clear_finished count = 0, want > 0")
	}
}

func TestCmdAbilityUnknownCommand(t *testing.T) {
	c := NewCmdAbility()
	atom := newCmdAtom(t)
	if out := c.Command(atom, "nope", nil); !errors.Is(out.Err, types.ErrUnsupportedCommand) {
		t.Fatalf("unknown error = %v", out.Err)
	}
}

func TestCmdAbilityRejectsMissingDependency(t *testing.T) {
	c := NewCmdAbility()
	// 缺 BaseData
	if out := c.Command(&types.Atom{}, CmdCommandRun, CmdRunArgs{Name: "echo"}); !errors.Is(out.Err, types.ErrMissingDependency) {
		t.Fatalf("missing deps error = %v", out.Err)
	}
}

// TestCmdAllowlistArgsPrefixIsolation 回归: SetAllowlist/allowlistSnapshot
// 旧实现与调用方切片共享 ArgsPrefix 底层数组——调用方 set 后改切片元素会
// 无锁改写白名单(数据竞争 + 可绕过参数前缀约束), 返回值同样可污染内部。
func TestCmdAllowlistArgsPrefixIsolation(t *testing.T) {
	c := NewCmdAbility()
	prefix := []string{"--input"}
	// set 后改入参: 不应影响内部条目(旧实现共享底层数组, 此处会改写)
	c.SetAllowlist([]CmdAllowlistEntry{{Name: "tool", ArgsPrefix: prefix}})
	prefix[0] = "--output"
	snap := c.allowlistSnapshot()
	if len(snap) != 1 || len(snap[0].ArgsPrefix) != 1 || snap[0].ArgsPrefix[0] != "--input" {
		t.Fatalf("set-then-mutate leaked into internal allowlist: %+v", snap)
	}
	// snapshot 返回值改元素: 不应影响内部(并发 matchAllowlist 安全)
	snap[0].ArgsPrefix[0] = "EVIL"
	snap2 := c.allowlistSnapshot()
	if snap2[0].ArgsPrefix[0] != "--input" {
		t.Fatalf("mutating snapshot leaked into internal allowlist: %+v", snap2)
	}
	// 匹配面仍按内部值工作
	if _, ok, _ := c.matchAllowlist("tool", []string{"--input"}); !ok {
		t.Fatalf("matchAllowlist should still accept --input")
	}
	// fail-closed: 空 allowlist 时 matchAllowlist 返回 emptyAllowlist=true
	c.SetAllowlist(nil)
	if _, ok, empty := c.matchAllowlist("tool", nil); ok || !empty {
		t.Fatalf("matchAllowlist with empty allowlist = (%v,%v,%v), want ok=false empty=true", false, ok, empty)
	}
}
