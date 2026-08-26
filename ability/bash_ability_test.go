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

func newBashAtom(t *testing.T) *types.Atom {
	t.Helper()
	a := &types.Atom{}
	if err := a.AddData(&data.BaseData{}); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestBashAbilityAllowlistCRUD(t *testing.T) {
	b := NewBashAbility()
	atom := newBashAtom(t)
	if out := b.Command(atom, BashCommandSetAllowlist, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set wrong type error = %v", out.Err)
	}
	if out := b.Command(atom, BashCommandGetAllowlist, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get with args error = %v", out.Err)
	}
	if out := b.Command(atom, BashCommandSetAllowlist, ShSetAllowlistArgs{Allowed: []string{"echo", "printf"}}); out.Err != nil {
		t.Fatal(out.Err)
	}
	getOut := b.Command(atom, BashCommandGetAllowlist, nil)
	if getOut.Err != nil {
		t.Fatal(getOut.Err)
	}
	al, ok := getOut.Value.(ShAllowlist)
	if !ok || len(al.Allowed) != 2 {
		t.Fatalf("allowlist = %#v", getOut.Value)
	}
}

func TestBashAbilityRejectsWrongArgs(t *testing.T) {
	b := NewBashAbility()
	atom := newBashAtom(t)
	b.SetAllowed([]string{"echo"})
	if out := b.Command(atom, BashCommandRun, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("run wrong type error = %v", out.Err)
	}
	if out := b.Command(atom, BashCommandRun, ShRunArgs{Command: "rm -rf /tmp/x"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("not allowed subcommand error = %v", out.Err)
	}
	if out := b.Command(atom, BashCommandStart, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("start wrong type error = %v", out.Err)
	}
}

func TestBashAbilityRunEcho(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("requires POSIX sh/bash")
	}
	b := NewBashAbility()
	atom := newBashAtom(t)
	b.SetAllowed([]string{"printf"})
	out := b.Command(atom, BashCommandRun, ShRunArgs{Command: "printf hello-bash"})
	if out.Err != nil {
		t.Fatalf("run: %v", out.Err)
	}
	res, ok := out.Value.(CmdResult)
	if !ok {
		t.Fatalf("result type = %T", out.Value)
	}
	if res.ExitCode != 0 || res.Stdout != "hello-bash" {
		t.Fatalf("result = %+v", res)
	}
}

func TestBashAbilityStartWaitKill(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("requires POSIX sh/bash")
	}
	b := NewBashAbility()
	atom := newBashAtom(t)
	b.SetAllowed([]string{"sleep", "printf"})
	startOut := b.Command(atom, BashCommandStart, ShRunArgs{Command: "sleep 0.1; printf bash-done"})
	if startOut.Err != nil {
		t.Fatal(startOut.Err)
	}
	jobID, ok := startOut.Value.(string)
	if !ok || !strings.HasPrefix(jobID, "job-") {
		t.Fatalf("start value = %#v", startOut.Value)
	}
	// wait
	if out := b.Command(atom, BashCommandWait, ShWaitArgs{JobID: jobID}); out.Err != nil {
		t.Fatal(out.Err)
	} else {
		res := out.Value.(CmdResult)
		if res.ExitCode != 0 || res.Stdout != "bash-done" {
			t.Fatalf("wait = %+v", res)
		}
	}
	// kill
	if out := b.Command(atom, BashCommandKill, ShKillArgs{JobID: jobID}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := b.Command(atom, BashCommandKill, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("kill wrong type error = %v", out.Err)
	}
	// list
	if out := b.Command(atom, BashCommandList, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("list with args error = %v", out.Err)
	}
	if out := b.Command(atom, BashCommandList, nil); out.Err != nil {
		t.Fatal(out.Err)
	}
}

func TestBashAbilityRunTimeout(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("requires POSIX sh/bash")
	}
	b := NewBashAbility()
	atom := newBashAtom(t)
	b.SetAllowed([]string{"sleep"})
	out := b.Command(atom, BashCommandRun, ShRunArgs{Command: "sleep 5", Timeout: 200 * time.Millisecond})
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	res := out.Value.(CmdResult)
	if res.ExitCode == 0 {
		t.Fatalf("expected non-zero exit on timeout, got 0")
	}
}

func TestBashAbilityUnknownCommand(t *testing.T) {
	b := NewBashAbility()
	atom := newBashAtom(t)
	if out := b.Command(atom, "nope", nil); !errors.Is(out.Err, types.ErrUnsupportedCommand) {
		t.Fatalf("unknown error = %v", out.Err)
	}
}

func TestBashAbilityRejectsMissingDependency(t *testing.T) {
	b := NewBashAbility()
	if out := b.Command(&types.Atom{}, BashCommandRun, ShRunArgs{Command: "echo hi"}); !errors.Is(out.Err, types.ErrMissingDependency) {
		t.Fatalf("missing deps error = %v", out.Err)
	}
}

func TestShellQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "''"},
		{"echo", "'echo'"},
		{"it's", "'it'\\''s'"},
		{"a'b'c", "'a'\\''b'\\''c'"},
	}
	for _, c := range cases {
		if got := shellQuote(c.in); got != c.want {
			t.Errorf("shellQuote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
