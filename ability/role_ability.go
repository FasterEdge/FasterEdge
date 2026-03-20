package ability

import "github.com/FasterEdge/FasterEdge/types"

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

type RoleAbility struct{}

func (r *RoleAbility) GetName() string { return "RoleAbility" }

func (r *RoleAbility) Describe() string { return "提供角色管理的能力。" }

func (r *RoleAbility) Check(atmo types.Atom) bool {
	_, ok := atmo.GetAllData()["BaseData"]
	return ok
}

func (r *RoleAbility) Mount(atmo types.Atom) bool {
	if !r.Check(atmo) {
		return false
	}
	atmo.AddAbility(r)
	return true
}

func (r *RoleAbility) Command(atmo types.Atom, act string, args any) types.AbilityOutput {
	typed, _ := args.(RoleAbilityArgs)
	switch act {
	case "describe":
		return types.AbilityOutput{Name: act, Success: true, Value: RoleAbilityOutput{Message: r.Describe(), Success: true}}
	}
	_ = atmo
	_ = typed
	return types.AbilityOutput{Name: act, Success: false, Error: "unsupported act"}
}
