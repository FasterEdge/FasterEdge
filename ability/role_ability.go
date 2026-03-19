package ability

import "github.com/FasterEdge/FasterEdge/types"

// RoleAbilityArgs defines inputs for role commands.
type RoleAbilityArgs struct {
	Role string
}

// RoleAbilityOutput represents role command outcomes.
type RoleAbilityOutput struct {
	Message string
	Success bool
	Error   string
}

type RoleAbility struct{}

func (r *RoleAbility) GetName() string { return "RoleAbility" }

func (r *RoleAbility) Describe() string { return "提供角色管理的能力。" }

func (r *RoleAbility) Check(atmo types.Atom) bool {
	// 依赖 BaseData 存在
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

func (r *RoleAbility) Command(atmo types.Atom, act string, args RoleAbilityArgs) types.AbilityOutput[RoleAbilityOutput] {
	_ = atmo
	_ = args
	switch act {
	case "describe":
		return types.AbilityOutput[RoleAbilityOutput]{Name: act, Success: true, Value: RoleAbilityOutput{Message: r.Describe(), Success: true}}
	}
	return types.AbilityOutput[RoleAbilityOutput]{Name: act, Success: false, Error: "unsupported act"}
}

func (r *RoleAbility) CommandAny(atmo types.Atom, act string, args any) types.AbilityOutput[any] {
	typed, _ := args.(RoleAbilityArgs)
	out := r.Command(atmo, act, typed)
	return types.AbilityOutput[any]{Name: out.Name, Value: out.Value, Success: out.Success, Error: out.Error}
}
