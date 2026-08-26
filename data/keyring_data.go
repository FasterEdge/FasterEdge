package data

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/FasterEdge/FasterEdge/types"
)

const (
	KeyringCommandStatus      = "status"
	KeyringCommandSetSecret   = "set_secret"
	KeyringCommandRotate      = "rotate"
	KeyringCommandListTokens  = "list_tokens"
	KeyringCommandIssueToken  = "issue_token"
	KeyringCommandRevokeToken = "revoke_token"
	KeyringCommandRevokeAll   = "revoke_all"
)

// KeyringToken 描述一张已签发令牌。
type KeyringToken struct {
	Subject   string // 令牌主题(通常为对等节点名)
	IssuedAt  time.Time
	ExpiresAt time.Time
	Revoked   bool
}

// KeyringStatus 是 KeyringData 状态命令的返回结构。
type KeyringStatus struct {
	Algorithm     string
	SecretFinger  string
	ActiveTokens  int
	TotalIssued   int
	LastRotatedAt time.Time
}

// KeyringSetSecretArgs 是 set_secret 命令的参数。
type KeyringSetSecretArgs struct {
	Secret string
}

// KeyringIssueTokenArgs 是 issue_token 命令的参数。
type KeyringIssueTokenArgs struct {
	Subject   string
	TTL       time.Duration
	Algorithm string // 可选,留空使用默认 (HMAC-SHA256)
}

// KeyringRevokeTokenArgs 是 revoke_token 命令的参数。
type KeyringRevokeTokenArgs struct {
	Subject string
}

// KeyringData 存储本节点加密访问所需的密钥与令牌表。
// 它不做签名/校验,只持有持久化状态 —— 真正的算法逻辑由 OneKeyAbility 提供。
type KeyringData struct {
	mu              sync.RWMutex
	secret          []byte
	lastRotatedAt   time.Time
	totalIssued     int
	revokedCount    int
	tokens          map[string]*KeyringToken // key = subject
	defaultAlgo     string
	defaultTokenTTL time.Duration
}

func NewKeyringData() *KeyringData {
	return &KeyringData{
		tokens:          make(map[string]*KeyringToken),
		defaultAlgo:     "HMAC-SHA256",
		defaultTokenTTL: 24 * time.Hour,
	}
}

func (k *KeyringData) GetName() string { return "KeyringData" }

func (k *KeyringData) Describe() string {
	return "KeyringData存储本节点用于加密访问的共享密钥与令牌表。"
}

func (k *KeyringData) Check(_ *types.Atom) error { return nil }

func (k *KeyringData) Mount(_ *types.Atom) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.secret) == 0 {
		// 默认随机生成 32 字节密钥,避免明文落盘但保证开箱即用
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return fmt.Errorf("KeyringData mount: %w", err)
		}
		k.secret = buf
		k.lastRotatedAt = time.Now()
	}
	return nil
}

// SetSecret 直接覆盖当前密钥(供 Ability 或测试使用)。
func (k *KeyringData) SetSecret(secret []byte) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.secret = append([]byte(nil), secret...)
	k.lastRotatedAt = time.Now()
	k.tokens = make(map[string]*KeyringToken) // 轮换密钥后旧令牌失效
	k.totalIssued = 0
	k.revokedCount = 0
}

// Secret 返回密钥副本(供 Ability 进行 HMAC 签名)。
func (k *KeyringData) Secret() []byte {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return append([]byte(nil), k.secret...)
}

// SecretFingerprint 返回密钥的 SHA-256 指纹(十六进制短串),用于状态展示。
func (k *KeyringData) SecretFingerprint() string {
	k.mu.RLock()
	defer k.mu.RUnlock()
	if len(k.secret) == 0 {
		return ""
	}
	sum := sha256.Sum256(k.secret)
	return hex.EncodeToString(sum[:8])
}

// IssueToken 写入新令牌并返回其引用。Ability 负责签名编码。
func (k *KeyringData) IssueToken(subject string, ttl time.Duration) (*KeyringToken, error) {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return nil, fmt.Errorf("issue token: %w", types.ErrInvalidArguments)
	}
	if ttl <= 0 {
		ttl = k.defaultTokenTTL
	}
	now := time.Now()
	k.mu.Lock()
	defer k.mu.Unlock()
	if t, ok := k.tokens[subject]; ok && !t.Revoked && t.ExpiresAt.After(now) {
		return nil, fmt.Errorf("issue token: subject %q has an active token: %w", subject, types.ErrInvalidArguments)
	}
	tok := &KeyringToken{
		Subject:   subject,
		IssuedAt:  now,
		ExpiresAt: now.Add(ttl),
	}
	k.tokens[subject] = tok
	k.totalIssued++
	return tok, nil
}

// RevokeToken 吊销指定 subject 的令牌;若不存在或已吊销返回 false。
func (k *KeyringData) RevokeToken(subject string) (bool, *KeyringToken) {
	k.mu.Lock()
	defer k.mu.Unlock()
	t, ok := k.tokens[subject]
	if !ok || t.Revoked {
		return false, nil
	}
	t.Revoked = true
	k.revokedCount++
	return true, t
}

// RevokeAll 吊销所有未吊销令牌,返回被吊销的数量。
func (k *KeyringData) RevokeAll() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	n := 0
	for _, t := range k.tokens {
		if !t.Revoked {
			t.Revoked = true
			n++
		}
	}
	k.revokedCount += n
	return n
}

// ActiveToken 返回指定 subject 的当前有效令牌(未吊销且未过期)。
func (k *KeyringData) ActiveToken(subject string) (*KeyringToken, bool) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	t, ok := k.tokens[subject]
	if !ok || t.Revoked || t.ExpiresAt.Before(time.Now()) {
		return nil, false
	}
	return t, true
}

// LookupToken 返回指定 subject 的令牌条目(不做有效性过滤,供校验时使用)。
func (k *KeyringData) LookupToken(subject string) (*KeyringToken, bool) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	t, ok := k.tokens[subject]
	return t, ok
}

// Snapshot 返回状态与令牌表的深拷贝副本(按 Subject 排序)。
func (k *KeyringData) Snapshot() (KeyringStatus, []KeyringToken) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	active := 0
	tokens := make([]KeyringToken, 0, len(k.tokens))
	for _, t := range k.tokens {
		if !t.Revoked && t.ExpiresAt.After(time.Now()) {
			active++
		}
		tokens = append(tokens, *t)
	}
	sort.Slice(tokens, func(i, j int) bool { return tokens[i].Subject < tokens[j].Subject })
	return KeyringStatus{
		Algorithm:     k.defaultAlgo,
		SecretFinger:  fingerprintLocked(k.secret),
		ActiveTokens:  active,
		TotalIssued:   k.totalIssued,
		LastRotatedAt: k.lastRotatedAt,
	}, tokens
}

func fingerprintLocked(secret []byte) string {
	if len(secret) == 0 {
		return ""
	}
	sum := sha256.Sum256(secret)
	return hex.EncodeToString(sum[:8])
}

func (k *KeyringData) Command(_ *types.Atom, act string, args any) types.CommandOutput {
	switch act {
	case KeyringCommandStatus:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		status, _ := k.Snapshot()
		return types.CommandOutput{Name: act, Value: status}
	case KeyringCommandSetSecret:
		typed, ok := args.(KeyringSetSecretArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		raw := strings.TrimSpace(typed.Secret)
		if raw == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: empty secret: %w", act, types.ErrInvalidArguments)}
		}
		decoded, err := decodeSecret(raw)
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %v", act, types.ErrInvalidArguments, err)}
		}
		k.SetSecret(decoded)
		return types.CommandOutput{Name: act, Value: k.SecretFingerprint()}
	case KeyringCommandRotate:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %v", act, types.ErrInvalidArguments, err)}
		}
		k.SetSecret(buf)
		return types.CommandOutput{Name: act, Value: k.SecretFingerprint()}
	case KeyringCommandListTokens:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		_, tokens := k.Snapshot()
		return types.CommandOutput{Name: act, Value: tokens}
	case KeyringCommandIssueToken:
		// 实际上 OneKeyAbility 也实现了此命令以同时返回签名后的 token 字符串;
		// KeyringData 仅提供原始令牌条目,留作直接调用入口。
		typed, ok := args.(KeyringIssueTokenArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		subject := strings.TrimSpace(typed.Subject)
		if subject == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		ttl := typed.TTL
		if ttl < 0 {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: ttl must be positive: %w", act, types.ErrInvalidArguments)}
		}
		tok, err := k.IssueToken(subject, ttl)
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, err)}
		}
		return types.CommandOutput{Name: act, Value: *tok}
	case KeyringCommandRevokeToken:
		typed, ok := args.(KeyringRevokeTokenArgs)
		if !ok || strings.TrimSpace(typed.Subject) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		ok, tok := k.RevokeToken(strings.TrimSpace(typed.Subject))
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no active token for subject: %w", act, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: *tok}
	case KeyringCommandRevokeAll:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: k.RevokeAll()}
	}
	return types.CommandOutput{Name: act, Err: fmt.Errorf("command %s: %w", act, types.ErrUnsupportedCommand)}
}

// Sign 计算给定 subject + expiresAt 的 HMAC 签名,返回 base64 字符串。
// 该函数供 OneKeyAbility 使用,签名内容是 subject|issuedAt.UnixNano|expiresAt.UnixNano。
func (k *KeyringData) Sign(tok *KeyringToken) string {
	secret := k.Secret()
	mac := hmac.New(sha256.New, secret)
	payload := fmt.Sprintf("%s|%d|%d", tok.Subject, tok.IssuedAt.UnixNano(), tok.ExpiresAt.UnixNano())
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Verify 校验 subject + issuedAt + expiresAt + signature 是否与当前密钥一致。
func (k *KeyringData) Verify(subject string, issuedAt, expiresAt time.Time, signature string) bool {
	secret := k.Secret()
	mac := hmac.New(sha256.New, secret)
	payload := fmt.Sprintf("%s|%d|%d", subject, issuedAt.UnixNano(), expiresAt.UnixNano())
	mac.Write([]byte(payload))
	want := mac.Sum(nil)
	got, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return false
	}
	return hmac.Equal(want, got)
}

// decodeSecret 接受 base64 / 原始字符串两种形式的密钥;若 base64 解码后长度>=16 字节则用 base64,否则按 UTF-8 原文。
func decodeSecret(raw string) ([]byte, error) {
	if decoded, err := base64.StdEncoding.DecodeString(raw); err == nil && len(decoded) >= 16 {
		return decoded, nil
	}
	if len(raw) < 16 {
		return nil, fmt.Errorf("secret too short (min 16 bytes)")
	}
	return []byte(raw), nil
}
