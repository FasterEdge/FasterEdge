package ability

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/FasterEdge/FasterEdge/types"
)

// TestFileTransferUnmountCancelsActive verifies Unmount signals every active
// transfer's cancel channel and waits for workers.
func TestFileTransferUnmountCancelsActive(t *testing.T) {
	// cancellingTransport observes Cancel() and unblocks immediately.
	transport := &cancellingTransport{}
	f := NewFileTransferAbility()
	atom := newFileTransferAtom(t)
	registerPeer(t, atom, "edge-2", "10.0.0.2:7000")
	f.Command(atom, FileTransferCommandSetTarget, FileTransferTargetArgs{PeerName: "edge-2"})
	f.SetTransport(transport)
	tmp := filepath.Join(t.TempDir(), "x.txt")
	os.WriteFile(tmp, []byte("x"), 0o644)
	out := f.Command(atom, FileTransferCommandUpload, FileTransferUploadArgs{LocalPath: tmp, RemotePath: "/r"})
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	id := out.Value.(string)
	// Allow worker to start blocking.
	time.Sleep(20 * time.Millisecond)
	if err := f.Unmount(context.Background(), atom); err != nil {
		t.Fatalf("Unmount=%v", err)
	}
	for i := 0; i < 100; i++ {
		getOut := f.Command(atom, FileTransferCommandGet, FileTransferIDArg{ID: id})
		if getOut.Err == nil {
			if t1, _ := getOut.Value.(FileTransfer); t1.Status == FileTransferStatusCanceled {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("transfer not canceled after Unmount")
}

// cancellingTransport blocks on a per-call unblock channel and also watches
// the cancel channel, simulating a well-behaved real transport.
type cancellingTransport struct {
	mu       sync.Mutex
	uploads  int
	download int
	released chan struct{}
}

func (c *cancellingTransport) Upload(ctx FileTransferContext) error {
	c.mu.Lock()
	c.uploads++
	c.released = make(chan struct{})
	release := c.released
	c.mu.Unlock()
	select {
	case <-release:
		return nil
	case <-ctx.Cancel():
		return errors.New("canceled")
	}
}
func (c *cancellingTransport) Download(ctx FileTransferContext) error {
	c.mu.Lock()
	c.download++
	c.released = make(chan struct{})
	release := c.released
	c.mu.Unlock()
	select {
	case <-release:
		return nil
	case <-ctx.Cancel():
		return errors.New("canceled")
	}
}

// TestFileTransferUnmountTimeout validates the bounded-wait contract.
func TestFileTransferUnmountTimeout(t *testing.T) {
	// A transport that ignores Cancel; wg will not complete in time.
	blocking := newNeverEndingTransport()
	f := NewFileTransferAbility()
	atom := newFileTransferAtom(t)
	registerPeer(t, atom, "edge-2", "10.0.0.2:7000")
	f.Command(atom, FileTransferCommandSetTarget, FileTransferTargetArgs{PeerName: "edge-2"})
	f.SetTransport(blocking)
	tmp := filepath.Join(t.TempDir(), "x.txt")
	os.WriteFile(tmp, []byte("x"), 0o644)
	out := f.Command(atom, FileTransferCommandUpload, FileTransferUploadArgs{LocalPath: tmp, RemotePath: "/r"})
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	<-blocking.started
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := f.Unmount(ctx, atom)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	// unblock the transport so goroutines exit.
	blocking.Release()
}

// neverEndingTransport blocks until Release is called and never observes
// the cancel channel — used to force a shutdown timeout.
type neverEndingTransport struct {
	started chan struct{}
	once    sync.Once
	release chan struct{}
}

func newNeverEndingTransport() *neverEndingTransport {
	return &neverEndingTransport{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (n *neverEndingTransport) Upload(_ FileTransferContext) error {
	n.once.Do(func() { close(n.started) })
	<-n.release
	return nil
}
func (n *neverEndingTransport) Download(_ FileTransferContext) error {
	n.once.Do(func() { close(n.started) })
	<-n.release
	return nil
}
func (n *neverEndingTransport) Release() {
	close(n.release)
}

func TestFileTransferRejectsNewCommandsAfterUnmount(t *testing.T) {
	f := NewFileTransferAbility()
	atom := newFileTransferAtom(t)
	registerPeer(t, atom, "edge-2", "10.0.0.2:7000")
	f.Command(atom, FileTransferCommandSetTarget, FileTransferTargetArgs{PeerName: "edge-2"})
	if err := f.Unmount(context.Background(), atom); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(t.TempDir(), "x.txt")
	os.WriteFile(tmp, []byte("x"), 0o644)
	out := f.Command(atom, FileTransferCommandUpload, FileTransferUploadArgs{LocalPath: tmp, RemotePath: "/r"})
	if out.Err == nil {
		t.Fatal("expected upload to be rejected after Unmount")
	}
}

// TestFileTransferConcurrentCommandVsUnmount ensures no panics or data races
// when commands are issued concurrently with Unmount.
func TestFileTransferConcurrentCommandVsUnmount(t *testing.T) {
	transport := &fakeTransport{block: make(chan struct{})}
	f := NewFileTransferAbility()
	atom := newFileTransferAtom(t)
	registerPeer(t, atom, "edge-2", "10.0.0.2:7000")
	f.Command(atom, FileTransferCommandSetTarget, FileTransferTargetArgs{PeerName: "edge-2"})
	f.SetTransport(transport)
	tmp := filepath.Join(t.TempDir(), "x.txt")
	os.WriteFile(tmp, []byte("x"), 0o644)
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.Command(atom, FileTransferCommandUpload, FileTransferUploadArgs{LocalPath: tmp, RemotePath: "/r"})
		}()
	}
	go func() {
		_ = f.Unmount(context.Background(), atom)
	}()
	wg.Wait()
	close(transport.block)
}

// TestFileTransferUnmountIdempotent ensures multiple Unmounts are safe.
func TestFileTransferUnmountIdempotent(t *testing.T) {
	f := NewFileTransferAbility()
	atom := newFileTransferAtom(t)
	if err := f.Unmount(context.Background(), atom); err != nil {
		t.Fatalf("first unmount=%v", err)
	}
	if err := f.Unmount(context.Background(), atom); err != nil {
		t.Fatalf("second unmount=%v", err)
	}
}

// Compile-time assertion that the ability satisfies Unmounter.
var _ types.Unmounter = (*FileTransferAbility)(nil)
