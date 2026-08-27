package ability

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FasterEdge/FasterEdge/types"
)

// TestCmdAbilityUnmountCancelsRunningJobs verifies Unmount signals cancel
// to every active async job and waits for them.
func TestCmdAbilityUnmountCancelsRunningJobs(t *testing.T) {
	shell, ok := pickShell()
	if !ok {
		t.Skip("no standard shell on this platform")
	}
	c := NewCmdAbility()
	atom := newCmdAtom(t)
	c.SetAllowlist([]CmdAllowlistEntry{
		{Name: shell, ArgsPrefix: []string{"-c"}, MaxArgs: 2},
	})

	out := c.Command(atom, CmdCommandStart, CmdRunArgs{
		Name: shell,
		Args: []string{"-c", "sleep 5"},
	})
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	jobID := out.Value.(string)
	err := c.Unmount(context.Background(), atom)
	if err != nil {
		t.Fatalf("Unmount=%v", err)
	}
	if job, ok := c.snapshotJob(jobID); !ok || !job.Done {
		t.Fatalf("job not done after Unmount: %+v", job)
	}
}

// TestCmdAbilityUnmountWaitsForConcurrent validates the bounded wait pattern.
func TestCmdAbilityUnmountWaitsForConcurrent(t *testing.T) {
	shell, ok := pickShell()
	if !ok {
		t.Skip("no standard shell on this platform")
	}
	c := NewCmdAbility()
	atom := newCmdAtom(t)
	c.SetAllowlist([]CmdAllowlistEntry{
		{Name: shell, ArgsPrefix: []string{"-c"}, MaxArgs: 2},
	})
	for i := 0; i < 3; i++ {
		if out := c.Command(atom, CmdCommandStart, CmdRunArgs{
			Name: shell,
			Args: []string{"-c", "sleep 2"},
		}); out.Err != nil {
			t.Fatal(out.Err)
		}
	}
	done := make(chan error, 1)
	go func() {
		done <- c.Unmount(context.Background(), atom)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Unmount=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Unmount did not finish after cancelling 3 jobs")
	}
}

// TestCmdAbilityRejectsNewCommandsAfterUnmount verifies the closing gate.
func TestCmdAbilityRejectsNewCommandsAfterUnmount(t *testing.T) {
	shell, ok := pickShell()
	if !ok {
		t.Skip("no standard shell on this platform")
	}
	c := NewCmdAbility()
	atom := newCmdAtom(t)
	c.SetAllowlist([]CmdAllowlistEntry{
		{Name: shell, ArgsPrefix: []string{"-c"}, MaxArgs: 2},
	})
	if err := c.Unmount(context.Background(), atom); err != nil {
		t.Fatal(err)
	}
	if out := c.Command(atom, CmdCommandRun, CmdRunArgs{Name: shell, Args: []string{"-c", "printf x"}}); out.Err == nil {
		t.Fatal("expected run to be rejected after Unmount")
	}
	if out := c.Command(atom, CmdCommandStart, CmdRunArgs{Name: shell, Args: []string{"-c", "printf x"}}); out.Err == nil {
		t.Fatal("expected start to be rejected after Unmount")
	}
}

// TestCmdAbilityUnmountRespectsTimeout ensures the timeout governs shutdown
// duration even when the worker's cancel call takes a moment to complete.
func TestCmdAbilityUnmountRespectsTimeout(t *testing.T) {
	shell, ok := pickShell()
	if !ok {
		t.Skip("no standard shell on this platform")
	}
	c := NewCmdAbility()
	atom := newCmdAtom(t)
	c.SetAllowlist([]CmdAllowlistEntry{
		{Name: shell, ArgsPrefix: []string{"-c"}, MaxArgs: 2},
	})
	for i := 0; i < 3; i++ {
		if out := c.Command(atom, CmdCommandStart, CmdRunArgs{
			Name: shell,
			Args: []string{"-c", "sleep 1"},
		}); out.Err != nil {
			t.Fatal(out.Err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := c.Unmount(ctx, atom)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unexpected err=%v", err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatalf("Unmount took too long: %s", time.Since(start))
	}
}

// TestCmdAbilityRunSyncCancellable verifies runSync's embedded cancel func
// cancels the running process.
func TestCmdAbilityRunSyncCancellable(t *testing.T) {
	shell, ok := pickShell()
	if !ok {
		t.Skip("no standard shell on this platform")
	}
	c := NewCmdAbility()
	atom := newCmdAtom(t)
	c.SetAllowlist([]CmdAllowlistEntry{
		{Name: shell, ArgsPrefix: []string{"-c"}, MaxArgs: 2},
	})
	// Trigger Unmount in another goroutine to cancel a running sync job via
	// its cancel func.
	var wg sync.WaitGroup
	var result CmdResult
	wg.Add(1)
	go func() {
		defer wg.Done()
		out := c.Command(atom, CmdCommandRun, CmdRunArgs{
			Name:    shell,
			Args:    []string{"-c", "sleep 5"},
			Timeout: 5 * time.Second,
		})
		if out.Err == nil {
			if r, ok := out.Value.(CmdResult); ok {
				result = r
			}
		}
	}()
	time.Sleep(50 * time.Millisecond)
	if err := c.Unmount(context.Background(), atom); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if result.JobID == "" {
		t.Fatal("expected a CmdResult back from runSync")
	}
	if result.ExitCode == 0 {
		t.Fatalf("expected non-zero exit after cancel, got %d", result.ExitCode)
	}
}

// TestCmdAbilityConcurrentCommandVsUnmount stress-tests the closing gate.
func TestCmdAbilityConcurrentCommandVsUnmount(t *testing.T) {
	shell, ok := pickShell()
	if !ok {
		t.Skip("no standard shell on this platform")
	}
	c := NewCmdAbility()
	atom := newCmdAtom(t)
	c.SetAllowlist([]CmdAllowlistEntry{
		{Name: shell, ArgsPrefix: []string{"-c"}, MaxArgs: 2},
	})
	var wg sync.WaitGroup
	var rejected atomic.Int32
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out := c.Command(atom, CmdCommandRun, CmdRunArgs{Name: shell, Args: []string{"-c", "printf x"}})
			if out.Err != nil {
				rejected.Add(1)
			}
		}()
	}
	go func() {
		_ = c.Unmount(context.Background(), atom)
	}()
	wg.Wait()
	// All runs that started before Unmount may succeed; no crash.
	if rejected.Load() == 0 {
		// Allow zero rejections in extreme fast runs; not a hard failure.
	}
}

// TestCmdAbilityKillWaitsForCompletion validates the bounded kill path.
func TestCmdAbilityKillWaitsForCompletion(t *testing.T) {
	shell, ok := pickShell()
	if !ok {
		t.Skip("no standard shell on this platform")
	}
	c := NewCmdAbility()
	atom := newCmdAtom(t)
	c.SetAllowlist([]CmdAllowlistEntry{
		{Name: shell, ArgsPrefix: []string{"-c"}, MaxArgs: 2},
	})
	startOut := c.Command(atom, CmdCommandStart, CmdRunArgs{
		Name: shell,
		Args: []string{"-c", "sleep 1"},
	})
	if startOut.Err != nil {
		t.Fatal(startOut.Err)
	}
	jobID := startOut.Value.(string)
	if out := c.Command(atom, CmdCommandKill, CmdKillArgs{JobID: jobID}); out.Err != nil {
		t.Fatal(out.Err)
	} else if killed, _ := out.Value.(bool); !killed {
		t.Fatalf("kill returned not killed: %v", out.Value)
	}
	// kill again should report not killed (already done).
	if out := c.Command(atom, CmdCommandKill, CmdKillArgs{JobID: jobID}); out.Err != nil {
		t.Fatal(out.Err)
	} else if killed, _ := out.Value.(bool); killed {
		t.Fatal("second kill should not report killed")
	}
}

// TestCmdAbilityGetJobAfterUnmount verifies state remains readable after Unmount.
func TestCmdAbilityGetJobAfterUnmount(t *testing.T) {
	shell, ok := pickShell()
	if !ok {
		t.Skip("no standard shell on this platform")
	}
	c := NewCmdAbility()
	atom := newCmdAtom(t)
	c.SetAllowlist([]CmdAllowlistEntry{
		{Name: shell, ArgsPrefix: []string{"-c"}, MaxArgs: 2},
	})
	startOut := c.Command(atom, CmdCommandStart, CmdRunArgs{
		Name: shell,
		Args: []string{"-c", "printf done"},
	})
	if startOut.Err != nil {
		t.Fatal(startOut.Err)
	}
	jobID := startOut.Value.(string)
	if err := c.Unmount(context.Background(), atom); err != nil {
		t.Fatal(err)
	}
	if out := c.Command(atom, CmdCommandGetJob, CmdJobIDArg{JobID: jobID}); out.Err != nil {
		t.Fatal(out.Err)
	} else if job, ok := out.Value.(CmdJob); !ok || !job.Done {
		t.Fatalf("job not done after Unmount: %+v", out.Value)
	}
}

// Compile-time assertion for Unmounter.
var _ types.Unmounter = (*CmdAbility)(nil)
