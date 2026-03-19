package ability

import (
	"fmt"

	"github.com/FasterEdge/FasterEdge/types"
)

// BaseAbilityArgs defines accepted arguments for BaseAbility commands.
type BaseAbilityArgs struct {
	ListArgs []string
}

// BaseAbilityOutput represents the command result.
type BaseAbilityOutput struct {
	Success bool
	Error   string
}

type BaseAbility struct {
}

func (a *BaseAbility) GetName() string {
	return "BaseAbility"
}

func (b *BaseAbility) Describe() string {
	return "BaseAbility是一个基础能力，提供一些基本功能。"
}

func (b *BaseAbility) Check(atmo types.Atom) bool {
	// 检查BaseData是否已经被挂载
	if _, ok := atmo.GetAllData()["BaseData"]; !ok {
		return false
	}
	return true
}

func (b *BaseAbility) Mount(atmo types.Atom) bool {
	b.Check(atmo)
	atmo.AddAbility(b)
	return true
}

// Command implements typed handler for BaseAbility.
func (b *BaseAbility) Command(atmo types.Atom, act string, args BaseAbilityArgs) types.AbilityOutput[BaseAbilityOutput] {
	switch act {
	case "list_data_name":
		fmt.Printf("[%s] 正在执行 list_data_name\n", b.GetName())
		for key := range atmo.GetAllData() { // print map keys
			println(key)
		}
		return types.AbilityOutput[BaseAbilityOutput]{Name: act, Success: true}

	case "list_ability_name":
		fmt.Printf("[%s] 正在执行 list_ability_name\n", b.GetName())
		for key := range atmo.GetAllAbility() { // print map keys
			println(key)
		}
		return types.AbilityOutput[BaseAbilityOutput]{Name: act, Success: true}

	case "blocking":
		fmt.Printf("[%s] 正在执行 blocking\n", b.GetName())
		// 这个指令会永久阻塞，以防止其他携程运行被打断
		select {}
	}

	return types.AbilityOutput[BaseAbilityOutput]{Name: act, Success: false, Error: "unsupported act"}
}

// CommandAny adapts untyped args to the typed Command.
func (b *BaseAbility) CommandAny(atmo types.Atom, act string, args any) types.AbilityOutput[any] {
	typed, _ := args.(BaseAbilityArgs)
	out := b.Command(atmo, act, typed)
	return types.AbilityOutput[any]{Name: out.Name, Value: out.Value, Success: out.Success, Error: out.Error}
}
