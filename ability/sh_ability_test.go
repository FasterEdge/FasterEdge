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
	atom := newShAtom(t)
	startOut := s.Command(atom, ShCommandStart, ShRunArgs{Command: "sleep 0.1; printf done-sh"})
	if startOut.Err != nil {
		t.Fatal(startOut.Err)
	}
	jobID, ok := startOut.Value.(string)
	if !ok || !strings.HasPrefix(jobID, "job-") {
		t.Fatalf("start value = %#v", startOut.Value)
	}
	// wait
	waitOut := s.Command(atom, ShCommandWait, ShWaitArgs{JobID: jobID})
	if waitOut.Err != nil {
		t.Fatal(waitOut.Err)
	}
	res := waitOut.Value.(CmdResult)
	if res.ExitCode != 0 || res.Stdout != "done-sh" {
		t.Fatalf("wait = %+v", res)
	}
	// wait 空 ID
	if out := s.Command(atom, ShCommandWait, ShWaitArgs{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("wait empty id error = %v", out.Err)
	}
	// kill
	if out := s.Command(atom, ShCommandKill, ShKillArgs{JobID: jobID}); out.Err != nil {
		t.Fatal(out.Err)
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
