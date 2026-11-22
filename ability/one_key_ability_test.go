package ability

import (
	"context"
	"encoding/base64"
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

// TestOneKeyScopedWireFormatRoundTrip 带作用域令牌的跨节点封装往返:
// 作用域与 nonce 必须完整保留(否则远端验签失败); 无作用域令牌退化为旧格式,
// 与旧对端互通。
func TestOneKeyScopedWireFormatRoundTrip(t *testing.T) {
	o := NewOneKeyAbility()
	atom := newOneKeyAtom(t)
	if err := atom.MountAll(); err != nil {
		t.Fatal(err)
	}
	out := o.Command(atom, OneKeyCommandIssueToken, OneKeyIssueTokenArgs{
		Subject: "edge-1.local", TTL: time.Hour, OneShot: true,
		Scope: []data.KeyringScopeRule{
			{Component: "KeyringData", Commands: []string{data.KeyringCommandListTokens}},
		},
	})
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	tok := out.Value.(OneKeyToken)

	wire := EncodeScopedForTransmission(tok)
	if !strings.HasPrefix(wire, "fe2:") {
		t.Fatalf("scoped token must use the v2 envelope, got %q", wire)
	}
	dec, err := DecodeScopedFromTransmission(wire)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dec.Subject != tok.Subject || dec.Signature != tok.Signature || dec.Nonce != tok.Nonce {
		t.Fatalf("decoded = %#v", dec)
	}
	if len(dec.Scope) != 1 || dec.Scope[0].Component != "KeyringData" {
		t.Fatalf("decoded scope = %#v", dec.Scope)
	}
	// 往返后仍能验签(载荷字段没丢)
	if v := o.Command(atom, OneKeyCommandVerifyToken, OneKeyVerifyTokenArgs{
		Subject: dec.Subject, IssuedAt: dec.IssuedAt, ExpiresAt: dec.ExpiresAt,
		Signature: dec.Signature, Scope: dec.Scope, Nonce: dec.Nonce,
	}); v.Err != nil {
		t.Fatalf("verify after round-trip: %v", v.Err)
	}
	// 无作用域令牌 → 旧格式(与旧对端互通)
	plain := o.Command(atom, OneKeyCommandIssueToken, OneKeyIssueTokenArgs{Subject: "edge-legacy", TTL: time.Hour})
	if plain.Err != nil {
		t.Fatal(plain.Err)
	}
	plainTok := plain.Value.(OneKeyToken)
	if wire := EncodeScopedForTransmission(plainTok); strings.HasPrefix(wire, "fe2:") {
		t.Fatalf("unscoped token must stay legacy: %q", wire)
	}
	back, err := DecodeScopedFromTransmission(EncodeScopedForTransmission(plainTok))
	if err != nil || back.Subject != plainTok.Subject {
		t.Fatalf("legacy round-trip: %#v err=%v", back, err)
	}
	// 畸形 v2 封装被拒(不 panic)
	for _, bad := range []string{
		"fe2:###not-base64###",
		"fe2:" + base64.RawURLEncoding.EncodeToString([]byte("not json")),
		"fe2:" + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"a","iat":1,"exp":2}`)),
	} {
		if _, err := DecodeScopedFromTransmission(bad); !errors.Is(err, types.ErrInvalidArguments) {
			t.Fatalf("bad envelope %q: err=%v (want ErrInvalidArguments)", bad, err)
		}
	}
}

func TestParseUnixNanosRejectsOverflow(t *testing.T) {
	// 20 位数字: n*10+d 会回绕 int64, 必须显式拒绝而非接受为负纳秒时间
	over := "99999999999999999999"
	if _, err := parseUnixNanos(over); !errors.Is(err, types.ErrInvalidArguments) {
		t.Fatalf("overflow timestamp %q: err = %v, want ErrInvalidArguments", over, err)
	}
	// MaxInt64 纳秒本身合法
	maxNanos := "9223372036854775807"
	tm, err := parseUnixNanos(maxNanos)
	if err != nil || tm.UnixNano() != int64(^uint64(0)>>1) {
		t.Fatalf("max nanos: tm=%v err=%v", tm, err)
	}
	// 非数字拒绝
	if _, err := parseUnixNanos("12a34"); !errors.Is(err, types.ErrInvalidArguments) {
		t.Fatalf("non-digit: err = %v", err)
	}
	// 空拒绝
	if _, err := parseUnixNanos(""); !errors.Is(err, types.ErrInvalidArguments) {
		t.Fatalf("empty: err = %v", err)
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

// TestOneKeyAbilityRemoteRestrictedCommands 回归第五轮高危缺口: AuthenticateCommand
// 旧实现只拦 OneKeyAbility 四个管理命令与 set_role, 兜底对其余 component/command
// 全放行——持任意有效令牌的对端可远程置换共享密钥(伪造任意 subject 身份)、
// 吊销全集群令牌(认证 DoS)、改写 Cmd/Sh/Bash 白名单(远程 RCE)。
// 现 KeyringData 密钥/令牌管理命令与 set_allowlist 均仅限本地进程内调用。
func TestOneKeyAbilityRemoteRestrictedCommands(t *testing.T) {
	o := NewOneKeyAbility()
	atom := newOneKeyAtom(t)
	if err := atom.AddAbility(o); err != nil {
		t.Fatal(err)
	}
	if err := atom.SetCommandAuthenticator(o); err != nil {
		t.Fatal(err)
	}
	if err := atom.MountAll(); err != nil {
		t.Fatal(err)
	}
	out := o.Command(atom, OneKeyCommandIssueToken, OneKeyIssueTokenArgs{Subject: "edge-1", TTL: time.Hour})
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	tok, ok := out.Value.(OneKeyToken)
	if !ok {
		t.Fatalf("issue value = %#v", out.Value)
	}
	cred := OneKeyVerifyTokenArgs{Subject: tok.Subject, IssuedAt: tok.IssuedAt, ExpiresAt: tok.ExpiresAt, Signature: tok.Signature}

	// KeyringData 密钥/令牌管理命令: 远程拒绝
	for _, cmd := range []string{
		data.KeyringCommandSetSecret, data.KeyringCommandRotate,
		data.KeyringCommandIssueToken, data.KeyringCommandRevokeToken,
		data.KeyringCommandRevokeAll,
	} {
		if r := atom.AuthenticatedCommand(cred, "KeyringData", cmd, nil); !errors.Is(r.Err, types.ErrAuthenticationFailed) {
			t.Fatalf("KeyringData/%s: err=%v (want ErrAuthenticationFailed)", cmd, r.Err)
		}
	}
	// Cmd/Sh/Bash set_allowlist: 远程拒绝
	for _, name := range []string{"CmdAbility", "ShAbility", "BashAbility"} {
		if r := atom.AuthenticatedCommand(cred, name, "set_allowlist", nil); !errors.Is(r.Err, types.ErrAuthenticationFailed) {
			t.Fatalf("%s/set_allowlist: err=%v (want ErrAuthenticationFailed)", name, r.Err)
		}
	}
	// 读面与 verify_token 保持远程可达
	if r := atom.AuthenticatedCommand(cred, "KeyringData", data.KeyringCommandListTokens, nil); r.Err != nil {
		t.Fatalf("KeyringData/list_tokens: %v", r.Err)
	}
	if r := atom.AuthenticatedCommand(cred, "OneKeyAbility", OneKeyCommandVerifyToken, cred); r.Err != nil {
		t.Fatalf("OneKeyAbility/verify_token: %v", r.Err)
	}
}

// newScopedOneKeyAtom 装配带认证器的 Atom 并签发一张作用域令牌,
// 返回 atom / ability / 完整凭据(含 scope 与 nonce)。
func newScopedOneKeyAtom(t *testing.T, subject string, scope []data.KeyringScopeRule, oneShot bool) (*types.Atom, *OneKeyAbility, OneKeyVerifyTokenArgs) {
	t.Helper()
	o := NewOneKeyAbility()
	atom := newOneKeyAtom(t)
	if err := atom.AddAbility(o); err != nil {
		t.Fatal(err)
	}
	if err := atom.SetCommandAuthenticator(o); err != nil {
		t.Fatal(err)
	}
	if err := atom.MountAll(); err != nil {
		t.Fatal(err)
	}
	out := o.Command(atom, OneKeyCommandIssueToken, OneKeyIssueTokenArgs{
		Subject: subject, TTL: time.Hour, Scope: scope, OneShot: oneShot,
	})
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	tok, ok := out.Value.(OneKeyToken)
	if !ok {
		t.Fatalf("issue value = %#v", out.Value)
	}
	return atom, o, OneKeyVerifyTokenArgs{
		Subject: tok.Subject, IssuedAt: tok.IssuedAt, ExpiresAt: tok.ExpiresAt,
		Signature: tok.Signature, Scope: tok.Scope, Nonce: tok.Nonce,
	}
}

// TestOneKeyScopeDeniesOutOfScopeCommands 作用域外的 component/command 被拒,
// 且拒绝先于组件查找: 目标组件未注册时同样返回鉴权失败(不泄露组件存在性)。
func TestOneKeyScopeDeniesOutOfScopeCommands(t *testing.T) {
	// newOneKeyAtom 注册 BaseData/NetMapData/KeyringData; 作用域只允许
	// KeyringData 的一个读命令。
	atom, _, cred := newScopedOneKeyAtom(t, "edge-1", []data.KeyringScopeRule{
		{Component: "KeyringData", Commands: []string{data.KeyringCommandListTokens}},
	}, false)
	// 作用域内的命令放行(基准)
	if r := atom.AuthenticatedCommand(cred, "KeyringData", data.KeyringCommandListTokens, nil); r.Err != nil {
		t.Fatalf("in-scope baseline: %v", r.Err)
	}
	// 作用域外的命令被拒(403: 凭据有效但越权)
	if r := atom.AuthenticatedCommand(cred, "KeyringData", data.KeyringCommandStatus, nil); !errors.Is(r.Err, types.ErrAuthorizationDenied) {
		t.Fatalf("out-of-scope command: err=%v (want ErrAuthorizationDenied)", r.Err)
	}
	// 作用域外的组件被拒
	if r := atom.AuthenticatedCommand(cred, "BaseData", "info", nil); !errors.Is(r.Err, types.ErrAuthorizationDenied) {
		t.Fatalf("out-of-scope component: err=%v (want ErrAuthorizationDenied)", r.Err)
	}
	// 先于组件查找: 目标组件不存在时也是授权拒绝(而非 ErrMissingDependency)
	r := atom.AuthenticatedCommand(cred, "NoSuchAbility", "ping", nil)
	if !errors.Is(r.Err, types.ErrAuthorizationDenied) {
		t.Fatalf("unknown component: err=%v (want ErrAuthorizationDenied before lookup)", r.Err)
	}
	if errors.Is(r.Err, types.ErrMissingDependency) {
		t.Fatal("scope denial must precede component lookup")
	}
}

// TestOneKeyScopeAllowsInScopeCommands 作用域内的调用照常放行;
// 组件内未列命令表的规则放行该组件全部命令。
func TestOneKeyScopeAllowsInScopeCommands(t *testing.T) {
	atom, _, cred := newScopedOneKeyAtom(t, "edge-1", []data.KeyringScopeRule{
		{Component: "KeyringData", Commands: []string{data.KeyringCommandListTokens, data.KeyringCommandStatus}},
		{Component: "BaseData"},
	}, false)
	if r := atom.AuthenticatedCommand(cred, "KeyringData", data.KeyringCommandListTokens, nil); r.Err != nil {
		t.Fatalf("in-scope list_tokens: %v", r.Err)
	}
	// 未列命令表的组件 = 组件内全部命令(读面)
	if r := atom.AuthenticatedCommand(cred, "BaseData", "info", nil); r.Err != nil {
		t.Fatalf("component-wide rule: %v", r.Err)
	}
	// verify_token 是认证自身依赖的命令, 保持远程可达
	if r := atom.AuthenticatedCommand(cred, "OneKeyAbility", OneKeyCommandVerifyToken, cred); r.Err != nil {
		t.Fatalf("verify_token must stay reachable: %v", r.Err)
	}
	// 本地限制清单优先于作用域: 即使令牌覆盖了该命令也不允许远程调用
	wide, _, wideCred := newScopedOneKeyAtom(t, "edge-wide", []data.KeyringScopeRule{
		{Component: "KeyringData"},
	}, false)
	if r := wide.AuthenticatedCommand(wideCred, "KeyringData", data.KeyringCommandRotate, nil); !errors.Is(r.Err, types.ErrAuthenticationFailed) {
		t.Fatalf("restricted command must stay local-only: err=%v", r.Err)
	}
}

// TestOneKeyScopeArgsFingerprint 可选 args 指纹把令牌钉在具体参数上:
// 指纹匹配才放行, 参数变化即被拒。直接调用鉴权入口(不派发到组件),
// 因此断言只反映作用域判定本身, 不受组件参数校验影响。
func TestOneKeyScopeArgsFingerprint(t *testing.T) {
	allowed := NetMapRegisterPeerArgs{Name: "edge-1", Address: "10.0.0.1:7000"}
	fp := data.KeyringArgsFingerprint(allowed)
	atom, o, cred := newScopedOneKeyAtom(t, "edge-1", []data.KeyringScopeRule{
		{Component: "KeyringData", Commands: []string{"info"}, ArgsFingerprint: fp},
	}, false)
	// 指纹匹配 → 放行
	if _, err := o.AuthenticateCommand(context.Background(), atom, cred, "KeyringData", "info", allowed); err != nil {
		t.Fatalf("matching args fingerprint: %v", err)
	}
	// 参数不同 → 指纹不同 → 拒绝(403 而非 401)
	other := NetMapRegisterPeerArgs{Name: "edge-1", Address: "10.0.0.2:7000"}
	_, err := o.AuthenticateCommand(context.Background(), atom, cred, "KeyringData", "info", other)
	if !errors.Is(err, types.ErrAuthorizationDenied) {
		t.Fatalf("mismatched args fingerprint: err=%v (want ErrAuthorizationDenied)", err)
	}
}

// TestOneKeyScopeRejectsTamperedComponentField 远端篡改凭据里的 Scope
// (扩展白名单/剥离字段/改命令表)都会使验签失败——授权面不可被客户端改写。
func TestOneKeyScopeRejectsTamperedComponentField(t *testing.T) {
	atom, o, cred := newScopedOneKeyAtom(t, "edge-1", []data.KeyringScopeRule{
		{Component: "KeyringData", Commands: []string{data.KeyringCommandListTokens}},
	}, false)
	verify := func(c OneKeyVerifyTokenArgs) error {
		return o.Command(atom, OneKeyCommandVerifyToken, c).Err
	}
	// 基准: 原样凭据校验通过
	if err := verify(cred); err != nil {
		t.Fatalf("baseline verify: %v", err)
	}
	// 篡改 1: 扩展白名单(加一条规则)
	widened := cred
	widened.Scope = append(append([]data.KeyringScopeRule{}, cred.Scope...), data.KeyringScopeRule{Component: "BaseData"})
	if err := verify(widened); !errors.Is(err, types.ErrInvalidArguments) {
		t.Fatalf("widened scope: err=%v (want ErrInvalidArguments)", err)
	}
	// 篡改 2: 清空白名单(降级为不限制)
	stripped := cred
	stripped.Scope = nil
	if err := verify(stripped); !errors.Is(err, types.ErrInvalidArguments) {
		t.Fatalf("stripped scope: err=%v (want ErrInvalidArguments)", err)
	}
	// 篡改 3: 改命令表内容
	edited := cred
	edited.Scope = []data.KeyringScopeRule{{Component: "KeyringData", Commands: []string{data.KeyringCommandStatus}}}
	if err := verify(edited); !errors.Is(err, types.ErrInvalidArguments) {
		t.Fatalf("edited commands: err=%v (want ErrInvalidArguments)", err)
	}
	// 篡改 4: 剥离 nonce(一次性令牌降级为可重复使用)
	oneShotAtom, oneShotAbility, oneShotCred := newScopedOneKeyAtom(t, "edge-2", []data.KeyringScopeRule{
		{Component: "KeyringData", Commands: []string{data.KeyringCommandListTokens}},
	}, true)
	if oneShotCred.Nonce == "" {
		t.Fatal("one-shot token must carry a nonce")
	}
	noNonce := oneShotCred
	noNonce.Nonce = ""
	if err := oneShotAbility.Command(oneShotAtom, OneKeyCommandVerifyToken, noNonce).Err; !errors.Is(err, types.ErrInvalidArguments) {
		t.Fatalf("stripped nonce: err=%v (want ErrInvalidArguments)", err)
	}
	// 篡改 component 字段(把白名单指向另一个组件)同样被拒
	swapped := cred
	swapped.Scope = []data.KeyringScopeRule{{Component: "BaseData", Commands: []string{data.KeyringCommandListTokens}}}
	if err := verify(swapped); !errors.Is(err, types.ErrInvalidArguments) {
		t.Fatalf("swapped component: err=%v (want ErrInvalidArguments)", err)
	}
}

// TestOneKeyOneShotTokenReplay 一次性令牌首次放行, 重放被拒;
// 复用同一凭据的第二次调用必须失败。
func TestOneKeyOneShotTokenReplay(t *testing.T) {
	atom, _, cred := newScopedOneKeyAtom(t, "edge-1", []data.KeyringScopeRule{
		{Component: "KeyringData", Commands: []string{data.KeyringCommandListTokens}},
	}, true)
	if cred.Nonce == "" {
		t.Fatal("one-shot token must carry a nonce")
	}
	if r := atom.AuthenticatedCommand(cred, "KeyringData", data.KeyringCommandListTokens, nil); r.Err != nil {
		t.Fatalf("first use: %v", r.Err)
	}
	if r := atom.AuthenticatedCommand(cred, "KeyringData", data.KeyringCommandListTokens, nil); !errors.Is(r.Err, types.ErrAuthenticationFailed) {
		t.Fatalf("replay: err=%v (want ErrAuthenticationFailed)", r.Err)
	}
	// 非一次性令牌在有效期内可重复使用(旧语义)
	atom2, _, cred2 := newScopedOneKeyAtom(t, "edge-2", []data.KeyringScopeRule{
		{Component: "KeyringData", Commands: []string{data.KeyringCommandListTokens}},
	}, false)
	for i := 0; i < 3; i++ {
		if r := atom2.AuthenticatedCommand(cred2, "KeyringData", data.KeyringCommandListTokens, nil); r.Err != nil {
			t.Fatalf("reuse %d: %v", i, r.Err)
		}
	}
}

// TestOneKeyLegacyTokenStillAuthenticates 旧格式令牌(无 Scope/Nonce)
// 仍可校验并通过鉴权, 且保留"全 Atom"语义(不限制作用域)。
func TestOneKeyLegacyTokenStillAuthenticates(t *testing.T) {
	o := NewOneKeyAbility()
	atom := newOneKeyAtom(t)
	if err := atom.AddAbility(o); err != nil {
		t.Fatal(err)
	}
	if err := atom.SetCommandAuthenticator(o); err != nil {
		t.Fatal(err)
	}
	if err := atom.MountAll(); err != nil {
		t.Fatal(err)
	}
	out := o.Command(atom, OneKeyCommandIssueToken, OneKeyIssueTokenArgs{Subject: "edge-legacy", TTL: time.Hour})
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	tok := out.Value.(OneKeyToken)
	// 手工构造"旧格式凭据": 去掉 Scope/Nonce 字段, 签名也用 v1 载荷复算
	kr, ok := atom.Data("KeyringData")
	if !ok {
		t.Fatal("KeyringData missing")
	}
	keyring, ok := kr.(*data.KeyringData)
	if !ok {
		t.Fatalf("KeyringData type = %T", kr)
	}
	legacySig := keyring.SignLegacy(&data.KeyringToken{
		Subject: tok.Subject, IssuedAt: tok.IssuedAt, ExpiresAt: tok.ExpiresAt,
	})
	cred := OneKeyVerifyTokenArgs{
		Subject: tok.Subject, IssuedAt: tok.IssuedAt, ExpiresAt: tok.ExpiresAt, Signature: legacySig,
	}
	// 校验: 旧格式签名通过(兼容)
	if v := o.Command(atom, OneKeyCommandVerifyToken, cred); v.Err != nil {
		t.Fatalf("legacy verify: %v", v.Err)
	}
	// 鉴权: 旧格式令牌不限作用域(任意组件都可调), 且可重复使用
	if r := atom.AuthenticatedCommand(cred, "KeyringData", data.KeyringCommandListTokens, nil); r.Err != nil {
		t.Fatalf("legacy auth: %v", r.Err)
	}
	if r := atom.AuthenticatedCommand(cred, "BaseData", "info", nil); r.Err != nil {
		t.Fatalf("legacy auth reuse: %v", r.Err)
	}
}

// TestOneKeyAuthRateLimit 认证限速: 同一 subject 连续失败超过阈值后被拒,
// 与令牌本身的有效性无关。
func TestOneKeyAuthRateLimit(t *testing.T) {
	atom, _, _ := newScopedOneKeyAtom(t, "edge-1", []data.KeyringScopeRule{
		{Component: "KeyringData", Commands: []string{data.KeyringCommandListTokens}},
	}, false)
	bad := OneKeyVerifyTokenArgs{Subject: "edge-1", IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), Signature: "AAAA"}
	for i := 0; i < oneKeyAuthMaxFailures+5; i++ {
		r := atom.AuthenticatedCommand(bad, "KeyringData", data.KeyringCommandListTokens, nil)
		if r.Err == nil {
			t.Fatalf("attempt %d: bad credential accepted", i)
		}
		if !errors.Is(r.Err, types.ErrAuthenticationFailed) {
			t.Fatalf("attempt %d: err=%v (want ErrAuthenticationFailed)", i, r.Err)
		}
	}
}
