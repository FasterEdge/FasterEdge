package ability

import (
	"fmt"
	"github.com/FasterEdge/FasterEdge/types"
)

// RoleAbilityArgs 定义角色相关命令的入参。
type RoleAbilityArgs struct {
	Role string
}

// RoleAbilityOutput 描述角色命令的输出结果。
type RoleAbilityOutput struct {
	Message string
	Success bool
	Error   string
}

type RoleAbility struct {
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
	typed, _ := args.(RoleAbilityArgs)
	switch act {
	case "describe":
		return types.CommandOutput{Name: act, Value: RoleAbilityOutput{Message: r.Describe(), Success: true}}
	case "set_role":
		r.role = typed.Role
		return types.CommandOutput{Name: act, Value: RoleAbilityOutput{Message: "角色设置成功", Success: true}}
	case "get_role":
		return types.CommandOutput{Name: act, Value: RoleAbilityOutput{Message: r.role, Success: true}}
	}
	return types.CommandOutput{Name: act, Err: fmt.Errorf("command %s: %w", act, types.ErrUnsupportedCommand)}
}
