package ability

import (
	"encoding/base64"
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
)

// OneKeyToken 是 issue_token 命令返回的已签名令牌。
type OneKeyToken struct {
	Subject   string
	IssuedAt  time.Time
	ExpiresAt time.Time
	Signature string // base64(HMAC-SHA256)
}

// OneKeyIssueTokenArgs 是 issue_token 命令的参数。
type OneKeyIssueTokenArgs struct {
	Subject   string
	TTL       time.Duration // 0 表示使用 KeyringData 默认值
	Algorithm string        // 可选,留空使用默认
}

// OneKeyVerifyTokenArgs 是 verify_token 命令的参数。
type OneKeyVerifyTokenArgs struct {
	Subject   string
	IssuedAt  time.Time
	ExpiresAt time.Time
	Signature string
}

// OneKeyRevokeTokenArgs 是 revoke_token 命令的参数。
type OneKeyRevokeTokenArgs struct {
	Subject string
}

// OneKeyAbility 在 KeyringData 之上提供"一键加密访问"语义:
// 它为对等节点签发短期令牌,持有者可在远端通过 verify_token 证明身份。
type OneKeyAbility struct {
	mu sync.RWMutex
}

func NewOneKeyAbility() *OneKeyAbility { return &OneKeyAbility{} }

func (o *OneKeyAbility) GetName() string { return "OneKeyAbility" }
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
		// 默认 TTL 复用 KeyringData 的逻辑(传入 0 即可)
		tok, err := keyring.IssueToken(subject, typed.TTL)
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, err)}
		}
		return types.CommandOutput{Name: act, Value: OneKeyToken{
			Subject:   tok.Subject,
			IssuedAt:  tok.IssuedAt,
			ExpiresAt: tok.ExpiresAt,
			Signature: keyring.Sign(tok),
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
		if typed.ExpiresAt.IsZero() || typed.ExpiresAt.Before(time.Now()) {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: token expired: %w", act, types.ErrInvalidArguments)}
		}
		stored, ok := keyring.LookupToken(subject)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: unknown subject: %w", act, types.ErrInvalidArguments)}
		}
		if stored.Revoked {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: token revoked: %w", act, types.ErrInvalidArguments)}
		}
		if !keyring.Verify(subject, typed.IssuedAt, typed.ExpiresAt, typed.Signature) {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: bad signature: %w", act, types.ErrInvalidArguments)}
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

// EncodeForTransmission 把 OneKeyToken 打包为 "subject.issuedNanos.expiresNanos.signature" 形式,方便跨节点传输。
// 该函数为静态工具,不属于 Component 接口的一部分。
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
func DecodeFromTransmission(s string) (OneKeyToken, error) {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return OneKeyToken{}, fmt.Errorf("malformed token: %w", types.ErrInvalidArguments)
	}
	issued, err := parseUnixNanos(parts[1])
	if err != nil {
		return OneKeyToken{}, err
	}
	expires, err := parseUnixNanos(parts[2])
	if err != nil {
		return OneKeyToken{}, err
	}
	if _, err := base64.RawURLEncoding.DecodeString(parts[3]); err != nil {
		return OneKeyToken{}, fmt.Errorf("bad signature encoding: %w", types.ErrInvalidArguments)
	}
	return OneKeyToken{
		Subject:   parts[0],
		IssuedAt:  issued,
		ExpiresAt: expires,
		Signature: parts[3],
	}, nil
}

func parseUnixNanos(s string) (time.Time, error) {
	var n int64
	for _, r := range s {
		if r < '0' || r > '9' {
			return time.Time{}, fmt.Errorf("bad timestamp: %w", types.ErrInvalidArguments)
		}
		n = n*10 + int64(r-'0')
	}
	return time.Unix(0, n), nil
}
