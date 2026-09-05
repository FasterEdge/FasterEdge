// ─────────────────────────────────────────────────────────────
// FasterEdge 开源项目
// Github: https://github.com/FasterEdge
// Gitee:  https://gitee.com/FasterEdge
// ─────────────────────────────────────────────────────────────
package data

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/FasterEdge/FasterEdge/types"
)

const (
	keyringSnapshotVersion = "1.0"
	keyringSnapshotMajor   = 1
	keyringSnapshotMaxSize = int64(4 << 20) // 4 MiB
	keyringSnapshotPerm    = os.FileMode(0o600)

	// maxTokenTTL 是单次签发令牌的最大 TTL(30 天)。
	// 旧实现无上限: 显式超大 TTL(如 876000h)可签发约 292 年的"永久"令牌,
	// 使吊销/轮换丧失意义。
	maxTokenTTL = 30 * 24 * time.Hour
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
	snapshotPath    string
}

func NewKeyringData() *KeyringData {
	return &KeyringData{
		tokens:          make(map[string]*KeyringToken),
		defaultAlgo:     "HMAC-SHA256",
		defaultTokenTTL: 24 * time.Hour,
	}
}

// NewPersistentKeyringData constructs a KeyringData that loads its complete
// private state from path on Mount and atomically saves it on Unmount. The
// snapshot includes the secret and therefore is always written with 0600
// permissions. It is never returned by status/Snapshot APIs.
func NewPersistentKeyringData(path string) *KeyringData {
	k := NewKeyringData()
	k.snapshotPath = strings.TrimSpace(path)
	return k
}

// SetSnapshotPath enables lifecycle persistence on an existing KeyringData.
// It must be called before Mount. An empty path disables lifecycle I/O.
func (k *KeyringData) SetSnapshotPath(path string) {
	k.mu.Lock()
	k.snapshotPath = strings.TrimSpace(path)
	k.mu.Unlock()
}

func (k *KeyringData) GetName() string { return "KeyringData" }

func (k *KeyringData) Describe() string {
	return "KeyringData存储本节点用于加密访问的共享密钥与令牌表。"
}

func (k *KeyringData) Check(_ *types.Atom) error { return nil }

func (k *KeyringData) Mount(_ *types.Atom) error {
	k.mu.RLock()
	path := k.snapshotPath
	k.mu.RUnlock()
	if path != "" {
		if err := k.LoadSnapshot(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("KeyringData mount load %s: %w", path, err)
		}
	}
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

// Unmount performs the final atomic snapshot flush for lifecycle-managed
// keyrings. If persistence is disabled it is a no-op.
func (k *KeyringData) Unmount(_ context.Context, _ *types.Atom) error {
	k.mu.RLock()
	path := k.snapshotPath
	k.mu.RUnlock()
	if path == "" {
		return nil
	}
	return k.SaveSnapshot(path)
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
	if ttl > maxTokenTTL {
		return nil, fmt.Errorf("issue token: ttl %s exceeds max %s: %w", ttl, maxTokenTTL, types.ErrInvalidArguments)
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
// 注意: 返回共享指针, 调用方不得在锁外读取可变字段(Revoked 等),
// 否则与 RevokeToken/RevokeAll 构成数据竞争——请改用 TokenSnapshot。
func (k *KeyringData) LookupToken(subject string) (*KeyringToken, bool) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	t, ok := k.tokens[subject]
	return t, ok
}

// TokenSnapshot 返回指定 subject 的令牌**值拷贝**, 供校验路径在锁外安全读取
// (Revoked/IssuedAt/ExpiresAt), 与 LookupToken 的共享指针不同, 不与
// RevokeToken/RevokeAll 构成数据竞争。
func (k *KeyringData) TokenSnapshot(subject string) (KeyringToken, bool) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	t, ok := k.tokens[subject]
	if !ok {
		return KeyringToken{}, false
	}
	return *t, true
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
		_ = tok
		// IssueToken 返回共享 *KeyringToken: 此处若直接 *tok 则是锁外解引用
		// Revoked 字段, 与 RevokeToken/RevokeAll 构成数据竞争——经快照拷贝返回。
		snap, ok := k.TokenSnapshot(subject)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: snap}
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

// keyringSnapshot is the private on-disk representation. It intentionally
// includes the raw secret; no public status or command returns this type.
type keyringSnapshot struct {
	Version         string         `json:"version"`
	SavedAt         time.Time      `json:"savedAt"`
	Secret          string         `json:"secret"`
	LastRotatedAt   time.Time      `json:"lastRotatedAt"`
	TotalIssued     int            `json:"totalIssued"`
	RevokedCount    int            `json:"revokedCount"`
	Tokens          []KeyringToken `json:"tokens"`
	DefaultAlgo     string         `json:"defaultAlgo"`
	DefaultTokenTTL time.Duration  `json:"defaultTokenTTL"`
}

// SaveSnapshot atomically writes the complete KeyringData state to path.
// A sibling temp file is chmod 0600 before bytes are written, fsynced,
// closed, then atomically renamed over the target. Any error before rename
// leaves the existing target untouched.
func (k *KeyringData) SaveSnapshot(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("keyring snapshot: path is required")
	}
	k.mu.RLock()
	tokens := make([]KeyringToken, 0, len(k.tokens))
	for _, tok := range k.tokens {
		tokens = append(tokens, *tok)
	}
	sort.Slice(tokens, func(i, j int) bool { return tokens[i].Subject < tokens[j].Subject })
	snap := keyringSnapshot{
		Version:         keyringSnapshotVersion,
		SavedAt:         time.Now().UTC(),
		Secret:          base64.StdEncoding.EncodeToString(k.secret),
		LastRotatedAt:   k.lastRotatedAt,
		TotalIssued:     k.totalIssued,
		RevokedCount:    k.revokedCount,
		Tokens:          tokens,
		DefaultAlgo:     k.defaultAlgo,
		DefaultTokenTTL: k.defaultTokenTTL,
	}
	k.mu.RUnlock()

	body, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("keyring snapshot: encode: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("keyring snapshot: mkdir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".keyring-*.json.tmp")
	if err != nil {
		return fmt.Errorf("keyring snapshot: temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if err := tmp.Chmod(keyringSnapshotPerm); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("keyring snapshot: chmod: %w", err)
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("keyring snapshot: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("keyring snapshot: fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("keyring snapshot: close: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("keyring snapshot: rename: %w", err)
	}
	if d, err := os.Open(dir); err != nil {
		return fmt.Errorf("keyring snapshot: open dir for sync: %w", err)
	} else {
		// 目录 fsync 是 POSIX 持久化语义(确保 rename 目录项落盘)。
		// Windows 不支持目录 fsync(Sync 恒返回 Access denied)——
		// 属已知平台限制, 降级忽略; 文件 fsync + rename 原子性仍成立。
		if runtime.GOOS != "windows" {
			if serr := d.Sync(); serr != nil {
				_ = d.Close()
				return fmt.Errorf("keyring snapshot: dir fsync: %w", serr)
			}
		}
		_ = d.Close()
	}
	return nil
}

// LoadSnapshot reads and validates a bounded keyring snapshot, then replaces
// the in-memory state in one critical section. Unknown major versions are
// rejected. The raw secret is decoded only inside this private path.
func (k *KeyringData) LoadSnapshot(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("keyring snapshot: path is required")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() > keyringSnapshotMaxSize {
		return fmt.Errorf("keyring snapshot: file too large: %d > %d", info.Size(), keyringSnapshotMaxSize)
	}
	body, err := io.ReadAll(io.LimitReader(f, keyringSnapshotMaxSize+1))
	if err != nil {
		return fmt.Errorf("keyring snapshot: read: %w", err)
	}
	if int64(len(body)) > keyringSnapshotMaxSize {
		return fmt.Errorf("keyring snapshot: exceeded %d-byte limit", keyringSnapshotMaxSize)
	}
	if len(body) == 0 {
		return errors.New("keyring snapshot: empty file")
	}
	var snap keyringSnapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		return fmt.Errorf("keyring snapshot: decode: %w", err)
	}
	major, err := parseKeyringMajor(snap.Version)
	if err != nil {
		return fmt.Errorf("keyring snapshot: invalid version %q: %w", snap.Version, err)
	}
	if major != keyringSnapshotMajor {
		return fmt.Errorf("keyring snapshot: unsupported major version %d (expected %d, full=%q)", major, keyringSnapshotMajor, snap.Version)
	}
	secret, err := base64.StdEncoding.DecodeString(snap.Secret)
	if err != nil || len(secret) == 0 {
		return errors.New("keyring snapshot: missing or invalid secret")
	}
	// 密钥长度与 set_secret 的 16 字节下限一致: 弱密钥(如 4 字节)可直接
	// 被暴力穷举, 加载路径旧实现不校验。
	if len(secret) < 16 {
		return fmt.Errorf("keyring snapshot: secret too short (%d bytes, need >= 16)", len(secret))
	}
	tokens := make(map[string]*KeyringToken, len(snap.Tokens))
	now := time.Now()
	for i := range snap.Tokens {
		tok := snap.Tokens[i]
		if strings.TrimSpace(tok.Subject) == "" {
			return fmt.Errorf("keyring snapshot: tokens[%d] has empty subject", i)
		}
		// 时间窗校验: 签发时间非零、过期晚于签发、且不超未来 maxTokenTTL
		// (旧实现接受 9999 年的"永久"令牌, 绕过签发路径的 TTL 上限)。
		if tok.IssuedAt.IsZero() || !tok.ExpiresAt.After(tok.IssuedAt) {
			return fmt.Errorf("keyring snapshot: tokens[%d] has invalid time window", i)
		}
		if tok.ExpiresAt.After(now.Add(maxTokenTTL)) {
			return fmt.Errorf("keyring snapshot: tokens[%d] expires beyond max ttl", i)
		}
		if _, exists := tokens[tok.Subject]; exists {
			return fmt.Errorf("keyring snapshot: duplicate subject %q", tok.Subject)
		}
		copyTok := tok
		tokens[tok.Subject] = &copyTok
	}
	algo := strings.TrimSpace(snap.DefaultAlgo)
	if algo == "" {
		algo = "HMAC-SHA256"
	}
	ttl := snap.DefaultTokenTTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	if ttl > maxTokenTTL {
		// 被篡改快照的超大默认 TTL 会让后续 IssueToken(subject,0) 命中
		// 上限报错——钳制到上限。
		ttl = maxTokenTTL
	}
	if snap.TotalIssued < 0 || snap.RevokedCount < 0 || snap.RevokedCount > snap.TotalIssued {
		return fmt.Errorf("keyring snapshot: inconsistent counters (total=%d revoked=%d)", snap.TotalIssued, snap.RevokedCount)
	}
	k.mu.Lock()
	k.secret = append([]byte(nil), secret...)
	k.lastRotatedAt = snap.LastRotatedAt
	k.totalIssued = snap.TotalIssued
	k.revokedCount = snap.RevokedCount
	k.tokens = tokens
	k.defaultAlgo = algo
	k.defaultTokenTTL = ttl
	k.mu.Unlock()
	return nil
}

func parseKeyringMajor(v string) (int, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, errors.New("empty version")
	}
	major := strings.SplitN(v, ".", 2)[0]
	n, err := strconv.Atoi(major)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("bad major %q", major)
	}
	return n, nil
}

// JSONMarshal returns only the normal public status shape. It is deliberately
// separate from SaveSnapshot and can never reveal the raw secret.
func (k *KeyringData) JSONMarshal() ([]byte, error) {
	status, tokens := k.Snapshot()
	return json.Marshal(struct {
		Status KeyringStatus  `json:"status"`
		Tokens []KeyringToken `json:"tokens"`
	}{Status: status, Tokens: tokens})
}

// Sign 计算给定 subject + expiresAt 的 HMAC 签名,返回 base64 字符串。
// 该函数供 OneKeyAbility 使用,签名内容是 subject|issuedAt.UnixNano|expiresAt.UnixNano。
func (k *KeyringData) Sign(tok *KeyringToken) string {
	if tok == nil {
		return ""
	}
	secret := k.Secret()
	mac := hmac.New(sha256.New, secret)
	payload := fmt.Sprintf("%s|%d|%d", tok.Subject, tok.IssuedAt.UnixNano(), tok.ExpiresAt.UnixNano())
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Verify 校验 subject + issuedAt + expiresAt + signature 是否与当前密钥一致。
func (k *KeyringData) Verify(subject string, issuedAt, expiresAt time.Time, signature string) bool {
	secret := k.Secret()
	if len(secret) == 0 {
		// 密钥未就绪(未挂载): 空密钥 HMAC 是确定性可计算的, 任意签名都可被伪造
		// 匹配——必须拒绝(fail-closed), 否则未 PreRun 即对外提供认证服务的
		// 进程, 其认证边界形同虚设(伪造任意 subject 令牌)。
		return false
	}
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
