// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package data

import (
	"fmt"

	"github.com/FasterEdge/FasterEdge/types"
)

// Compile-time interface assertions. 每个 data 组件都通过 ListCommands
// 自描述其命令目录, 供 Atom.CommandNames 汇总。
var (
	_ types.CommandLister = (*BaseData)(nil)
	_ types.CommandLister = (*ConfigData)(nil)
	_ types.CommandLister = (*NetMapData)(nil)
	_ types.CommandLister = (*KeyringData)(nil)
)

// 所用公共参数定义
var logo = `
 _______ _______ _______ _______ _______  ______ _______ ______   ______ _______
 |______ |_____| |______    |    |______ |_____/ |______ |     \ |  ____ |______
 |       |     | ______|    |    |______ |    \_ |______ |_____/ |_____| |______
`
var version = "1.0.20261124"

const (
	CommandLogo = "logo"
	CommandInfo = "info"
)

// BaseData 定义
type BaseData struct{}

// 获取名称
func (b *BaseData) GetName() string {
	return "BaseData"
}

// 获取描述
func (b *BaseData) Describe() string {
	return "BaseData存储一些基本数据，可以在其中存储各种基本信息。"
}

// 挂载检查
func (b *BaseData) Check(atmo *types.Atom) error {
	// 最最基础的一个属性，不检查任何东西，直接返回true
	return nil
}

// 挂载 Data
func (b *BaseData) Mount(atmo *types.Atom) error {
	if err := b.Check(atmo); err != nil {
		return err
	}
	return nil
}

// baseDataCommands 是 BaseData 的规范命令目录。Command 的 switch 与
// ListCommands 消费同一批命令常量, 两个清单不会各自漂移。
var baseDataCommands = []string{CommandLogo, CommandInfo}

// ListCommands 实现 types.CommandLister。
func (b *BaseData) ListCommands() []string { return append([]string(nil), baseDataCommands...) }

// 指令入口
func (b *BaseData) Command(atmo *types.Atom, act string, args any) types.CommandOutput {
	_ = atmo
	if args != nil {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
	}
	switch act {
	case CommandLogo:
		return types.CommandOutput{Name: act, Value: logo}
	case CommandInfo:
		return types.CommandOutput{Name: act, Value: "FasterEdge v" + version + " - 对称、可靠、安全的多场景边缘计算框架"}
	}

	return types.CommandOutput{Name: act, Err: fmt.Errorf("command %s: %w", act, types.ErrUnsupportedCommand)}
}
