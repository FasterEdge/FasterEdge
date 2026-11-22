// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package ability

import (
	"fmt"
	"sort"

	"github.com/FasterEdge/FasterEdge/types"
)

const (
	CommandListDataNames    = "list_data_names"
	CommandListAbilityNames = "list_ability_names"
)

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
	if args != nil {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
	}
	if atom == nil {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
	}
	switch act {
	case CommandListDataNames:
		names := make([]string, 0, len(atom.AllData()))
		for key := range atom.AllData() {
			names = append(names, key)
		}
		sort.Strings(names)
		return types.CommandOutput{Name: act, Value: names}
	case CommandListAbilityNames:
		names := make([]string, 0, len(atom.AllAbilities()))
		for key := range atom.AllAbilities() {
			names = append(names, key)
		}
		sort.Strings(names)
		return types.CommandOutput{Name: act, Value: names}
	}
	return types.CommandOutput{Name: act, Err: fmt.Errorf("command %s: %w", act, types.ErrUnsupportedCommand)}
}

// baseAbilityCommands 是 BaseAbility 的命令目录, 与 Command 中 act 的
// switch 分支一一对应(由 command_contract_test.go 解析源码交叉核对,
// 新增分支忘了登记会直接测试失败)。
var baseAbilityCommands = []string{
	CommandListDataNames,
	CommandListAbilityNames,
}

// ListCommands 实现 types.CommandLister, 返回命令目录的副本(调用方持有所有权)。
func (b *BaseAbility) ListCommands() []string { return append([]string(nil), baseAbilityCommands...) }

// 细节方法实现...
