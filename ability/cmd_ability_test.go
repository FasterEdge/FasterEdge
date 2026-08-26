package ability

import (
	"errors"
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
	// get_job 空
	if out := c.Command(atom, CmdCommandGetJob, CmdJobIDArg{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Logf("note: empty get_job error = %v", out.Err)
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
