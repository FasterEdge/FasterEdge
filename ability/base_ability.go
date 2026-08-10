package ability

import (
	"fmt"

	"github.com/FasterEdge/FasterEdge/types"
)

// 基础能力的入参定义
type BaseAbilityArgs struct {
	ListArgs []string // 用于存储参数列表
}

// 基础能力的出参定义（可以封存在AbilityOutput中的Data）...

// BaseAbility 相关参数
type BaseAbility struct {
}

// 能力名称
func (a *BaseAbility) GetName() string {
	return "BaseAbility"
}

// 能力描述
func (b *BaseAbility) Describe() string {
	return "BaseAbility是一个基础能力，提供一些基本功能。"
}

// 挂载前检查定义
func (b *BaseAbility) Check(atom *types.Atom) error {
	// 检查 BaseData 是否已经被挂载
	if _, ok := atom.Data("BaseData"); !ok {
		return types.ErrMissingDependency
	}
	return nil
}

// 挂载定义
func (b *BaseAbility) Mount(atom *types.Atom) error {
	if err := b.Check(atom); err != nil {
		return err
	}
	return nil
}

// 指令入口
func (b *BaseAbility) Command(atom *types.Atom, act string, args any) types.CommandOutput {
	typed, _ := args.(BaseAbilityArgs)
	switch act {
	case "list_data_name":
		fmt.Printf("[%s] 正在执行 list_data_name\n", b.GetName())
		for key := range atom.GetAllData() { // print map keys
			println(key)
		}
		return types.CommandOutput{Name: act}

	case "list_ability_name":
		fmt.Printf("[%s] 正在执行 list_ability_name\n", b.GetName())
		for key := range atom.GetAllAbility() { // print map keys
			println(key)
		}
		return types.CommandOutput{Name: act}

	case "blocking":
		fmt.Printf("[%s] 正在执行 blocking\n", b.GetName())
		// 这个指令会永久阻塞，以防止其他携程运行被打断
		select {}
	}

	_ = typed
	return types.CommandOutput{Name: act, Err: fmt.Errorf("command %s: %w", act, types.ErrUnsupportedCommand)}
}

// 细节方法实现...
