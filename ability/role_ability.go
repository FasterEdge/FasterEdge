// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package ability

import (
	"fmt"
	"github.com/FasterEdge/FasterEdge/types"
	"strings"
	"sync"
)

// RoleAbilityArgs 定义角色相关命令的入参。
type RoleAbilityArgs struct {
	Role string
}

const (
	CommandDescribe = "describe"
	CommandSetRole  = "set_role"
	CommandGetRole  = "get_role"

	// 合法角色集合: cloud/edge 权限判定(CloudRoleAbility/EdgeRoleAbility 的
	// Check)直接比较该字符串, 因此 set_role 只接受这两个值, 拒绝任意注入。
	RoleCloud = "cloud"
	RoleEdge  = "edge"
)

// validRoles 是 set_role 允许的角色白名单。
var validRoles = map[string]bool{RoleCloud: true, RoleEdge: true}

type RoleAbility struct {
	mu   sync.RWMutex
	role string
}

func (r *RoleAbility) GetName() string { return "RoleAbility" }

func (r *RoleAbility) Describe() string { return "提供角色管理的能力。" }

func (r *RoleAbility) Check(atmo *types.Atom) error {
	if atmo == nil {
		return types.ErrMissingDependency
	}
	if _, ok := atmo.Data("BaseData"); !ok {
		return types.ErrMissingDependency
	}
	return nil
}

func (r *RoleAbility) Mount(atmo *types.Atom) error { return r.Check(atmo) }

func (r *RoleAbility) Command(atmo *types.Atom, act string, args any) types.CommandOutput {
	switch act {
	case CommandDescribe:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: r.Describe()}
	case CommandSetRole:
		typed, ok := args.(RoleAbilityArgs)
		if !ok || strings.TrimSpace(typed.Role) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		role := strings.TrimSpace(typed.Role)
		// 白名单: Cloud/Edge 权限判定仅比较本字符串, 任意值注入会绕过角色门禁
		// (旧实现接受任意字符串)。注意: 角色门禁的信任模型是"已认证对端可信",
		// 持有有效 OneKey 令牌的对端本就可设任意合法角色——这是软控制,
		// 硬授权需在 AuthenticateCommand 层按 component/command 扩展。
		if !validRoles[role] {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: role must be %q or %q: %w", act, RoleCloud, RoleEdge, types.ErrInvalidArguments)}
		}
		r.mu.Lock()
		r.role = role
		r.mu.Unlock()
		return types.CommandOutput{Name: act, Value: "角色设置成功"}
	case CommandGetRole:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		r.mu.RLock()
		role := r.role
		r.mu.RUnlock()
		return types.CommandOutput{Name: act, Value: role}
	}
	return types.CommandOutput{Name: act, Err: fmt.Errorf("command %s: %w", act, types.ErrUnsupportedCommand)}
}
