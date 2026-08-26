package data

import (
	"errors"
	"testing"
	"time"

	"github.com/FasterEdge/FasterEdge/types"
)

func TestKeyringDataMountGeneratesSecret(t *testing.T) {
	k := NewKeyringData()
	if err := k.Mount(nil); err != nil {
		t.Fatal(err)
	}
	if len(k.Secret()) == 0 {
		t.Fatal("secret empty after Mount")
	}
}

func TestKeyringDataIssueRevokeLifecycle(t *testing.T) {
	k := NewKeyringData()
	if err := k.Mount(nil); err != nil {
		t.Fatal(err)
	}
	// 缺 subject
	if out := k.Command(nil, KeyringCommandIssueToken, KeyringIssueTokenArgs{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("blank subject error = %v", out.Err)
	}
	// 负 TTL
	if out := k.Command(nil, KeyringCommandIssueToken, KeyringIssueTokenArgs{Subject: "edge-1", TTL: -1}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("negative ttl error = %v", out.Err)
	}
	// 类型不匹配
	if out := k.Command(nil, KeyringCommandIssueToken, "not-typed"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("wrong type error = %v", out.Err)
	}
	// 正常签发
	out := k.Command(nil, KeyringCommandIssueToken, KeyringIssueTokenArgs{Subject: "edge-1", TTL: time.Hour})
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	tok, ok := out.Value.(KeyringToken)
	if !ok || tok.Subject != "edge-1" {
		t.Fatalf("token value = %#v", out.Value)
	}
	// 重复签发同一 subject 应被拒
	if out := k.Command(nil, KeyringCommandIssueToken, KeyringIssueTokenArgs{Subject: "edge-1", TTL: time.Hour}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("duplicate issue error = %v", out.Err)
	}
	// 吊销
	if out := k.Command(nil, KeyringCommandRevokeToken, KeyringRevokeTokenArgs{Subject: "edge-1"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// 再次吊销应失败
	if out := k.Command(nil, KeyringCommandRevokeToken, KeyringRevokeTokenArgs{Subject: "edge-1"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("re-revoke error = %v", out.Err)
	}
	// 吊销后可以再次签发
	if out := k.Command(nil, KeyringCommandIssueToken, KeyringIssueTokenArgs{Subject: "edge-1", TTL: time.Hour}); out.Err != nil {
		t.Fatal(out.Err)
	}
}

func TestKeyringDataSetSecretAndRotate(t *testing.T) {
	k := NewKeyringData()
	if err := k.Mount(nil); err != nil {
		t.Fatal(err)
	}
	oldFinger := k.SecretFingerprint()
	// 类型不匹配
	if out := k.Command(nil, KeyringCommandSetSecret, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("wrong type error = %v", out.Err)
	}
	// 空密钥
	if out := k.Command(nil, KeyringCommandSetSecret, KeyringSetSecretArgs{Secret: "   "}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("blank secret error = %v", out.Err)
	}
	// 短密钥
	if out := k.Command(nil, KeyringCommandSetSecret, KeyringSetSecretArgs{Secret: "short"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("short secret error = %v", out.Err)
	}
	// 正常设置(传入一个超长 base64 字符串)
	if out := k.Command(nil, KeyringCommandSetSecret, KeyringSetSecretArgs{Secret: "this-is-a-very-long-secret-passphrase-for-test-32bytes+"}); out.Err != nil {
		t.Fatal(out.Err)
	} else if out.Value == oldFinger {
		t.Fatalf("fingerprint did not change after set_secret")
	}
	// rotate
	if out := k.Command(nil, KeyringCommandRotate, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("rotate with args error = %v", out.Err)
	}
	rotFinger := k.Command(nil, KeyringCommandRotate, nil).Value.(string)
	if rotFinger == "" {
		t.Fatalf("rotate returned empty fingerprint")
	}
}

func TestKeyringDataListAndRevokeAll(t *testing.T) {
	k := NewKeyringData()
	if err := k.Mount(nil); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"a", "b", "c"} {
		if out := k.Command(nil, KeyringCommandIssueToken, KeyringIssueTokenArgs{Subject: sub, TTL: time.Hour}); out.Err != nil {
			t.Fatal(out.Err)
		}
	}
	listOut := k.Command(nil, KeyringCommandListTokens, struct{}{})
	if listOut.Err == nil {
		t.Fatalf("list with args should reject")
	}
	listOut = k.Command(nil, KeyringCommandListTokens, nil)
	if listOut.Err != nil {
		t.Fatal(listOut.Err)
	}
	tokens, ok := listOut.Value.([]KeyringToken)
	if !ok || len(tokens) != 3 {
		t.Fatalf("list value = %#v", listOut.Value)
	}
	revOut := k.Command(nil, KeyringCommandRevokeAll, struct{}{})
	if revOut.Err == nil {
		t.Fatalf("revoke_all with args should reject")
	}
	revOut = k.Command(nil, KeyringCommandRevokeAll, nil)
	if revOut.Err != nil {
		t.Fatal(revOut.Err)
	}
	if n, _ := revOut.Value.(int); n != 3 {
		t.Fatalf("revoked count = %d, want 3", n)
	}
	// status
	stOut := k.Command(nil, KeyringCommandStatus, struct{}{})
	if stOut.Err == nil {
		t.Fatalf("status with args should reject")
	}
	stOut = k.Command(nil, KeyringCommandStatus, nil)
	if stOut.Err != nil {
		t.Fatal(stOut.Err)
	}
	st, ok := stOut.Value.(KeyringStatus)
	if !ok || st.Algorithm != "HMAC-SHA256" {
		t.Fatalf("status = %#v", stOut.Value)
	}
	// unknown command
	if out := k.Command(nil, "nope", nil); !errors.Is(out.Err, types.ErrUnsupportedCommand) {
		t.Fatalf("unknown error = %v", out.Err)
	}
}

func TestKeyringDataSignAndVerify(t *testing.T) {
	k := NewKeyringData()
	if err := k.Mount(nil); err != nil {
		t.Fatal(err)
	}
	tok, err := k.IssueToken("edge-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	sig := k.Sign(tok)
	if sig == "" {
		t.Fatal("empty signature")
	}
	if !k.Verify("edge-1", tok.IssuedAt, tok.ExpiresAt, sig) {
		t.Fatal("verify should succeed for own signature")
	}
	// 错误 subject
	if k.Verify("edge-2", tok.IssuedAt, tok.ExpiresAt, sig) {
		t.Fatal("verify should fail for wrong subject")
	}
	// 篡改时间
	if k.Verify("edge-1", tok.IssuedAt.Add(time.Second), tok.ExpiresAt, sig) {
		t.Fatal("verify should fail for tampered time")
	}
	// 篡改签名
	if k.Verify("edge-1", tok.IssuedAt, tok.ExpiresAt, sig+"AA") {
		t.Fatal("verify should fail for tampered signature")
	}
	// 错误编码的签名
	if k.Verify("edge-1", tok.IssuedAt, tok.ExpiresAt, "###not-base64###") {
		t.Fatal("verify should fail for bad encoding")
	}
}
