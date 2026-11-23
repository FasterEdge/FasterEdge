// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package ability

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/FasterEdge/FasterEdge/data"
	"github.com/FasterEdge/FasterEdge/types"
)

const (
	OneKeyCommandIssueToken  = "issue_token"
	OneKeyCommandVerifyToken = "verify_token"
	OneKeyCommandRevokeToken = "revoke_token"
	OneKeyCommandRevokeAll   = "revoke_all"
	OneKeyCommandListTokens  = "list_tokens"
	OneKeyCommandStatus      = "status"
	OneKeyCommandRotate      = "rotate"

	// oneKeyAbilityName 是组件注册名; 也是认证拦截清单里的自身 key。
	oneKeyAbilityName = "OneKeyAbility"
)

// OneKeyToken 是 issue_token 命令返回的已签名令牌。
type OneKeyToken struct {
	Subject   string
	IssuedAt  time.Time
	ExpiresAt time.Time
	Signature string // base64(HMAC-SHA256)
	// Scope 是令牌绑定的能力作用域; 为 nil/空表示不限制(旧格式语义)。
	// 字段由签发方设定并经签名保护, 远端篡改会导致校验失败。
	Scope []data.KeyringScopeRule
	// Nonce 非空表示该令牌为一次性(每次鉴权消耗, 重放即被拒)。
	Nonce string
}

// OneKeyIssueTokenArgs 是 issue_token 命令的参数。
type OneKeyIssueTokenArgs struct {
	Subject   string
	TTL       time.Duration // 0 表示使用 KeyringData 默认值
	Algorithm string        // 可选,留空使用默认
	// Scope 绑定令牌的能力作用域: component + command 白名单(可选 args 指纹)。
	// 为 nil/空表示不限制(旧格式: 一张令牌 = 整个 Atom 写权限)。
	// 新签发默认应显式声明作用域; 声明后 AuthenticateCommand 会拒绝作用域
	// 外的 component/command, 且该拒绝先于组件查找(不泄露组件存在性)。
	Scope []data.KeyringScopeRule
	// OneShot 为 true 时自动生成一次性 nonce, 使该令牌只能使用一次。
	OneShot bool
}

// OneKeyVerifyTokenArgs 是 verify_token 命令的参数。
type OneKeyVerifyTokenArgs struct {
	Subject   string
	IssuedAt  time.Time
	ExpiresAt time.Time
	Signature string
	// Scope 必须与签发时一致(进入签名载荷); 旧格式令牌留空即可。
	Scope []data.KeyringScopeRule
	// Nonce 必须与签发时一致; 旧格式令牌留空即可。
	Nonce string
}

// OneKeyCredential is the credential accepted by Atom's authenticated command
// entry point. It is intentionally identical to the verify_token arguments.
type OneKeyCredential = OneKeyVerifyTokenArgs

// OneKeyRevokeTokenArgs 是 revoke_token 命令的参数。
type OneKeyRevokeTokenArgs struct {
	Subject string
}

// OneKeyAbility 在 KeyringData 之上提供"一键加密访问"语义:
// 它为对等节点签发短期令牌,持有者可在远端通过 verify_token 证明身份。
// 令牌可绑定能力作用域(component + command 白名单), 使"身份"与"授权"
// 分离: 一张令牌只被允许调用其作用域内的命令。
type OneKeyAbility struct {
	mu sync.RWMutex
	// authFailures 记录每个 subject 的连续认证失败次数与窗口起点,
	// 用于认证限速(见 allowAuthAttempt/recordAuthFailure)。
	authFailures map[string]authFailureState
}

// authFailureState 是单个 subject 的认证失败计数状态。
type authFailureState struct {
	count     int
	windowEnd time.Time
}

const (
	// oneKeyAuthMaxFailures 是窗口内允许的最大认证失败次数。
	oneKeyAuthMaxFailures = 10
	// oneKeyAuthFailureWindow 是失败计数的滑动窗口长度。
	oneKeyAuthFailureWindow = time.Minute
	// oneKeyAuthMaxSubjects 限制失败计数表的 subject 条目上限
	// (攻击者可自选 subject, 无上限时表会随伪造 subject 无限增长)。
	oneKeyAuthMaxSubjects = 1024
)

func NewOneKeyAbility() *OneKeyAbility {
	return &OneKeyAbility{authFailures: make(map[string]authFailureState)}
}

func (o *OneKeyAbility) GetName() string { return oneKeyAbilityName }
func (o *OneKeyAbility) Dependencies() []types.Dependency {
	return []types.Dependency{{Kind: types.DependencyData, Name: "BaseData"}, {Kind: types.DependencyData, Name: "NetMapData"}, {Kind: types.DependencyData, Name: "KeyringData"}}
}

func (o *OneKeyAbility) Describe() string {
	return "OneKeyAbility提供节点加密访问(One-Key)能力:为对等节点签发短期 HMAC 令牌并支持校验/吊销,依赖 KeyringData 共享密钥。"
}

func (o *OneKeyAbility) Check(atom *types.Atom) error {
	if atom == nil {
		return types.ErrMissingDependency
	}
	if _, ok := atom.Data("BaseData"); !ok {
		return types.ErrMissingDependency
	}
	if _, ok := atom.Data("NetMapData"); !ok {
		return types.ErrMissingDependency
	}
	if _, ok := atom.Data("KeyringData"); !ok {
		return types.ErrMissingDependency
	}
	return nil
}

func (o *OneKeyAbility) Mount(atom *types.Atom) error { return o.Check(atom) }

// AuthenticateCommand implements types.CommandAuthenticator.
//
// 认证分两步: 先验签(身份), 再验授权(令牌绑定的 component/command 白名单)。
// 作用域校验位于组件查找之前(见 types.AuthenticatedCommandContext 的调用顺序),
// 因此作用域外的调用不会泄露目标组件是否存在。
//
// 旧格式令牌(无作用域、无 nonce)仍可校验并通过鉴权, 但保留"一张令牌 =
// 整个 Atom 写权限"的语义——这是有意的向后兼容, 不引入新的默认行为;
// 新签发的令牌应显式声明 Scope。
//
// 管理命令(issue_token/revoke_token/revoke_all/rotate)仅限本地进程内调用:
// 任何持有效令牌的对端若可远程调用, 即可签发任意 subject 的令牌冒充任意
// 节点, 或做吊销/轮换 DoS。verify_token 必须远程可达(认证依赖它)。
//
// 信任边界: 拦截清单(下方 122-155 行)覆盖 OneKey 管理命令、RoleAbility
// set_role、KeyringData 写命令、Cmd/Sh/Bash set_allowlist——其余写面
// (ConfigFileAbility 的 load/save/set_path、Docker/K8s、file_transfer 等)
// 仍可被任意已认证对端远程调用, 属防御纵深的设计取舍。新增高危写命令时
// 应同步评估是否加入本地限制清单。
func (o *OneKeyAbility) AuthenticateCommand(ctx context.Context, atom *types.Atom, credential any, component, command string, args any) (string, error) {
	if ctx == nil {
		return "", types.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	token, ok := credential.(OneKeyVerifyTokenArgs)
	if !ok {
		return "", types.ErrAuthenticationFailed
	}
	scope, err := data.NormalizeKeyringScope(token.Scope)
	if err != nil {
		return "", types.ErrAuthenticationFailed
	}
	// 本地限制清单必须先于作用域判定执行: 这些命令无论令牌作用域如何都
	// 不允许远程调用(否则持 "全作用域" 令牌的对端即可远程 set_secret 伪造
	// 任意身份, 属权限授予面的兜底)。
	if local, localCmd := restrictedRemoteCommand(component, command); local {
		return "", fmt.Errorf("%w: %s is restricted to local in-process calls", types.ErrAuthenticationFailed, localCmd)
	}
	// 认证限速: 同一 subject 的连续失败在窗口内累计, 超限后拒绝后续尝试验证
	// (认证 DoS 的兜底, 与令牌的 TTL/吊销解耦)。仅统计失败, 成功即清零。
	if !o.allowAuthAttempt(token.Subject) {
		return "", fmt.Errorf("%w: too many recent authentication failures for subject %q", types.ErrAuthenticationFailed, token.Subject)
	}
	out := o.Command(atom, OneKeyCommandVerifyToken, OneKeyVerifyTokenArgs{
		Subject:   token.Subject,
		IssuedAt:  token.IssuedAt,
		ExpiresAt: token.ExpiresAt,
		Signature: token.Signature,
		Scope:     token.Scope,
		Nonce:     token.Nonce,
	})
	if out.Err != nil {
		o.recordAuthFailure(token.Subject)
		return "", types.ErrAuthenticationFailed
	}
	subject, ok := out.Value.(string)
	if !ok || strings.TrimSpace(subject) == "" {
		o.recordAuthFailure(token.Subject)
		return "", types.ErrAuthenticationFailed
	}
	o.resetAuthFailures(subject)
	// 一次性令牌防重放: 非空 nonce 每次鉴权只放行一次, 重放即被拒。
	// 消费发生在签名校验之后, 因此伪造的 nonce 不会占用合法令牌的窗口。
	if !o.consumeNonce(atom, subject, token.Nonce) {
		return "", fmt.Errorf("%w: one-shot token already used", types.ErrAuthenticationFailed)
	}
	// 授权: 令牌绑定的作用域必须覆盖本次调用, 否则拒绝。空作用域 = 不限制
	// (旧格式语义)。哨兵为 ErrAuthorizationDenied(403)而非认证失败(401),
	// 让平台可区分"凭据无效"与"凭据有效但越权"。
	//
	// verify_token 恒放行: 它是认证协议自身依赖的命令(对端靠它换身份),
	// 若被作用域挡住则令牌永远无法自证; 该命令无写面, 不构成越权风险。
	if command == OneKeyCommandVerifyToken && component == oneKeyAbilityName {
		return subject, nil
	}
	if !scope.Covers(component, command, data.KeyringArgsFingerprint(args)) {
		return "", fmt.Errorf("%w: subject %q is not authorized for %s/%s", types.ErrAuthorizationDenied, subject, component, command)
	}
	return subject, nil
}

// restrictedRemoteCommand 返回该 (component, command) 是否仅限本地进程内调用。
// 与 AuthenticateCommand 旧实现的拦截清单一一对应, 提取为纯函数便于测试。
func restrictedRemoteCommand(component, command string) (bool, string) {
	switch component {
	case oneKeyAbilityName:
		switch command {
		case OneKeyCommandIssueToken, OneKeyCommandRevokeToken, OneKeyCommandRevokeAll, OneKeyCommandRotate:
			return true, command
		}
	case "RoleAbility":
		// set_role 同属部署态配置: 任意已认证对端可远程翻转 cloud/edge 角色
		// 开关(同一 atom 可同时注册两侧角色能力)——与 OneKey 管理命令一样
		// 仅限本地进程内调用。
		if command == "set_role" {
			return true, command
		}
	case "KeyringData":
		// KeyringData 的密钥/令牌管理命令同样仅限本地进程内调用(旧实现只拦了
		// OneKeyAbility 的四个命令, 兜底分支对其余 component/command 全放行):
		// 任何持有效令牌的对端可经 AuthenticatedCommand(cred, "KeyringData",
		// "set_secret", ...) 置换共享密钥为自选值, 再用所知密钥离线 Sign 出任意
		// subject 的伪造令牌(身份伪造); rotate/revoke_all 即全局认证 DoS。
		// list_tokens/status 为读面, 保持远程可达。
		switch command {
		case data.KeyringCommandSetSecret, data.KeyringCommandRotate,
			data.KeyringCommandIssueToken, data.KeyringCommandRevokeToken,
			data.KeyringCommandRevokeAll:
			return true, command
		}
	case "CmdAbility", "ShAbility", "BashAbility":
		// set_allowlist / set_allowlist_any 是能力授予面: 远程改写白名单后可经
		// run 执行任意命令(allowlist 以 Name 精确匹配 shell 条目即放行)——绕过
		// 命令执行的安全边界演化为远程 RCE。set_allowlist_any 更彻底: 它一步就把
		// 白名单换成"允许任意命令", 因此与 set_allowlist 同级受限。两者都仅限本地;
		// run 保持远程可达但受 allowlist 约束。
		if command == CmdCommandSetAllowlist || command == CmdCommandSetAllowAny {
			return true, command
		}
	}
	return false, ""
}

// consumeNonce 消费一次性令牌的 nonce; 非空 nonce 只放行一次。
func (o *OneKeyAbility) consumeNonce(atom *types.Atom, subject, nonce string) bool {
	if strings.TrimSpace(nonce) == "" {
		return true
	}
	if atom == nil {
		return false
	}
	d, ok := atom.Data("KeyringData")
	if !ok {
		return false
	}
	keyring, ok := d.(*data.KeyringData)
	if !ok {
		return false
	}
	return keyring.ConsumeNonce(subject, nonce)
}

// allowAuthAttempt 判断 subject 当前是否仍在认证尝试预算内。
// 空 subject 直接放行: 它必然在验签阶段失败, 无需为匿名输入维护计数表。
func (o *OneKeyAbility) allowAuthAttempt(subject string) bool {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return true
	}
	now := time.Now()
	o.mu.RLock()
	st, ok := o.authFailures[subject]
	o.mu.RUnlock()
	if !ok || now.After(st.windowEnd) {
		return true
	}
	return st.count < oneKeyAuthMaxFailures
}

// recordAuthFailure 累计一次失败; 窗口过期则重新开窗。
//
// 内存上界: 失败表按 subject 建键, 未认证调用方可自选 subject, 因此表大小
// 必须封顶。超限时整表清空(而非淘汰单条)——清空只会让部分 subject 的计数
// 归零(限速变宽松), 不会误放行任何凭据, 属 fail-open 的可用性选择。
func (o *OneKeyAbility) recordAuthFailure(subject string) {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return
	}
	now := time.Now()
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.authFailures == nil {
		o.authFailures = make(map[string]authFailureState)
	}
	if len(o.authFailures) >= oneKeyAuthMaxSubjects {
		if _, tracked := o.authFailures[subject]; !tracked {
			o.authFailures = make(map[string]authFailureState)
		}
	}
	st := o.authFailures[subject]
	if now.After(st.windowEnd) {
		st = authFailureState{windowEnd: now.Add(oneKeyAuthFailureWindow)}
	}
	st.count++
	o.authFailures[subject] = st
}

// resetAuthFailures 在认证成功后清空该 subject 的失败计数(避免限速误伤
// 正常对端; 也防止表随历史失败无限增长)。
func (o *OneKeyAbility) resetAuthFailures(subject string) {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return
	}
	o.mu.Lock()
	delete(o.authFailures, subject)
	o.mu.Unlock()
}

// keyringScopesEqual 比较两个已规范化作用域(两者都必须先经
// NormalizeKeyringScope, 否则切片顺序/去重差异会造成误判)。
func keyringScopesEqual(a, b data.KeyringScope) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Component != b[i].Component || a[i].ArgsFingerprint != b[i].ArgsFingerprint {
			return false
		}
		if len(a[i].Commands) != len(b[i].Commands) {
			return false
		}
		for j := range a[i].Commands {
			if a[i].Commands[j] != b[i].Commands[j] {
				return false
			}
		}
	}
	return true
}

func (o *OneKeyAbility) Command(atom *types.Atom, act string, args any) types.CommandOutput {
	if err := o.Check(atom); err != nil {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, err)}
	}
	kr, _ := atom.Data("KeyringData")
	keyring, _ := kr.(*data.KeyringData)
	if keyring == nil {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: KeyringData type mismatch: %w", act, types.ErrInvalidArguments)}
	}
	switch act {
	case OneKeyCommandIssueToken:
		typed, ok := args.(OneKeyIssueTokenArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		subject := strings.TrimSpace(typed.Subject)
		if subject == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		if typed.TTL < 0 {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: ttl must be positive: %w", act, types.ErrInvalidArguments)}
		}
		algo := strings.TrimSpace(typed.Algorithm)
		if algo != "" && !strings.EqualFold(algo, "HMAC-SHA256") {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: unsupported algorithm %q: %w", act, algo, types.ErrInvalidArguments)}
		}
		// 一次性令牌: nonce 由签发方自动生成(16 字节随机, 16 进制编码)。
		// nonce 进入签名载荷, 因此不可伪造、不可续期; 远端必须原样回传。
		nonce := ""
		if typed.OneShot {
			buf := make([]byte, 16)
			if _, err := rand.Read(buf); err != nil {
				return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %w", act, types.ErrOperationFailed, err)}
			}
			nonce = hex.EncodeToString(buf)
		}
		// 默认 TTL 复用 KeyringData 的逻辑(传入 0 即可)
		tok, err := keyring.IssueScopedToken(subject, typed.TTL, typed.Scope, nonce)
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, err)}
		}
		return types.CommandOutput{Name: act, Value: OneKeyToken{
			Subject:   tok.Subject,
			IssuedAt:  tok.IssuedAt,
			ExpiresAt: tok.ExpiresAt,
			Signature: keyring.Sign(tok),
			Scope:     tok.Scope,
			Nonce:     tok.Nonce,
		}}
	case OneKeyCommandVerifyToken:
		typed, ok := args.(OneKeyVerifyTokenArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		subject := strings.TrimSpace(typed.Subject)
		if subject == "" || strings.TrimSpace(typed.Signature) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		scope, err := data.NormalizeKeyringScope(typed.Scope)
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, err)}
		}
		now := time.Now()
		if typed.IssuedAt.IsZero() || typed.ExpiresAt.IsZero() || !typed.ExpiresAt.After(typed.IssuedAt) || typed.ExpiresAt.Before(now) || typed.IssuedAt.After(now.Add(time.Minute)) {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: invalid token lifetime: %w", act, types.ErrInvalidArguments)}
		}
		// TokenSnapshot 返回值拷贝: LookupToken 返回共享指针, 锁外读
		// Revoked/IssuedAt/ExpiresAt 会与 RevokeToken/RevokeAll 构成数据竞争。
		stored, ok := keyring.TokenSnapshot(subject)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: unknown subject: %w", act, types.ErrInvalidArguments)}
		}
		if stored.Revoked {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: token revoked: %w", act, types.ErrInvalidArguments)}
		}
		if !stored.IssuedAt.Equal(typed.IssuedAt) || !stored.ExpiresAt.Equal(typed.ExpiresAt) {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: token does not match issued credential: %w", act, types.ErrInvalidArguments)}
		}
		// 载荷含作用域与 nonce: 与签发时不一致(含篡改 Scope/Nonce 字段)即
		// 验签失败——令牌的授权面因此不可被远端改写。旧格式(v1)载荷的令牌
		// 在签发记录里同样没有 Scope/Nonce, 因此按"空作用域"复算即可通过,
		// 无需迁移。
		if !keyring.VerifyToken(&data.KeyringToken{
			Subject:   subject,
			IssuedAt:  typed.IssuedAt,
			ExpiresAt: typed.ExpiresAt,
			Scope:     scope,
			Nonce:     strings.TrimSpace(typed.Nonce),
		}, typed.Signature) {
			// 兼容旧格式: 签发记录无作用域且调用方未传 nonce 时, 尝试 v1 载荷。
			if len(stored.Scope) != 0 || strings.TrimSpace(typed.Nonce) != "" ||
				!keyring.Verify(subject, typed.IssuedAt, typed.ExpiresAt, typed.Signature) {
				return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: bad signature: %w", act, types.ErrInvalidArguments)}
			}
		}
		// 与签发记录交叉核对作用域/nonce, 防止"签名自洽但与签发记录不符"的
		// 载荷(签发方规范化后的作用域才是权威; 调用方传等价但未规范化的
		// 列表会被 NormalizeKeyringScope 归一, 故此处比较是安全的)。
		storedScope, scopeErr := data.NormalizeKeyringScope(stored.Scope)
		if scopeErr != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, scopeErr)}
		}
		if !keyringScopesEqual(storedScope, scope) {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: token scope does not match issued credential: %w", act, types.ErrInvalidArguments)}
		}
		if strings.TrimSpace(stored.Nonce) != strings.TrimSpace(typed.Nonce) {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: token nonce does not match issued credential: %w", act, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: subject}
	case OneKeyCommandRevokeToken:
		typed, ok := args.(OneKeyRevokeTokenArgs)
		if !ok || strings.TrimSpace(typed.Subject) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		ok, prev := keyring.RevokeToken(strings.TrimSpace(typed.Subject))
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no active token: %w", act, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: prev.Subject}
	case OneKeyCommandRevokeAll:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: keyring.RevokeAll()}
	case OneKeyCommandListTokens:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		_, tokens := keyring.Snapshot()
		return types.CommandOutput{Name: act, Value: tokens}
	case OneKeyCommandStatus:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		status, _ := keyring.Snapshot()
		return types.CommandOutput{Name: act, Value: status}
	case OneKeyCommandRotate:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		// 触发 KeyringData 自身的 rotate 命令
		out := keyring.Command(atom, data.KeyringCommandRotate, nil)
		if out.Err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, out.Err)}
		}
		return types.CommandOutput{Name: act, Value: out.Value}
	}
	return types.CommandOutput{Name: act, Err: fmt.Errorf("command %s: %w", act, types.ErrUnsupportedCommand)}
}

// oneKeyAbilityCommands 是 OneKeyAbility 的命令目录, 与 Command 中 act 的
// switch 分支一一对应(由 command_contract_test.go 解析源码交叉核对)。
var oneKeyAbilityCommands = []string{
	OneKeyCommandIssueToken,
	OneKeyCommandVerifyToken,
	OneKeyCommandRevokeToken,
	OneKeyCommandRevokeAll,
	OneKeyCommandListTokens,
	OneKeyCommandStatus,
	OneKeyCommandRotate,
}

// ListCommands 实现 types.CommandLister, 返回命令目录的副本。
// 目录只声明"命令存在", 不代表可被远程调用: 管理命令(issue/revoke/rotate)
// 在 AuthenticateCommand 中被限制为仅本地进程内调用。
func (o *OneKeyAbility) ListCommands() []string {
	return append([]string(nil), oneKeyAbilityCommands...)
}

// EncodeForTransmission 把 OneKeyToken 打包为 "subject.issuedNanos.expiresNanos.signature" 形式,方便跨节点传输。
// 该函数为静态工具,不属于 Component 接口的一部分。
//
// 注意: 该格式**不携带** Scope/Nonce——带作用域或一次性的令牌必须用
// EncodeScopedForTransmission 打包, 否则作用域在解码后丢失, 验签失败。
// 旧格式保留是为了与旧对端互通。
func EncodeForTransmission(t OneKeyToken) string {
	return strings.Join([]string{
		t.Subject,
		fmt.Sprintf("%d", t.IssuedAt.UnixNano()),
		fmt.Sprintf("%d", t.ExpiresAt.UnixNano()),
		t.Signature,
	}, ".")
}

// DecodeFromTransmission 解析 EncodeForTransmission 的输出。
// 返回 (OneKeyToken, error);签名解析失败时返回 types.ErrInvalidArguments 包裹的错误。
// 布局为 subject.issuedNanos.expiresNanos.signature; 从尾部解析时间戳与签名,
// 因此 subject 可安全包含 "." (如主机名 edge-1.local)。signature 为
// RawURL base64 (不含 "."), 时间戳为纯数字, 尾部三段无歧义。
func DecodeFromTransmission(s string) (OneKeyToken, error) {
	parts := strings.Split(s, ".")
	if len(parts) < 4 {
		return OneKeyToken{}, fmt.Errorf("malformed token: %w", types.ErrInvalidArguments)
	}
	issued, err := parseUnixNanos(parts[len(parts)-3])
	if err != nil {
		return OneKeyToken{}, err
	}
	expires, err := parseUnixNanos(parts[len(parts)-2])
	if err != nil {
		return OneKeyToken{}, err
	}
	signature := parts[len(parts)-1]
	// HMAC-SHA256 RawURL base64 为 43 字符; 无上限时超大 token 串会先被
	// 完整 base64 解码再比对(内存/CPU 消耗仅受传输层消息上限约束)。
	// 128 字符上界覆盖未来算法更换余量。
	if len(signature) > 128 {
		return OneKeyToken{}, fmt.Errorf("signature too long: %w", types.ErrInvalidArguments)
	}
	if _, err := base64.RawURLEncoding.DecodeString(signature); err != nil {
		return OneKeyToken{}, fmt.Errorf("bad signature encoding: %w", types.ErrInvalidArguments)
	}
	return OneKeyToken{
		Subject:   strings.Join(parts[:len(parts)-3], "."),
		IssuedAt:  issued,
		ExpiresAt: expires,
		Signature: signature,
	}, nil
}

// wireEnvelopeV2 是"带作用域令牌"的跨节点封装前缀。旧格式(裸 4 段)没有前缀,
// 因此两种格式在同一入口上不会互相歧义: 只有显式带前缀的串按 v2 解析。
const wireEnvelopeV2 = "fe2:"

// wirePayloadV2 是 v2 封装体: 令牌的全部可验证字段(含作用域与 nonce)。
// 它是**未签名**的传输编码, 字段本身的完整性由 Signature 保证——对端仍必须
// 走 verify_token, 篡改任何字段都会在验签阶段失败。
type wirePayloadV2 struct {
	Subject   string                  `json:"sub"`
	IssuedAt  int64                   `json:"iat"`
	ExpiresAt int64                   `json:"exp"`
	Signature string                  `json:"sig"`
	Scope     []data.KeyringScopeRule `json:"scope,omitempty"`
	Nonce     string                  `json:"nonce,omitempty"`
}

// EncodeScopedForTransmission 打包带作用域的令牌, 输出 "fe2:<base64url(JSON)>"。
// 令牌无作用域且无 nonce 时退化为旧格式 EncodeForTransmission 的输出
// (与旧对端完全互通)。
func EncodeScopedForTransmission(t OneKeyToken) string {
	if len(t.Scope) == 0 && strings.TrimSpace(t.Nonce) == "" {
		return EncodeForTransmission(t)
	}
	body, err := json.Marshal(wirePayloadV2{
		Subject:   t.Subject,
		IssuedAt:  t.IssuedAt.UnixNano(),
		ExpiresAt: t.ExpiresAt.UnixNano(),
		Signature: t.Signature,
		Scope:     t.Scope,
		Nonce:     t.Nonce,
	})
	if err != nil {
		// 结构全为字符串/整数, 序列化不会失败; 防御性退化为旧格式。
		return EncodeForTransmission(t)
	}
	return wireEnvelopeV2 + base64.RawURLEncoding.EncodeToString(body)
}

// DecodeScopedFromTransmission 解析 EncodeScopedForTransmission 的输出,
// 同时接受旧格式(裸 4 段): 两种格式由前缀区分, 旧令牌因此无需迁移。
func DecodeScopedFromTransmission(s string) (OneKeyToken, error) {
	if !strings.HasPrefix(s, wireEnvelopeV2) {
		return DecodeFromTransmission(s)
	}
	raw, err := base64.RawURLEncoding.DecodeString(s[len(wireEnvelopeV2):])
	if err != nil {
		return OneKeyToken{}, fmt.Errorf("bad envelope encoding: %w", types.ErrInvalidArguments)
	}
	// 无上限时超大封装体会被完整解码再解析; 128 KiB 上界远超任何合法令牌
	// (作用域 ≤32 条、名称 ≤128 字节), 仅用于挡住放大攻击。
	if len(raw) > 128<<10 {
		return OneKeyToken{}, fmt.Errorf("envelope too large: %w", types.ErrInvalidArguments)
	}
	var payload wirePayloadV2
	if err := json.Unmarshal(raw, &payload); err != nil {
		return OneKeyToken{}, fmt.Errorf("bad envelope body: %w", types.ErrInvalidArguments)
	}
	if strings.TrimSpace(payload.Subject) == "" || strings.TrimSpace(payload.Signature) == "" {
		return OneKeyToken{}, fmt.Errorf("envelope missing subject or signature: %w", types.ErrInvalidArguments)
	}
	if len(payload.Signature) > 128 {
		return OneKeyToken{}, fmt.Errorf("signature too long: %w", types.ErrInvalidArguments)
	}
	if _, err := base64.RawURLEncoding.DecodeString(payload.Signature); err != nil {
		return OneKeyToken{}, fmt.Errorf("bad signature encoding: %w", types.ErrInvalidArguments)
	}
	if payload.IssuedAt <= 0 || payload.ExpiresAt <= payload.IssuedAt {
		return OneKeyToken{}, fmt.Errorf("invalid token lifetime: %w", types.ErrInvalidArguments)
	}
	if len(payload.Nonce) > keyringNonceMaxLenValue {
		return OneKeyToken{}, fmt.Errorf("nonce too long: %w", types.ErrInvalidArguments)
	}
	return OneKeyToken{
		Subject:   payload.Subject,
		IssuedAt:  time.Unix(0, payload.IssuedAt),
		ExpiresAt: time.Unix(0, payload.ExpiresAt),
		Signature: payload.Signature,
		Scope:     payload.Scope,
		Nonce:     payload.Nonce,
	}, nil
}

// keyringNonceMaxLenValue 是 data 层 nonce 长度上限的镜像: 解码期先挡掉超长
// 值, 避免把明显非法的封装体交给后续验签路径。
const keyringNonceMaxLenValue = 64

func parseUnixNanos(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("empty timestamp: %w", types.ErrInvalidArguments)
	}
	var n int64
	for _, r := range s {
		if r < '0' || r > '9' {
			return time.Time{}, fmt.Errorf("bad timestamp: %w", types.ErrInvalidArguments)
		}
		d := int64(r - '0')
		// 溢出防护: 超长数字 (如 20 位 "999...9") 会使 n*10+d 回绕成
		// 负数并被接受为合法纳秒时间, 需显式拒绝。
		if n > (int64(^uint64(0)>>1)-d)/10 {
			return time.Time{}, fmt.Errorf("timestamp overflow: %w", types.ErrInvalidArguments)
		}
		n = n*10 + d
	}
	return time.Unix(0, n), nil
}
