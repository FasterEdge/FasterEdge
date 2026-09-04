package ability

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/FasterEdge/FasterEdge/data"
	"github.com/FasterEdge/FasterEdge/types"
)

func newOneKeyAtom(t *testing.T) *types.Atom {
	t.Helper()
	a := &types.Atom{}
	if err := a.AddData(&data.BaseData{}); err != nil {
		t.Fatal(err)
	}
	if err := a.AddData(data.NewNetMapData()); err != nil {
		t.Fatal(err)
	}
	if err := a.AddData(data.NewKeyringData()); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestOneKeyAbilityRejectsMissingDependencies(t *testing.T) {
	o := NewOneKeyAbility()
	atom := &types.Atom{}
	if out := o.Command(atom, OneKeyCommandStatus, nil); !errors.Is(out.Err, types.ErrMissingDependency) {
		t.Fatalf("missing deps error = %v", out.Err)
	}
}

func TestOneKeyAbilityIssueAndVerify(t *testing.T) {
	o := NewOneKeyAbility()
	atom := newOneKeyAtom(t)
	if err := atom.MountAll(); err != nil {
		t.Fatal(err)
	}
	// 类型不匹配
	if out := o.Command(atom, OneKeyCommandIssueToken, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("wrong type error = %v", out.Err)
	}
	// 缺 subject
	if out := o.Command(atom, OneKeyCommandIssueToken, OneKeyIssueTokenArgs{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("blank subject error = %v", out.Err)
	}
	// 负 TTL
	if out := o.Command(atom, OneKeyCommandIssueToken, OneKeyIssueTokenArgs{Subject: "edge-1", TTL: -1}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("negative ttl error = %v", out.Err)
	}
	// 不支持的算法
	if out := o.Command(atom, OneKeyCommandIssueToken, OneKeyIssueTokenArgs{Subject: "edge-1", Algorithm: "AES"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("bad algo error = %v", out.Err)
	}
	// 正常签发
	out := o.Command(atom, OneKeyCommandIssueToken, OneKeyIssueTokenArgs{Subject: "edge-1", TTL: time.Hour})
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	tok, ok := out.Value.(OneKeyToken)
	if !ok || tok.Subject != "edge-1" || tok.Signature == "" {
		t.Fatalf("token = %#v", out.Value)
	}
	// 正常校验
	verifyOut := o.Command(atom, OneKeyCommandVerifyToken, OneKeyVerifyTokenArgs{
		Subject:   tok.Subject,
		IssuedAt:  tok.IssuedAt,
		ExpiresAt: tok.ExpiresAt,
		Signature: tok.Signature,
	})
	if verifyOut.Err != nil {
		t.Fatalf("verify: %v", verifyOut.Err)
	}
	if verifyOut.Value != "edge-1" {
		t.Fatalf("verify value = %v", verifyOut.Value)
	}
	// 校验:类型错误
	if out := o.Command(atom, OneKeyCommandVerifyToken, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("verify wrong type error = %v", out.Err)
	}
	// 校验:空 subject / 空签名
	if out := o.Command(atom, OneKeyCommandVerifyToken, OneKeyVerifyTokenArgs{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("verify empty error = %v", out.Err)
	}
	// 校验:过期
	if out := o.Command(atom, OneKeyCommandVerifyToken, OneKeyVerifyTokenArgs{
		Subject:   tok.Subject,
		IssuedAt:  tok.IssuedAt,
		ExpiresAt: time.Now().Add(-time.Hour),
		Signature: tok.Signature,
	}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("expired error = %v", out.Err)
	}
	// 校验:未知 subject
	if out := o.Command(atom, OneKeyCommandVerifyToken, OneKeyVerifyTokenArgs{
		Subject:   "ghost",
		IssuedAt:  tok.IssuedAt,
		ExpiresAt: tok.ExpiresAt,
		Signature: tok.Signature,
	}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("unknown subject error = %v", out.Err)
	}
	// 校验:错误签名
	if out := o.Command(atom, OneKeyCommandVerifyToken, OneKeyVerifyTokenArgs{
		Subject:   tok.Subject,
		IssuedAt:  tok.IssuedAt,
		ExpiresAt: tok.ExpiresAt,
		Signature: tok.Signature + "AA",
	}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("bad signature error = %v", out.Err)
	}
}

func TestOneKeyAbilityRevokeAndRotate(t *testing.T) {
	o := NewOneKeyAbility()
	atom := newOneKeyAtom(t)
	if err := atom.MountAll(); err != nil {
		t.Fatal(err)
	}
	// 签发
	tokOut := o.Command(atom, OneKeyCommandIssueToken, OneKeyIssueTokenArgs{Subject: "edge-2", TTL: time.Hour})
	if tokOut.Err != nil {
		t.Fatal(tokOut.Err)
	}
	tok := tokOut.Value.(OneKeyToken)
	// 吊销
	if out := o.Command(atom, OneKeyCommandRevokeToken, OneKeyRevokeTokenArgs{Subject: "edge-2"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// 校验已被吊销的令牌
	if out := o.Command(atom, OneKeyCommandVerifyToken, OneKeyVerifyTokenArgs{
		Subject:   tok.Subject,
		IssuedAt:  tok.IssuedAt,
		ExpiresAt: tok.ExpiresAt,
		Signature: tok.Signature,
	}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("revoked verify error = %v", out.Err)
	}
	// 重复吊销
	if out := o.Command(atom, OneKeyCommandRevokeToken, OneKeyRevokeTokenArgs{Subject: "edge-2"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("re-revoke error = %v", out.Err)
	}
	// 类型错误
	if out := o.Command(atom, OneKeyCommandRevokeToken, nil); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("revoke nil error = %v", out.Err)
	}
	if out := o.Command(atom, OneKeyCommandRevokeToken, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("revoke wrong type error = %v", out.Err)
	}
	// 重新签发 + 旋转密钥
	if out := o.Command(atom, OneKeyCommandIssueToken, OneKeyIssueTokenArgs{Subject: "edge-3", TTL: time.Hour}); out.Err != nil {
		t.Fatal(out.Err)
	}
	rotOut := o.Command(atom, OneKeyCommandRotate, struct{}{})
	if rotOut.Err == nil {
		t.Fatalf("rotate with args should reject")
	}
	rotOut = o.Command(atom, OneKeyCommandRotate, nil)
	if rotOut.Err != nil {
		t.Fatal(rotOut.Err)
	}
	// rotate 之后,旧的 edge-3 令牌应无法再校验(因为 KeyringData.rotate 会清空令牌表)
	// 先重新签发
	tok3 := o.Command(atom, OneKeyCommandIssueToken, OneKeyIssueTokenArgs{Subject: "edge-3", TTL: time.Hour}).Value.(OneKeyToken)
	// rotate 第二次
	if out := o.Command(atom, OneKeyCommandRotate, nil); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := o.Command(atom, OneKeyCommandVerifyToken, OneKeyVerifyTokenArgs{
		Subject:   tok3.Subject,
		IssuedAt:  tok3.IssuedAt,
		ExpiresAt: tok3.ExpiresAt,
		Signature: tok3.Signature,
	}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("after rotate verify error = %v", out.Err)
	}
}

func TestOneKeyAbilityListAndStatus(t *testing.T) {
	o := NewOneKeyAbility()
	atom := newOneKeyAtom(t)
	if err := atom.MountAll(); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"a", "b"} {
		if out := o.Command(atom, OneKeyCommandIssueToken, OneKeyIssueTokenArgs{Subject: sub, TTL: time.Hour}); out.Err != nil {
			t.Fatal(out.Err)
		}
	}
	if out := o.Command(atom, OneKeyCommandListTokens, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("list with args error = %v", out.Err)
	}
	listOut := o.Command(atom, OneKeyCommandListTokens, nil)
	if listOut.Err != nil {
		t.Fatal(listOut.Err)
	}
	tokens, ok := listOut.Value.([]data.KeyringToken)
	if !ok || len(tokens) != 2 {
		t.Fatalf("list value = %#v", listOut.Value)
	}
	if out := o.Command(atom, OneKeyCommandStatus, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("status with args error = %v", out.Err)
	}
	stOut := o.Command(atom, OneKeyCommandStatus, nil)
	if stOut.Err != nil {
		t.Fatal(stOut.Err)
	}
	st, ok := stOut.Value.(data.KeyringStatus)
	if !ok || st.Algorithm != "HMAC-SHA256" {
		t.Fatalf("status = %#v", stOut.Value)
	}
	// revoke_all
	allOut := o.Command(atom, OneKeyCommandRevokeAll, struct{}{})
	if allOut.Err == nil {
		t.Fatalf("revoke_all with args should reject")
	}
	allOut = o.Command(atom, OneKeyCommandRevokeAll, nil)
	if allOut.Err != nil {
		t.Fatal(allOut.Err)
	}
	if n, _ := allOut.Value.(int); n != 2 {
		t.Fatalf("revoke_all count = %d, want 2", n)
	}
	// unknown
	if out := o.Command(atom, "nope", nil); !errors.Is(out.Err, types.ErrUnsupportedCommand) {
		t.Fatalf("unknown error = %v", out.Err)
	}
}

func TestOneKeyAbilityEncodeDecode(t *testing.T) {
	now := time.Now()
	tok := OneKeyToken{
		Subject:   "edge-1",
		IssuedAt:  now,
		ExpiresAt: now.Add(time.Hour),
		Signature: "abcDEF-_123",
	}
	enc := EncodeForTransmission(tok)
	if !strings.HasPrefix(enc, "edge-1.") {
		t.Fatalf("encoded = %q", enc)
	}
	dec, err := DecodeFromTransmission(enc)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Subject != tok.Subject || dec.Signature != tok.Signature {
		t.Fatalf("decoded = %#v", dec)
	}
	if !dec.ExpiresAt.Equal(tok.ExpiresAt) {
		t.Fatalf("expires mismatch: %s vs %s", dec.ExpiresAt, tok.ExpiresAt)
	}
	// 错误格式
	if _, err := DecodeFromTransmission("not.enough.parts"); err == nil {
		t.Fatal("malformed should error")
	}
	if _, err := DecodeFromTransmission("a.b.c.###not-base64###"); err == nil {
		t.Fatal("bad encoding should error")
	}
	if _, err := DecodeFromTransmission("a.bb.c.zzz"); err == nil {
		t.Fatal("bad timestamp should error")
	}
	// subject 可安全包含 "." (如主机名 edge-1.local): 从尾部解析, 前缀整体为 subject
	dotted := OneKeyToken{
		Subject:   "edge-1.local",
		IssuedAt:  now,
		ExpiresAt: now.Add(30 * time.Minute),
		Signature: "aBcDeF-_9xYz",
	}
	dec2, err := DecodeFromTransmission(EncodeForTransmission(dotted))
	if err != nil {
		t.Fatalf("dotted subject round-trip: %v", err)
	}
	if dec2.Subject != "edge-1.local" || dec2.Signature != dotted.Signature {
		t.Fatalf("dotted decoded = %#v", dec2)
	}
}
