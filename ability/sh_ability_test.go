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

func newShAtom(t *testing.T) *types.Atom {
	t.Helper()
	a := &types.Atom{}
	if err := a.AddData(&data.BaseData{}); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestShAbilityAllowlistCRUD(t *testing.T) {
	s := NewShAbility()
	atom := newShAtom(t)
	// 类型错误
	if out := s.Command(atom, ShCommandSetAllowlist, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set wrong type error = %v", out.Err)
	}
	// get with args
	if out := s.Command(atom, ShCommandGetAllowlist, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get with args error = %v", out.Err)
	}
	// 设置子命令 allowlist
	if out := s.Command(atom, ShCommandSetAllowlist, ShSetAllowlistArgs{Allowed: []string{"echo", "printf"}}); out.Err != nil {
		t.Fatal(out.Err)
	}
	getOut := s.Command(atom, ShCommandGetAllowlist, nil)
	if getOut.Err != nil {
		t.Fatal(getOut.Err)
	}
	al, ok := getOut.Value.(ShAllowlist)
	if !ok || al.Shell != "sh" {
		t.Fatalf("allowlist = %#v", getOut.Value)
	}
	if len(al.Allowed) != 2 {
		t.Fatalf("allowed = %#v", al.Allowed)
	}
}

func TestShAbilityRejectsNotAllowed(t *testing.T) {
	s := NewShAbility()
	atom := newShAtom(t)
	s.SetAllowed([]string{"echo"})
	// 类型错误
	if out := s.Command(atom, ShCommandRun, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("run wrong type error = %v", out.Err)
	}
	// 空命令
	if out := s.Command(atom, ShCommandRun, ShRunArgs{Command: "   "}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("empty command error = %v", out.Err)
	}
	// 非法子命令
	if out := s.Command(atom, ShCommandRun, ShRunArgs{Command: "rm -rf /tmp/abc"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("not allowed subcommand error = %v", out.Err)
	}
}

// TestShAbilityFailClosed 验证 fail-closed 授权语义:
// 未配置 allowlist(空)时, run/start 一律以 ErrAllowlistEmpty 拒绝, 不再"不限制"。
func TestShAbilityFailClosed(t *testing.T) {
	s := NewShAbility()
	atom := newShAtom(t)
	// 新实例 allowlist 为空 → 缺省即拒绝(旧实现此处放行, 是 fail-open 洞)
	if out := s.Command(atom, ShCommandRun, ShRunArgs{Command: "printf hello"}); !errors.Is(out.Err, ErrAllowlistEmpty) {
		t.Fatalf("default empty allowlist run: err=%v (want ErrAllowlistEmpty, fail-closed)", out.Err)
	}
	if out := s.Command(atom, ShCommandStart, ShRunArgs{Command: "printf hello"}); !errors.Is(out.Err, ErrAllowlistEmpty) {
		t.Fatalf("default empty allowlist start: err=%v (want ErrAllowlistEmpty, fail-closed)", out.Err)
	}
	// 显式 set_allowlist 空列表同样回落到 fail-closed
	if out := s.Command(atom, ShCommandSetAllowlist, ShSetAllowlistArgs{}); out.Err != nil {
		t.Fatalf("set empty allowlist: %v", out.Err)
	}
	if out := s.Command(atom, ShCommandRun, ShRunArgs{Command: "printf hello"}); !errors.Is(out.Err, ErrAllowlistEmpty) {
		t.Fatalf("set-empty allowlist run: err=%v (want ErrAllowlistEmpty)", out.Err)
	}
}

// TestShAbilitySetAllowAny 验证显式 opt-in:SetAllowAny(true) 放行任意命令,
// false 回到 fail-closed; 且 get_allowlist 能区分两种状态。
func TestShAbilitySetAllowAny(t *testing.T) {
	s := NewShAbility()
	atom := newShAtom(t)
	if out := s.Command(atom, ShCommandSetAllowAny, "raw"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set_allowlist_any wrong type: %v", out.Err)
	}
	// opt-in 打开前仍拒绝(不产生状态)
	if out := s.Command(atom, ShCommandRun, ShRunArgs{Command: "printf hi"}); !errors.Is(out.Err, ErrAllowlistEmpty) {
		t.Fatalf("before allow-any run: err=%v (want ErrAllowlistEmpty)", out.Err)
	}
	// 显式 opt-in
	if out := s.Command(atom, ShCommandSetAllowAny, CmdSetAllowAnyArgs{Any: true}); out.Err != nil {
		t.Fatalf("set_allowlist_any(true): %v", out.Err)
	}
	if !s.AllowAny() {
		t.Fatal("AllowAny() = false, want true after opt-in")
	}
	// 打开后任意命令被放行(不会因白名单为空而被拒)
	if out := s.Command(atom, ShCommandRun, ShRunArgs{Command: "rm -rf /tmp/x"}); errors.Is(out.Err, ErrAllowlistEmpty) {
		t.Fatalf("run after allow-any wrongly reports empty allowlist: %v", out.Err)
	}
	// get_allowlist 明确标识 AllowAny
	alv := s.Command(atom, ShCommandGetAllowlist, nil).Value.(ShAllowlist)
	if !alv.AllowAny || len(alv.Allowed) != 1 || alv.Allowed[0] != "*" {
		t.Fatalf("get_allowlist after allow-any = %+v (want AllowAny=true, [*])", alv)
	}
	// 关闭 → 回到 fail-closed
	if out := s.Command(atom, ShCommandSetAllowAny, CmdSetAllowAnyArgs{Any: false}); out.Err != nil {
		t.Fatalf("set_allowlist_any(false): %v", out.Err)
	}
	if s.AllowAny() {
		t.Fatal("AllowAny() = true, want false after opt-out")
	}
	if out := s.Command(atom, ShCommandRun, ShRunArgs{Command: "printf hi"}); !errors.Is(out.Err, ErrAllowlistEmpty) {
		t.Fatalf("run after opt-out: err=%v (want ErrAllowlistEmpty)", out.Err)
	}
}

// TestShAbilityRejectsChainingBypass 验证首词白名单不能被 shell 链接元字符绕过。
func TestShAbilityRejectsChainingBypass(t *testing.T) {
	s := NewShAbility()
	atom := newShAtom(t)
	s.SetAllowed([]string{"printf", "echo"})
	bypassCases := []string{
		"printf 'x'; touch /tmp/sh-bypass-1",
		"echo hi && rm -rf /tmp/sh-bypass-2",
		"echo hi | wc -l",
		"echo hi > /tmp/sh-bypass-3",
		"echo hi $(rm -rf /tmp/sh-bypass-4)",
		"echo hi `touch /tmp/sh-bypass-5`",
		"printf 'x'\ntouch /tmp/sh-bypass-6",
		"echo \"$(touch /tmp/sh-bypass-7)\"",
	}
	for _, c := range bypassCases {
		if out := s.Command(atom, ShCommandRun, ShRunArgs{Command: c, Timeout: 2 * time.Second}); !errors.Is(out.Err, types.ErrInvalidArguments) {
			t.Fatalf("bypass case %q should be rejected, err=%v", c, out.Err)
		}
	}
	// 合法单命令仍可通过 (单引号内 \n 是字面量)
	okCases := []string{
		"printf 'sh-ok\\n'",
		"echo hello",
		"printf 'semi;colon'",
	}
	for _, c := range okCases {
		if out := s.Command(atom, ShCommandRun, ShRunArgs{Command: c, Timeout: 2 * time.Second}); out.Err != nil {
			t.Fatalf("legit case %q should pass, err=%v", c, out.Err)
		}
	}
}

func TestShAbilityRunEcho(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no standard shell on this platform")
	}
	s := NewShAbility()
	// fail-closed 语义(空 allowlist 拒绝一切)由 TestShAbilityAllowlistCRUD
	// 断言; 本用例要跑真实执行, 因此显式配白名单。
	s.SetAllowed([]string{"printf"})
	atom := newShAtom(t)
	if out := s.Command(atom, ShCommandRun, ShRunArgs{Command: "printf hello-sh"}); out.Err != nil {
		t.Fatal(out.Err)
	} else {
		res, ok := out.Value.(CmdResult)
		if !ok {
			t.Fatalf("result type = %T", out.Value)
		}
		if res.ExitCode != 0 || res.Stdout != "hello-sh" {
			t.Fatalf("result = %+v", res)
		}
	}
}

func TestShAbilityStartWaitKill(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no standard shell on this platform")
	}
	s := NewShAbility()
	// 同上: 本用例跑真实 start/wait/kill, 白名单必须显式给出。
	s.SetAllowed([]string{"sleep", "printf"})
	atom := newShAtom(t)
	// 长任务用于 kill: 单条命令, 不含 shell 链接元字符(白名单会整体拒绝复合
	// 命令, 见 hasShellChaining), 因此用一条纯 sleep 而不是 "sleep; printf"。
	startOut := s.Command(atom, ShCommandStart, ShRunArgs{Command: "sleep 30"})
	if startOut.Err != nil {
		t.Fatal(startOut.Err)
	}
	jobID, ok := startOut.Value.(string)
	if !ok || !strings.HasPrefix(jobID, "job-") {
		t.Fatalf("start value = %#v", startOut.Value)
	}
	// wait 空 ID
	if out := s.Command(atom, ShCommandWait, ShWaitArgs{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("wait empty id error = %v", out.Err)
	}
	// kill
	if out := s.Command(atom, ShCommandKill, ShKillArgs{JobID: jobID}); out.Err != nil {
		t.Fatal(out.Err)
	}

	// 短任务用于 wait 的返回值: 同样必须是单条命令。
	shortOut := s.Command(atom, ShCommandStart, ShRunArgs{Command: "printf done-sh"})
	if shortOut.Err != nil {
		t.Fatal(shortOut.Err)
	}
	shortID, ok := shortOut.Value.(string)
	if !ok || !strings.HasPrefix(shortID, "job-") {
		t.Fatalf("start value = %#v", shortOut.Value)
	}
	waitOut := s.Command(atom, ShCommandWait, ShWaitArgs{JobID: shortID})
	if waitOut.Err != nil {
		t.Fatal(waitOut.Err)
	}
	res := waitOut.Value.(CmdResult)
	if res.ExitCode != 0 || res.Stdout != "done-sh" {
		t.Fatalf("wait = %+v", res)
	}
	// 类型错误
	if out := s.Command(atom, ShCommandKill, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("kill wrong type error = %v", out.Err)
	}
	// list
	if out := s.Command(atom, ShCommandList, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("list with args error = %v", out.Err)
	}
	listOut := s.Command(atom, ShCommandList, nil)
	if listOut.Err != nil {
		t.Fatal(listOut.Err)
	}
	if jobs, _ := listOut.Value.([]CmdJob); len(jobs) == 0 {
		t.Fatalf("list should not be empty")
	}
}

func TestShAbilitySetShell(t *testing.T) {
	s := NewShAbility()
	s.SetShell("  ")
	s.SetShell("bash")
	atom := newShAtom(t)
	al := s.Command(atom, ShCommandGetAllowlist, nil).Value.(ShAllowlist)
	if al.Shell != "bash" {
		t.Fatalf("shell = %q", al.Shell)
	}
}

func TestShAbilityUnknownCommand(t *testing.T) {
	s := NewShAbility()
	atom := newShAtom(t)
	if out := s.Command(atom, "nope", nil); !errors.Is(out.Err, types.ErrUnsupportedCommand) {
		t.Fatalf("unknown error = %v", out.Err)
	}
}

func TestShAbilityRejectsMissingDependency(t *testing.T) {
	s := NewShAbility()
	if out := s.Command(&types.Atom{}, ShCommandRun, ShRunArgs{Command: "echo hi"}); !errors.Is(out.Err, types.ErrMissingDependency) {
		t.Fatalf("missing deps error = %v", out.Err)
	}
}

func TestShAbilityRunTimeout(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no standard shell on this platform")
	}
	s := NewShAbility()
	s.SetAllowed([]string{"sleep"})
	atom := newShAtom(t)
	if out := s.Command(atom, ShCommandRun, ShRunArgs{Command: "sleep 5", Timeout: 200 * time.Millisecond}); out.Err != nil {
		t.Fatal(out.Err)
	} else {
		res := out.Value.(CmdResult)
		if res.ExitCode == 0 {
			t.Fatalf("expected non-zero exit on timeout, got 0")
		}
	}
}

// TestShAbilityRejectsExtendedChainingBypass 补充任务要求的绕过形态用例:
// 换行(含 \r)、$()、反引号、管道按位或 |&、进程替换 <(、ANSI-C 引号 $'...'。
// 这些形态在"引号外字符级扫描"范围内全部被 hasShellChaining 拒绝;
// 反斜杠转义(编码绕过)不被字符级扫描解码, 该局限如实记录为文档化行为,
// 断言固定"当前实现", 不假装已拦截(详见 hasShellChaining 注释与 README)。
func TestShAbilityRejectsExtendedChainingBypass(t *testing.T) {
	s := NewShAbility()
	atom := newShAtom(t)
	s.SetAllowed([]string{"printf", "echo"})
	// 已被字符级扫描拦截的形态
	rejected := []string{
		"echo hi\rprintf pwned",       // 回车控制字符(hasShellChaining 的控制字符分支)
		"echo hi $'\\n' printf pwned", // ANSI-C 引号: $ 在引号外即拦截
		"echo hi |& tee /dev/stderr",  // 管道按位或
		"echo <(printf injected)",     // 进程替换(输入)
		"echo a >(printf injected)",   // 进程替换(输出)
		"printf 'x'`touch /tmp/x`",    // 反引号命令替换
		"printf 'x'$(touch /tmp/y)",   // $() 命令替换
		"printf 'x'\ntouch /tmp/z",    // 换行分隔命令
	}
	for _, c := range rejected {
		if out := s.Command(atom, ShCommandRun, ShRunArgs{Command: c, Timeout: 2 * time.Second}); !errors.Is(out.Err, types.ErrInvalidArguments) {
			t.Fatalf("extended bypass %q should be rejected, err=%v", c, out.Err)
		}
	}
	// 反斜杠转义的编码绕过(\x3b 分号 / \u003c 尖括号)当前不展开解码,
	// 按字面量处理——断言当前实现行为, 便于未来改成真解码器时翻转。
	// 断言点放在"未被授权层拒绝"(Err 为 nil)而非"进程一定跑起来":
	// 无 sh 的平台上 exec 启动失败只体现为 ExitCode=-1, 仍满足 Err==nil。
	for _, c := range []string{
		"printf 'x'\\x3btouch /tmp/x-enc",
		"echo hi \\u003c /etc/passwd",
	} {
		out := s.Command(atom, ShCommandRun, ShRunArgs{Command: c, Timeout: 2 * time.Second})
		if out.Err != nil {
			t.Fatalf("encoding case %q: 当前实现按字面量放行, 不应被授权层拒绝, got %v", c, out.Err)
		}
		if _, ok := out.Value.(CmdResult); !ok {
			t.Fatalf("encoding case %q: 期望走到执行层并返回 CmdResult(退出码可为 -1), got %T (%v)", c, out.Value, out.Value)
		}
	}
}
