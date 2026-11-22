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
	"unicode/utf8"

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

	// keyringScopeMaxEntries 限制单个作用域白名单的条目数——
	// 无上限时一次签发即可把任意长度的 component/command 列表塞进签名载荷
	// 与持久化快照, 校验期线性扫描随之退化。
	keyringScopeMaxEntries = 32
	// keyringScopeNameMaxLen 限制单个 component/command 名称长度。
	keyringScopeNameMaxLen = 128
	// keyringScopeReserved 是作用域名称中禁止出现的分隔符: 作用域同时用于
	// 签名载荷(JSON, 天然无歧义)与 "component:command" 复合键, 保留字符
	// 一旦出现在名称里, 复合键就可被构造出"两个不同调用同一字符串"的歧义。
	keyringScopeReserved = ",:;|"
	// keyringNonceMaxLen 限制 nonce 长度(防重放表的键)。
	keyringNonceMaxLen = 64
	// keyringNonceMaxEntries 限制内存防重放表的条目上限, 超出后按最旧淘汰,
	// 保证上限内存占用(见 ConsumeNonce 的淘汰策略说明)。
	keyringNonceMaxEntries = 4096

	// keyringArgsFingerprintBytes 是 args 指纹散列后取用的字节数。
	keyringArgsFingerprintBytes = 8
	// keyringArgsFingerprintHexLen 是 args 指纹的十六进制字符长度。
	keyringArgsFingerprintHexLen = keyringArgsFingerprintBytes * 2

	// keyringPayloadV2 是绑定作用域的签名载荷版本标记(JSON 载荷的 "v" 字段)。
	// 旧格式载荷无此字段(纯文本 "subject|issued|expires"), 故不设 V1 常量——
	// 载荷形态本身就是版本区分。
	keyringPayloadV2 = "v2"
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
	// Scope 是令牌绑定的能力作用域(component + command 白名单, 可选 args
	// 指纹)。为 nil/空表示不限制——旧格式令牌与未声明作用域的新令牌均如此,
	// 保持向后兼容。签发路径经 NormalizeKeyringScope 规范化后写入签名载荷,
	// 因此远端篡改 Scope 任何字段都会使 HMAC 校验失败。
	Scope []KeyringScopeRule `json:"scope,omitempty"`
	// Nonce 非空时该令牌为一次性: 每次鉴权消耗一个 nonce, 重放即被拒。
	// nonce 同样在签名载荷内, 剥离 nonce 会破坏签名。
	Nonce string `json:"nonce,omitempty"`
}

// KeyringScopeRule 是单条作用域规则: 限定某个 component 下可执行的命令。
// Commands 为空表示该 component 的全部命令; ArgsFingerprint 非空时, 仅当本次
// 调用参数的 SHA-256 短指纹与之相等才放行(用于把令牌进一步钉在具体参数上,
// 例如只允许 set_endpoint 到某一个固定地址)。
type KeyringScopeRule struct {
	Component       string   `json:"component"`
	Commands        []string `json:"commands,omitempty"`
	ArgsFingerprint string   `json:"argsFingerprint,omitempty"`
}

// KeyringScope 是规范化后的作用域(条目数、名称长度与字符集均受约束,
// 规则按 Component 排序、命令去重排序)。
type KeyringScope []KeyringScopeRule

// Rule 返回覆盖指定 component 的规则; 不存在时 ok 为 false。
func (s KeyringScope) Rule(component string) (KeyringScopeRule, bool) {
	for _, r := range s {
		if r.Component == component {
			return r, true
		}
	}
	return KeyringScopeRule{}, false
}

// Covers 判断作用域是否覆盖本次 (component, command, argsFingerprint) 调用。
// 空作用域表示不限制(旧格式语义); ArgsFingerprint 仅在规则声明了指纹时参与
// 比对, 调用方无关心得时传空串即可。
func (s KeyringScope) Covers(component, command, argsFingerprint string) bool {
	if len(s) == 0 {
		return true
	}
	r, ok := s.Rule(component)
	if !ok {
		return false
	}
	if r.ArgsFingerprint != "" && !strings.EqualFold(r.ArgsFingerprint, argsFingerprint) {
		return false
	}
	if len(r.Commands) == 0 {
		return true
	}
	for _, c := range r.Commands {
		if c == command {
			return true
		}
	}
	return false
}

// NormalizeKeyringScope 校验并规范化作用域: 去空白、去重、排序, 并拒绝
// 超长/非法字符/重复 component 的规则。规范化结果即写入签名载荷的字节,
// 因此同一份逻辑作用域只有一种表示(签名可复算)。
// 输入为空时返回 nil(表示不限制), 与旧格式令牌的载荷完全一致。
func NormalizeKeyringScope(rules []KeyringScopeRule) (KeyringScope, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	if len(rules) > keyringScopeMaxEntries {
		return nil, fmt.Errorf("scope has %d rules, max %d: %w", len(rules), keyringScopeMaxEntries, types.ErrInvalidArguments)
	}
	out := make(KeyringScope, 0, len(rules))
	seen := make(map[string]struct{}, len(rules))
	for i, r := range rules {
		comp, err := normalizeScopeName(r.Component, "component")
		if err != nil {
			return nil, fmt.Errorf("scope[%d]: %w", i, err)
		}
		if _, dup := seen[comp]; dup {
			return nil, fmt.Errorf("scope[%d]: duplicate component %q: %w", i, comp, types.ErrInvalidArguments)
		}
		seen[comp] = struct{}{}
		cmds := make([]string, 0, len(r.Commands))
		cmdSeen := make(map[string]struct{}, len(r.Commands))
		for _, c := range r.Commands {
			name, err := normalizeScopeName(c, "command")
			if err != nil {
				return nil, fmt.Errorf("scope[%d]: %w", i, err)
			}
			if _, dup := cmdSeen[name]; dup {
				continue
			}
			cmdSeen[name] = struct{}{}
			cmds = append(cmds, name)
		}
		sort.Strings(cmds)
		fp := strings.ToLower(strings.TrimSpace(r.ArgsFingerprint))
		if fp != "" {
			if len(fp) != keyringArgsFingerprintHexLen || !isHexString(fp) {
				return nil, fmt.Errorf("scope[%d]: args fingerprint must be %d lowercase hex chars: %w", i, keyringArgsFingerprintHexLen, types.ErrInvalidArguments)
			}
		}
		out = append(out, KeyringScopeRule{Component: comp, Commands: cmds, ArgsFingerprint: fp})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Component < out[j].Component })
	return out, nil
}

func normalizeScopeName(raw, kind string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", fmt.Errorf("empty %s: %w", kind, types.ErrInvalidArguments)
	}
	if !utf8.ValidString(name) {
		return "", fmt.Errorf("%s is not valid UTF-8: %w", kind, types.ErrInvalidArguments)
	}
	if len(name) > keyringScopeNameMaxLen {
		return "", fmt.Errorf("%s exceeds %d bytes: %w", kind, keyringScopeNameMaxLen, types.ErrInvalidArguments)
	}
	if strings.ContainsAny(name, keyringScopeReserved) {
		return "", fmt.Errorf("%s contains reserved character %q: %w", kind, keyringScopeReserved, types.ErrInvalidArguments)
	}
	return name, nil
}

func isHexString(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// KeyringArgsFingerprint 计算命令参数的 SHA-256 短指纹(16 位小写十六进制),
// 供作用域规则把令牌钉在具体参数上。参数经 JSON 规范化编码后再散列, 因此
// 同一逻辑参数(字段顺序不同的结构体/map)得到同一指纹; JSON 不可编码的
// 参数退化为 %#v 文本散列, 仍然确定。
func KeyringArgsFingerprint(args any) string {
	var raw []byte
	if b, err := json.Marshal(args); err == nil {
		raw = b
	} else {
		raw = []byte(fmt.Sprintf("%#v", args))
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:keyringArgsFingerprintBytes])
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
	// Scope 是令牌绑定的能力作用域(component + command 白名单, 可选 args
	// 指纹)。为 nil/空表示不限制(旧格式语义)。规范化和校验见
	// NormalizeKeyringScope。
	Scope []KeyringScopeRule `json:"scope,omitempty"`
	// Nonce 非空时该令牌为一次性: 每次鉴权消耗一个 nonce, 重放即被拒。
	Nonce string `json:"nonce,omitempty"`
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
	// usedNonces 记录已消费的一次性令牌 nonce, 键为 "subject|nonce", 值为
	// 首次消费时间(UnixNano, 用于按最旧淘汰)。仅在令牌声明了 nonce 时写入,
	// 无 nonce 的令牌不受影响(保持旧格式的可重复使用语义)。
	usedNonces map[string]int64
	// nonceOrder 保留 usedNonces 的插入顺序, 供淘汰最旧条目使用。
	// map 迭代顺序随机, 无法据此判定"最旧", 故显式维护 FIFO 队列。
	nonceOrder []string
}

func NewKeyringData() *KeyringData {
	return &KeyringData{
		tokens:          make(map[string]*KeyringToken),
		usedNonces:      make(map[string]int64),
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
			// 熵源不可用是运行期故障——补哨兵(旧实现无哨兵, 调用方无法分类)
			return fmt.Errorf("KeyringData mount: %w: %w", types.ErrOperationFailed, err)
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
	// 密钥轮换后旧令牌已不可用: 防重放表一并清空, 避免为永不出现的令牌
	// 保留条目(表内键含 subject, 与令牌表同步失效)。
	k.usedNonces = make(map[string]int64)
	k.nonceOrder = nil
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
// 等价于 IssueScopedToken(subject, ttl, nil, "")——不限制作用域、可重复使用,
// 与旧格式令牌语义一致。
func (k *KeyringData) IssueToken(subject string, ttl time.Duration) (*KeyringToken, error) {
	return k.IssueScopedToken(subject, ttl, nil, "")
}

// IssueScopedToken 写入带能力作用域(component/command 白名单, 可选 args 指纹)
// 的新令牌; scope 为 nil/空表示不限制作用域, nonce 非空表示该令牌一次性。
// 作用域与 nonce 都进入签名载荷, 因此是不可伪造、不可续期的绑定。
func (k *KeyringData) IssueScopedToken(subject string, ttl time.Duration, scope []KeyringScopeRule, nonce string) (*KeyringToken, error) {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return nil, fmt.Errorf("issue token: %w", types.ErrInvalidArguments)
	}
	// 非 UTF-8 subject 经快照 JSON 落盘会静默腐蚀为 U+FFFD, 往返后
	// LookupToken/Verify 失效、腐蚀重合还会触发 duplicate subject 整盘拒载
	// ——签发即拒绝(与 ConfigData 值校验一致)。
	if !utf8.ValidString(subject) {
		return nil, fmt.Errorf("issue token: subject is not valid UTF-8: %w", types.ErrInvalidArguments)
	}
	normalized, err := NormalizeKeyringScope(scope)
	if err != nil {
		return nil, fmt.Errorf("issue token: %w", err)
	}
	nonce = strings.TrimSpace(nonce)
	if len(nonce) > keyringNonceMaxLen {
		return nil, fmt.Errorf("issue token: nonce exceeds %d bytes: %w", keyringNonceMaxLen, types.ErrInvalidArguments)
	}
	if nonce != "" && !utf8.ValidString(nonce) {
		return nil, fmt.Errorf("issue token: nonce is not valid UTF-8: %w", types.ErrInvalidArguments)
	}
	if ttl <= 0 {
		// defaultTokenTTL 由 LoadSnapshot 在锁内写入——锁外读与并发
		// 加载构成数据竞争(旧实现直接读字段)。
		k.mu.RLock()
		ttl = k.defaultTokenTTL
		k.mu.RUnlock()
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
		Scope:     normalized,
		Nonce:     nonce,
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

// ConsumeNonce 尝试消费一个一次性令牌 nonce: 首次返回 true, 已消费过
// (重放)返回 false。nonce 为空表示令牌不是一次性, 恒返回 true 并保持旧格式
// 的"有效期内可重复使用"语义。
//
// 淘汰策略: 条目数超过 keyringNonceMaxEntries 时淘汰最旧的一条。非一次性
// 令牌(占绝大多数)不写表, 因此上限内只容纳一次性令牌的活跃窗口——一次性
// 令牌数量本就受 maxTokenTTL 与 subject 数约束, 4096 条远未触及; 该上限
// 只是防止攻击者伪造大量一次性 subject 耗尽内存的兜底。
func (k *KeyringData) ConsumeNonce(subject, nonce string) bool {
	subject = strings.TrimSpace(subject)
	nonce = strings.TrimSpace(nonce)
	if nonce == "" {
		return true
	}
	if len(nonce) > keyringNonceMaxLen {
		// 与规范化作用域同纪律: 非法 nonce 一律按"已消费"拒绝(fail-closed)。
		return false
	}
	key := subject + "|" + nonce
	now := time.Now().UnixNano()
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.usedNonces == nil {
		k.usedNonces = make(map[string]int64)
	}
	if _, used := k.usedNonces[key]; used {
		return false
	}
	k.usedNonces[key] = now
	k.nonceOrder = append(k.nonceOrder, key)
	for len(k.nonceOrder) > keyringNonceMaxEntries {
		oldest := k.nonceOrder[0]
		k.nonceOrder = k.nonceOrder[1:]
		delete(k.usedNonces, oldest)
	}
	return true
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

// keyringCommands 是 KeyringData 的规范命令目录。Command 的 switch 与
// ListCommands 消费同一批命令常量, 两个清单不会各自漂移。
var keyringCommands = []string{
	KeyringCommandStatus,
	KeyringCommandSetSecret,
	KeyringCommandRotate,
	KeyringCommandListTokens,
	KeyringCommandIssueToken,
	KeyringCommandRevokeToken,
	KeyringCommandRevokeAll,
}

// ListCommands 实现 types.CommandLister。
func (k *KeyringData) ListCommands() []string { return append([]string(nil), keyringCommands...) }

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
			// rand.Read 运行期故障(熵源不可用)是操作失败而非参数校验
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %w", act, types.ErrOperationFailed, err)}
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
		tok, err := k.IssueScopedToken(subject, ttl, typed.Scope, typed.Nonce)
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
		revoked, _ := k.RevokeToken(strings.TrimSpace(typed.Subject))
		if !revoked {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no active token for subject: %w", act, types.ErrInvalidArguments)}
		}
		// 与 IssueToken 命令同纪律: RevokeToken 返回共享指针——锁外解引用
		// Revoked/时间字段依赖 write-once 不变量(脆弱易回归), 经快照拷贝返回。
		snap, snapOK := k.TokenSnapshot(strings.TrimSpace(typed.Subject))
		if !snapOK {
			// RevokeToken 成功但快照缺失只有一种可能: 并发 RevokeAll 清表——
			// 仍返回吊销成功(已无令牌), 不暴露内部态。
			return types.CommandOutput{Name: act, Value: typed.Subject}
		}
		return types.CommandOutput{Name: act, Value: snap}
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
	for i := range snap.Tokens {
		tok := snap.Tokens[i]
		if strings.TrimSpace(tok.Subject) == "" {
			return fmt.Errorf("keyring snapshot: tokens[%d] has empty subject", i)
		}
		if !utf8.ValidString(tok.Subject) {
			return fmt.Errorf("keyring snapshot: tokens[%d] subject is not valid UTF-8", i)
		}
		// 时间窗校验: 签发时间非零、过期晚于签发、且 TTL 窗不超 maxTokenTTL。
		// 旧实现锚定加载时刻墙上时钟(now.Add(maxTokenTTL)): RTC 回拨(边缘
		// 设备时钟复位/NTP 向后纠正)后, 自身 SaveSnapshot 产出的合法快照
		// (如 30 天 TTL 的新令牌)会因"过期时间晚于 now+30d"被整体拒载——
		// 含 secret, 节点拒启。TTL 窗与时钟无关, 同样能挡伪永久令牌。
		if tok.IssuedAt.IsZero() || !tok.ExpiresAt.After(tok.IssuedAt) {
			return fmt.Errorf("keyring snapshot: tokens[%d] has invalid time window", i)
		}
		if tok.ExpiresAt.Sub(tok.IssuedAt) > maxTokenTTL {
			return fmt.Errorf("keyring snapshot: tokens[%d] ttl exceeds max %s", i, maxTokenTTL)
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
	// 防重放表不落盘: 重启后一次性令牌的已消费状态丢失(进程级防重放)。
	// 清空而非保留, 避免旧进程的键与重启后重新签发的同 subject 令牌混淆。
	k.usedNonces = make(map[string]int64)
	k.nonceOrder = nil
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

// tokenPayloadV2 是带作用域的签名载荷表示。字段名固定, 序列化为 JSON 后
// 进入 HMAC; 因此任何字段(含 Scope 的每条规则)被篡改都会使校验失败。
// Version 字段即"新格式"标记: 载荷里没有它(纯文本)就是旧格式令牌。
type tokenPayloadV2 struct {
	Version   string             `json:"v"`
	Subject   string             `json:"sub"`
	IssuedAt  int64              `json:"iat"`
	ExpiresAt int64              `json:"exp"`
	Scope     []KeyringScopeRule `json:"scope,omitempty"`
	Nonce     string             `json:"nonce,omitempty"`
}

// tokenPayload 构造签名载荷。
//
// v2(默认): 新签发一律使用, 即使调用方未声明作用域——载荷显式携带
// Scope:null, 语义为"不限制", 且与"旧格式 v1 令牌"可区分(校验时能判断
// 对方是旧令牌, 从而按旧语义放行而不是误判为伪造)。
// v1(仅兼容): 无 Version 字段的纯文本 "subject|issued|expires", 与旧实现
// 逐字节一致, 只用于复算/校验历史令牌。
//
// legacyV1=true 时才产出 v1 文本; 签发路径永远传 false。
func tokenPayload(subject string, issuedAt, expiresAt time.Time, scope []KeyringScopeRule, nonce string, legacyV1 bool) []byte {
	if legacyV1 {
		return []byte(fmt.Sprintf("%s|%d|%d", subject, issuedAt.UnixNano(), expiresAt.UnixNano()))
	}
	body, err := json.Marshal(tokenPayloadV2{
		Version:   keyringPayloadV2,
		Subject:   subject,
		IssuedAt:  issuedAt.UnixNano(),
		ExpiresAt: expiresAt.UnixNano(),
		Scope:     scope,
		Nonce:     strings.TrimSpace(nonce),
	})
	if err != nil {
		// json.Marshal 对本结构不可能失败(全为字符串/整数/字符串切片);
		// 防御性退化, 绝不让签发路径 panic。
		return []byte(fmt.Sprintf("%s|%d|%d", subject, issuedAt.UnixNano(), expiresAt.UnixNano()))
	}
	return body
}

// Sign 计算令牌的 HMAC 签名,返回 base64 字符串。新签发一律使用 v2 载荷
// (含作用域与 nonce 字段)。该函数供 OneKeyAbility 使用。
func (k *KeyringData) Sign(tok *KeyringToken) string {
	if tok == nil {
		return ""
	}
	secret := k.Secret()
	if len(secret) == 0 {
		// 与 Verify 同纪律 fail-closed: 空密钥下签名是任何人可复算的确定性值。
		return ""
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(tokenPayload(tok.Subject, tok.IssuedAt, tok.ExpiresAt, tok.Scope, tok.Nonce, false))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// SignLegacy 用旧格式(v1)载荷签名: "subject|issued|expires" 纯文本。
// 仅为兼容/迁移工具与回归测试保留——新签发必须使用 Sign(载荷携带作用域)。
func (k *KeyringData) SignLegacy(tok *KeyringToken) string {
	if tok == nil {
		return ""
	}
	secret := k.Secret()
	if len(secret) == 0 {
		return ""
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(tokenPayload(tok.Subject, tok.IssuedAt, tok.ExpiresAt, nil, "", true))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// VerifyToken 校验令牌是否与 signature 匹配: 载荷含作用域与 nonce, 篡改任一
// 字段都会使 HMAC 不匹配。Scope/Nonce 取调用方提供的值, 因此调用方必须保证
// 它们来自可信的签发输出; 对远端输入应经 OneKeyAbility 的 verify_token 与
// TokenSnapshot 的签发记录交叉核对后才采信(见 OneKeyAbility.Command 的
// verify_token 分支)。
func (k *KeyringData) VerifyToken(tok *KeyringToken, signature string) bool {
	if tok == nil {
		return false
	}
	return k.verifyPayload(tok.Subject, tok.IssuedAt, tok.ExpiresAt, tok.Scope, tok.Nonce, false, signature)
}

// Verify 校验四元组 (subject, issuedAt, expiresAt, signature) 是否与当前密钥
// 一致。为保证向后兼容, 它同时接受两种签名载荷:
//   - v2(当前签发格式): 作用域为空、nonce 为空的那份载荷;
//   - v1(旧格式令牌): 纯文本 "subject|issued|expires", 与旧实现逐字节一致。
//
// 因此旧客户端持有的历史令牌无需重新签发即可继续校验, 而 Sign() 的新输出也
// 能被同一入口验证。需要校验带作用域/nonce 的令牌时用 VerifyToken(那份令牌
// 的作用域必须显式提供, 不能凭空按"空作用域"复算)。
func (k *KeyringData) Verify(subject string, issuedAt, expiresAt time.Time, signature string) bool {
	if k.verifyPayload(subject, issuedAt, expiresAt, nil, "", false, signature) {
		return true
	}
	return k.verifyPayload(subject, issuedAt, expiresAt, nil, "", true, signature)
}

// verifyPayload 是签名校验的唯一实现。legacyV1 选择载荷版本; signature 为
// 空直接拒绝(空签名不是合法输入)。
func (k *KeyringData) verifyPayload(subject string, issuedAt, expiresAt time.Time, scope []KeyringScopeRule, nonce string, legacyV1 bool, signature string) bool {
	secret := k.Secret()
	if len(secret) == 0 {
		// 密钥未就绪(未挂载): 空密钥 HMAC 是确定性可计算的, 任意签名都可被伪造
		// 匹配——必须拒绝(fail-closed), 否则未 PreRun 即对外提供认证服务的
		// 进程, 其认证边界形同虚设(伪造任意 subject 令牌)。
		return false
	}
	if signature == "" {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(tokenPayload(subject, issuedAt, expiresAt, scope, nonce, legacyV1))
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
