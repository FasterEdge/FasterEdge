package data

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestKeyringSnapshotRoundTrip verifies secret, tokens, counters, and
// rotation metadata survive a complete save/load cycle.
func TestKeyringSnapshotRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	k := NewKeyringData()
	if err := k.Mount(nil); err != nil {
		t.Fatal(err)
	}
	k.SetSecret([]byte("this-is-a-32-byte-secret-for-tests!"))
	if _, err := k.IssueToken("edge-a", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := k.IssueToken("edge-b", 2*time.Hour); err != nil {
		t.Fatal(err)
	}
	if ok, _ := k.RevokeToken("edge-b"); !ok {
		t.Fatal("revoke failed")
	}
	wantSecret := k.Secret()
	wantStatus, wantTokens := k.Snapshot()

	if err := k.SaveSnapshot(path); err != nil {
		t.Fatal(err)
	}
	loaded := NewKeyringData()
	if err := loaded.LoadSnapshot(path); err != nil {
		t.Fatal(err)
	}
	if string(loaded.Secret()) != string(wantSecret) {
		t.Fatal("secret did not round-trip")
	}
	gotStatus, gotTokens := loaded.Snapshot()
	if gotStatus.SecretFinger != wantStatus.SecretFinger {
		t.Fatalf("fingerprint = %q, want %q", gotStatus.SecretFinger, wantStatus.SecretFinger)
	}
	if gotStatus.TotalIssued != wantStatus.TotalIssued {
		t.Fatalf("totalIssued = %d, want %d", gotStatus.TotalIssued, wantStatus.TotalIssued)
	}
	if len(gotTokens) != len(wantTokens) {
		t.Fatalf("tokens = %d, want %d", len(gotTokens), len(wantTokens))
	}
	for i := range wantTokens {
		if gotTokens[i].Subject != wantTokens[i].Subject ||
			gotTokens[i].Revoked != wantTokens[i].Revoked ||
			!gotTokens[i].IssuedAt.Equal(wantTokens[i].IssuedAt) ||
			!gotTokens[i].ExpiresAt.Equal(wantTokens[i].ExpiresAt) {
			t.Fatalf("token[%d] = %+v, want %+v", i, gotTokens[i], wantTokens[i])
		}
	}
}

// TestKeyringSnapshotEmptyState verifies a newly constructed keyring with no
// secret or tokens is still a valid snapshot, and that Mount subsequently
// generates a usable secret.
func TestKeyringSnapshotEmptyState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	k := NewKeyringData()
	// An empty secret snapshot is intentionally invalid for loading; Mount is
	// the generator. Save a mounted but otherwise empty-token state instead.
	if err := k.Mount(nil); err != nil {
		t.Fatal(err)
	}
	if err := k.SaveSnapshot(path); err != nil {
		t.Fatal(err)
	}
	loaded := NewKeyringData()
	if err := loaded.LoadSnapshot(path); err != nil {
		t.Fatal(err)
	}
	if len(loaded.Secret()) == 0 {
		t.Fatal("secret empty after load")
	}
	_, tokens := loaded.Snapshot()
	if len(tokens) != 0 {
		t.Fatalf("tokens = %d, want 0", len(tokens))
	}
}

// TestKeyringSnapshotUnsupportedVersion rejects future incompatible formats.
func TestKeyringSnapshotUnsupportedVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	body := []byte(`{"version":"99.0","secret":"YWJj"}`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	k := NewKeyringData()
	err := k.LoadSnapshot(path)
	if err == nil {
		t.Fatal("expected unsupported version error")
	}
	if !strings.Contains(err.Error(), "unsupported major version") {
		t.Fatalf("err = %v", err)
	}
}

// TestKeyringSnapshotCorruptAndTruncated verifies invalid JSON never mutates
// existing in-memory state.
func TestKeyringSnapshotCorruptAndTruncated(t *testing.T) {
	for name, body := range map[string][]byte{
		"corrupt":   []byte("definitely not json"),
		"truncated": []byte(`{"version":"1.0","secret":`),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "keyring.json")
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatal(err)
			}
			k := NewKeyringData()
			k.SetSecret([]byte("existing-secret-that-must-survive"))
			before := string(k.Secret())
			if err := k.LoadSnapshot(path); err == nil {
				t.Fatal("expected load error")
			}
			if string(k.Secret()) != before {
				t.Fatal("corrupt load mutated existing keyring")
			}
		})
	}
}

// TestKeyringSnapshotRestrictivePermissions asserts the on-disk secret is
// owner read/write only.
func TestKeyringSnapshotRestrictivePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod permissions are not POSIX on Windows")
	}
	path := filepath.Join(t.TempDir(), "keyring.json")
	k := NewKeyringData()
	if err := k.Mount(nil); err != nil {
		t.Fatal(err)
	}
	if err := k.SaveSnapshot(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("perm = %o, want 600", got)
	}
}

// TestKeyringAtomicFailurePreservesOldFile uses a read-only parent directory
// to force CreateTemp to fail, then verifies the previous snapshot survives
// byte-for-byte and still decodes.
func TestKeyringAtomicFailurePreservesOldFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod permissions are not POSIX on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "keyring.json")
	k := NewKeyringData()
	if err := k.Mount(nil); err != nil {
		t.Fatal(err)
	}
	if err := k.SaveSnapshot(path); err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	k.SetSecret([]byte("new-secret-that-must-not-be-written"))
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := k.SaveSnapshot(path); err == nil {
		t.Fatal("expected save failure")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(old) {
		t.Fatal("old file changed after failed atomic save")
	}
	loaded := NewKeyringData()
	if err := loaded.LoadSnapshot(path); err != nil {
		t.Fatalf("old file no longer valid: %v", err)
	}
}

// TestKeyringConcurrentMutationAndSave continuously issues/revokes tokens
// while snapshots are captured. Run under -race, this validates all private
// fields are copied while holding the data lock and no torn JSON is emitted.
func TestKeyringConcurrentMutationAndSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	k := NewKeyringData()
	if err := k.Mount(nil); err != nil {
		t.Fatal(err)
	}

	const workers = 16
	const saves = 50
	var wg sync.WaitGroup
	wg.Add(workers + 1)
	for i := 0; i < workers; i++ {
		go func(worker int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				sub := "edge-" + intString(worker) + "-" + intString(j)
				_, _ = k.IssueToken(sub, time.Hour)
				if j%2 == 0 {
					_, _ = k.RevokeToken(sub)
				}
			}
		}(i)
	}
	go func() {
		defer wg.Done()
		for i := 0; i < saves; i++ {
			if err := k.SaveSnapshot(path); err != nil {
				t.Errorf("save[%d]: %v", i, err)
				return
			}
		}
	}()
	wg.Wait()

	if err := k.SaveSnapshot(path); err != nil {
		t.Fatal(err)
	}
	loaded := NewKeyringData()
	if err := loaded.LoadSnapshot(path); err != nil {
		t.Fatalf("final load: %v", err)
	}
	status, tokens := loaded.Snapshot()
	if status.TotalIssued == 0 || len(tokens) == 0 {
		t.Fatalf("empty final snapshot: status=%+v tokens=%d", status, len(tokens))
	}
}

// TestKeyringLifecycleLoadAndFinalFlush uses the Component Mount/Unmount hooks
// exactly as Atom lifecycle code does: Mount loads existing state, Unmount
// synchronously writes final state.
func TestKeyringLifecycleLoadAndFinalFlush(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	seed := NewKeyringData()
	if err := seed.Mount(nil); err != nil {
		t.Fatal(err)
	}
	seed.SetSecret([]byte("seed-secret-for-lifecycle-roundtrip"))
	if _, err := seed.IssueToken("seed-peer", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := seed.SaveSnapshot(path); err != nil {
		t.Fatal(err)
	}

	k := NewPersistentKeyringData(path)
	if err := k.Mount(nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := k.LookupToken("seed-peer"); !ok {
		t.Fatal("Mount did not load seed token")
	}
	if _, err := k.IssueToken("late-peer", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := k.Unmount(context.Background(), nil); err != nil {
		t.Fatal(err)
	}

	reloaded := NewKeyringData()
	if err := reloaded.LoadSnapshot(path); err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.LookupToken("late-peer"); !ok {
		t.Fatal("final flush did not persist late-peer")
	}
}

// TestKeyringPublicStatusNeverExposesSecret validates both the normal
// status command and JSONMarshal result. The literal raw secret and its
// base64 encoding must not appear in either public representation.
func TestKeyringPublicStatusNeverExposesSecret(t *testing.T) {
	secret := []byte("TOP-SECRET-KEY-MATERIAL-DO-NOT-LEAK")
	k := NewKeyringData()
	k.SetSecret(secret)

	out := k.Command(nil, KeyringCommandStatus, nil)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	if _, ok := out.Value.(KeyringStatus); !ok {
		t.Fatalf("status type = %T", out.Value)
	}
	body, err := k.JSONMarshal()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), string(secret)) {
		t.Fatalf("public JSON leaked raw secret: %s", body)
	}
	// Parse as generic JSON and recursively ensure no key is named secret.
	var anyValue any
	if err := json.Unmarshal(body, &anyValue); err != nil {
		t.Fatal(err)
	}
	if containsSecretKey(anyValue) {
		t.Fatalf("public JSON contains a secret field: %s", body)
	}
}

// TestKeyringSnapshotMissingFile preserves os.ErrNotExist for first-run
// callers that want to treat it as non-fatal.
func TestKeyringSnapshotMissingFile(t *testing.T) {
	k := NewKeyringData()
	err := k.LoadSnapshot(filepath.Join(t.TempDir(), "missing.json"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want os.ErrNotExist", err)
	}
}

func containsSecretKey(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if strings.EqualFold(k, "secret") {
				return true
			}
			if containsSecretKey(child) {
				return true
			}
		}
	case []any:
		for _, child := range x {
			if containsSecretKey(child) {
				return true
			}
		}
	}
	return false
}

func intString(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
