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
)

type RoleAbility struct {
	mu   sync.RWMutex
	role string
}

func (r *RoleAbility) GetName() string { return "RoleAbility" }

func (r *RoleAbility) Describe() string { return "提供角色管理的能力。" }

func (r *RoleAbility) Check(atmo *types.Atom) error {
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
		r.mu.Lock()
		r.role = typed.Role
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
