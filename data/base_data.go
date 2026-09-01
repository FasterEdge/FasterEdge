package data

import (
	"fmt"

	"github.com/FasterEdge/FasterEdge/types"
)

// 所用公共参数定义
var logo = `
 _______ _______ _______ _______ _______  ______ _______ ______   ______ _______
 |______ |_____| |______    |    |______ |_____/ |______ |     \ |  ____ |______
 |       |     | ______|    |    |______ |    \_ |______ |_____/ |_____| |______
`
var version = "1.0.20260902"

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
