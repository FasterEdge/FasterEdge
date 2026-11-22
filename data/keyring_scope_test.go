package data

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/FasterEdge/FasterEdge/types"
)

// TestKeyringScopeCovers 覆盖作用域判定的正向/负向矩阵: 组件不在白名单、
// 命令不在白名单、args 指纹不匹配都必须被拒; 空命令表 = 组件内全放行;
// 空作用域 = 不限制(旧格式语义)。
func TestKeyringScopeCovers(t *testing.T) {
	scope, err := NormalizeKeyringScope([]KeyringScopeRule{
		{Component: "NetMapAbility", Commands: []string{"get_topology"}},
		{Component: "ModbusAbility"},
		{Component: "TimeAbility", Commands: []string{"sync_network"}, ArgsFingerprint: "0123456789abcdef"},
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	cases := []struct {
		name        string
		component   string
		command     string
		fingerprint string
		want        bool
	}{
		{"in-scope component+command", "NetMapAbility", "get_topology", "", true},
		{"out-of-scope command", "NetMapAbility", "register_peer", "", false},
		{"out-of-scope component", "RoleAbility", "get_role", "", false},
		{"component-wide rule", "ModbusAbility", "any_command", "", true},
		{"component-wide other command", "ModbusAbility", "read_coils", "", true},
		{"args fingerprint match", "TimeAbility", "sync_network", "0123456789abcdef", true},
		{"args fingerprint mismatch", "TimeAbility", "sync_network", "fedcba9876543210", false},
		{"args fingerprint missing", "TimeAbility", "sync_network", "", false},
	}
	for _, tc := range cases {
		if got := scope.Covers(tc.component, tc.command, tc.fingerprint); got != tc.want {
			t.Errorf("%s: Covers(%q,%q,%q) = %v, want %v", tc.name, tc.component, tc.command, tc.fingerprint, got, tc.want)
		}
	}
	// 空作用域 = 不限制(旧格式令牌语义)
	var empty KeyringScope
	if !empty.Covers("Anything", "anything", "") {
		t.Fatal("empty scope must not restrict")
	}
}

// TestNormalizeKeyringScopeRejectsInvalid 校验作用域规范化拒绝超长/非法字符/
// 重复组件/坏指纹, 并保证规范化是幂等的(同一逻辑作用域只有一种表示,
// 否则签名不可复算)。
func TestNormalizeKeyringScopeRejectsInvalid(t *testing.T) {
	bad := []struct {
		name  string
		rules []KeyringScopeRule
	}{
		{"empty component", []KeyringScopeRule{{Component: "  ", Commands: []string{"ping"}}}},
		{"empty command", []KeyringScopeRule{{Component: "NetMapAbility", Commands: []string{" "}}}},
		{"reserved char", []KeyringScopeRule{{Component: "A:B", Commands: []string{"ping"}}}},
		{"reserved char in command", []KeyringScopeRule{{Component: "NetMapAbility", Commands: []string{"a|b"}}}},
		{"duplicate component", []KeyringScopeRule{{Component: "A"}, {Component: "A"}}},
		{"too many rules", make([]KeyringScopeRule, keyringScopeMaxEntries+1)},
		{"bad fingerprint", []KeyringScopeRule{{Component: "A", Commands: []string{"b"}, ArgsFingerprint: "xyz"}}},
		{"short fingerprint", []KeyringScopeRule{{Component: "A", Commands: []string{"b"}, ArgsFingerprint: "abc"}}},
		{"over-long name", []KeyringScopeRule{{Component: strings.Repeat("x", keyringScopeNameMaxLen+1)}}},
	}
	for i := range bad {
		for j := range bad[i].rules {
			if bad[i].rules[j].Component == "" && j > 0 {
				bad[i].rules[j].Component = "comp"
			}
		}
	}
	for _, tc := range bad {
		if _, err := NormalizeKeyringScope(tc.rules); !errors.Is(err, types.ErrInvalidArguments) {
			t.Errorf("%s: err = %v, want ErrInvalidArguments", tc.name, err)
		}
	}
	// 规范化: 去空白/去重/排序, 且幂等
	once, err := NormalizeKeyringScope([]KeyringScopeRule{
		{Component: " NetMapAbility ", Commands: []string{" b ", "a", "b"}},
		{Component: "ModbusAbility"},
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	twice, err := NormalizeKeyringScope(once)
	if err != nil {
		t.Fatalf("re-normalize: %v", err)
	}
	if len(once) != 2 || once[0].Component != "ModbusAbility" || once[1].Component != "NetMapAbility" {
		t.Fatalf("sorted scope = %#v", once)
	}
	if len(once[1].Commands) != 2 || once[1].Commands[0] != "a" || once[1].Commands[1] != "b" {
		t.Fatalf("commands = %#v", once[1].Commands)
	}
	if len(twice) != len(once) || twice[0].Component != once[0].Component {
		t.Fatalf("not idempotent: %#v vs %#v", once, twice)
	}
	// 空输入 = nil(不限制)
	if got, err := NormalizeKeyringScope(nil); err != nil || got != nil {
		t.Fatalf("nil input: scope=%#v err=%v", got, err)
	}
}

// TestKeyringArgsFingerprintStable 保证同一逻辑参数得到同一指纹
// (字段顺序不同的 map), 不同参数得到不同指纹。
func TestKeyringArgsFingerprintStable(t *testing.T) {
	a := KeyringArgsFingerprint(map[string]string{"a": "1", "b": "2"})
	b := KeyringArgsFingerprint(map[string]string{"b": "2", "a": "1"})
	c := KeyringArgsFingerprint(map[string]string{"a": "1", "b": "3"})
	if a == "" || a != b {
		t.Fatalf("fingerprint not stable: %q vs %q", a, b)
	}
	if a == c {
		t.Fatal("distinct args must produce distinct fingerprints")
	}
	if len(a) != keyringArgsFingerprintHexLen {
		t.Fatalf("fingerprint len = %d, want %d", len(a), keyringArgsFingerprintHexLen)
	}
}

// TestKeyringConsumeNonceReplay 一次性令牌 nonce 只放行一次, 重放即被拒;
// 空 nonce 恒放行(旧格式可重复使用语义)。
func TestKeyringConsumeNonceReplay(t *testing.T) {
	k := NewKeyringData()
	if err := k.Mount(nil); err != nil {
		t.Fatal(err)
	}
	if !k.ConsumeNonce("edge-1", "n1") {
		t.Fatal("first consumption must succeed")
	}
	if k.ConsumeNonce("edge-1", "n1") {
		t.Fatal("replay of the same nonce must be rejected")
	}
	// 不同 subject 的同值 nonce 不冲突(键含 subject)
	if !k.ConsumeNonce("edge-2", "n1") {
		t.Fatal("nonce is per subject")
	}
	// 空 nonce = 可重复使用
	for i := 0; i < 3; i++ {
		if !k.ConsumeNonce("edge-3", "") {
			t.Fatal("empty nonce must always pass")
		}
	}
	// 超长 nonce fail-closed
	if k.ConsumeNonce("edge-4", strings.Repeat("n", keyringNonceMaxLen+1)) {
		t.Fatal("over-long nonce must be rejected")
	}
	// 表容量上限: 淘汰最旧后不应 panic; 淘汰后旧条目可再次消费(FIFO 语义)
	for i := 0; i < keyringNonceMaxEntries+5; i++ {
		key := "n" + strconv.Itoa(i)
		if !k.ConsumeNonce("edge-5", key) {
			t.Fatalf("nonce %d rejected under capacity", i)
		}
	}
	// 第 0 条已被淘汰, 可再次消费; 最新的条目仍被拒绝
	if !k.ConsumeNonce("edge-5", "n0") {
		t.Fatal("evicted nonce should be consumable again")
	}
	if k.ConsumeNonce("edge-5", "n"+strconv.Itoa(keyringNonceMaxEntries+4)) {
		t.Fatal("recent nonce must still be remembered")
	}
}

// TestKeyringScopeBoundToSignature 作用域与 nonce 进入签名载荷:
// 篡改任一字段都会使验签失败——令牌的授权面不可被远端改写。
func TestKeyringScopeBoundToSignature(t *testing.T) {
	k := NewKeyringData()
	if err := k.Mount(nil); err != nil {
		t.Fatal(err)
	}
	tok, err := k.IssueScopedToken("edge-1", time.Hour, []KeyringScopeRule{
		{Component: "NetMapAbility", Commands: []string{"get_topology"}},
	}, "nonce-1")
	if err != nil {
		t.Fatal(err)
	}
	sig := k.Sign(tok)
	if sig == "" {
		t.Fatal("empty signature")
	}
	// 原样校验通过
	if !k.VerifyToken(&KeyringToken{Subject: "edge-1", IssuedAt: tok.IssuedAt, ExpiresAt: tok.ExpiresAt, Scope: tok.Scope, Nonce: "nonce-1"}, sig) {
		t.Fatal("scoped signature must verify")
	}
	// 篡改作用域(加一条规则)被拒
	if k.VerifyToken(&KeyringToken{Subject: "edge-1", IssuedAt: tok.IssuedAt, ExpiresAt: tok.ExpiresAt, Scope: append(append([]KeyringScopeRule{}, tok.Scope...), KeyringScopeRule{Component: "RoleAbility"}), Nonce: "nonce-1"}, sig) {
		t.Fatal("tampered scope must be rejected")
	}
	// 剥离 nonce 被拒(降级为"无 nonce"载荷)
	if k.VerifyToken(&KeyringToken{Subject: "edge-1", IssuedAt: tok.IssuedAt, ExpiresAt: tok.ExpiresAt, Scope: tok.Scope, Nonce: ""}, sig) {
		t.Fatal("stripped nonce must be rejected")
	}
	// 换成旧四参 Verify 也拒绝(载荷版本不同)
	if k.Verify("edge-1", tok.IssuedAt, tok.ExpiresAt, sig) {
		t.Fatal("v2 signature must not verify through the legacy v1 path")
	}
}

// TestKeyringLegacyTokenStillVerifies 旧格式令牌(无作用域、无 nonce)的签名
// 仍可校验: 与旧实现逐字节一致的 v1 载荷在 Verify 中被接受, 同时新签发的
// 同参数令牌(v2 载荷)也能通过 Verify——两端兼容。
func TestKeyringLegacyTokenStillVerifies(t *testing.T) {
	k := NewKeyringData()
	if err := k.Mount(nil); err != nil {
		t.Fatal(err)
	}
	tok, err := k.IssueToken("edge-legacy", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// 手工构造旧格式载荷的签名(v1 文本, 与旧实现一致)
	mac := hmac.New(sha256.New, k.Secret())
	mac.Write(tokenPayload("edge-legacy", tok.IssuedAt, tok.ExpiresAt, nil, "", true))
	legacySig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !k.Verify("edge-legacy", tok.IssuedAt, tok.ExpiresAt, legacySig) {
		t.Fatal("legacy v1 signature must still verify")
	}
	// 篡改任一字段仍被拒
	if k.Verify("edge-legacy", tok.IssuedAt.Add(time.Second), tok.ExpiresAt, legacySig) {
		t.Fatal("tampered legacy token must be rejected")
	}
	// 新签发(v2 载荷, 空作用域)也通过同一入口
	if !k.Verify("edge-legacy", tok.IssuedAt, tok.ExpiresAt, k.Sign(tok)) {
		t.Fatal("newly issued signature must verify through the same entry")
	}
}

// TestKeyringIssueScopedTokenValidation 带作用域签发时的参数校验:
// 非法作用域/超长 nonce 在签发期即拒绝(而不是签出一张不可用的令牌)。
func TestKeyringIssueScopedTokenValidation(t *testing.T) {
	k := NewKeyringData()
	if err := k.Mount(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := k.IssueScopedToken("edge-1", time.Hour, []KeyringScopeRule{{Component: "A:B"}}, ""); !errors.Is(err, types.ErrInvalidArguments) {
		t.Fatalf("bad scope: err = %v", err)
	}
	if _, err := k.IssueScopedToken("edge-1", time.Hour, nil, strings.Repeat("n", keyringNonceMaxLen+1)); !errors.Is(err, types.ErrInvalidArguments) {
		t.Fatalf("over-long nonce: err = %v", err)
	}
	tok, err := k.IssueScopedToken("edge-1", time.Hour, []KeyringScopeRule{{Component: "NetMapAbility", Commands: []string{"get_topology"}}}, "n1")
	if err != nil {
		t.Fatal(err)
	}
	if len(tok.Scope) != 1 || tok.Scope[0].Component != "NetMapAbility" {
		t.Fatalf("scope = %#v", tok.Scope)
	}
	// issue_token 命令路径同样接受作用域
	out := k.Command(nil, KeyringCommandIssueToken, KeyringIssueTokenArgs{
		Subject: "edge-2", TTL: time.Hour,
		Scope: []KeyringScopeRule{{Component: "ModbusAbility"}}, Nonce: "n2",
	})
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	snap, ok := out.Value.(KeyringToken)
	if !ok || len(snap.Scope) != 1 || snap.Nonce != "n2" {
		t.Fatalf("issue_token value = %#v", out.Value)
	}
}

// TestKeyringSnapshotRoundTripsScope 快照往返保留作用域与 nonce,
// 重启后令牌仍按原作用域校验(持久化不丢授权面)。
func TestKeyringSnapshotRoundTripsScope(t *testing.T) {
	path := t.TempDir() + "/keyring.json"
	k := NewKeyringData()
	if err := k.Mount(nil); err != nil {
		t.Fatal(err)
	}
	tok, err := k.IssueScopedToken("edge-1", time.Hour, []KeyringScopeRule{
		{Component: "NetMapAbility", Commands: []string{"get_topology"}},
	}, "nonce-x")
	if err != nil {
		t.Fatal(err)
	}
	if err := k.SaveSnapshot(path); err != nil {
		t.Fatal(err)
	}
	reloaded := NewKeyringData()
	if err := reloaded.LoadSnapshot(path); err != nil {
		t.Fatal(err)
	}
	snap, ok := reloaded.TokenSnapshot("edge-1")
	if !ok {
		t.Fatal("token missing after reload")
	}
	if len(snap.Scope) != 1 || snap.Scope[0].Component != "NetMapAbility" || snap.Nonce != "nonce-x" {
		t.Fatalf("reloaded token = %#v", snap)
	}
	// 重启后旧签名仍可校验(密钥与作用域都往返成功)
	if !reloaded.VerifyToken(&KeyringToken{Subject: "edge-1", IssuedAt: tok.IssuedAt, ExpiresAt: tok.ExpiresAt, Scope: snap.Scope, Nonce: snap.Nonce}, k.Sign(tok)) {
		t.Fatal("signature must survive snapshot round-trip")
	}
}
