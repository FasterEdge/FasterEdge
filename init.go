package FasterEdge

import (
	"fmt"

	"github.com/FasterEdge/FasterEdge/ability"
	"github.com/FasterEdge/FasterEdge/data"
	"github.com/FasterEdge/FasterEdge/types"
)

func InitAtom() *types.Atom {
	atom := &types.Atom{}
	if err := atom.AddData(&data.BaseData{}); err != nil {
		panic(fmt.Sprintf("register BaseData: %v", err))
	}
	if err := atom.AddAbility(&ability.BaseAbility{}); err != nil {
		panic(fmt.Sprintf("register BaseAbility: %v", err))
	}
	return atom
}

// 只挂载数据和能力,用于给用户提供自定义开发的基础环境
func PreRunAtom(atom *types.Atom) error {
	return atom.PreRun()
}

// InitStandardAtom 注册一组常用的 Ability + Data,作为"完整骨架"示例:
//   - 身份:BaseData / BaseAbility
//   - 角色:RoleAbility
//   - 网络拓扑:NetMapData + NetMapAbility
//   - 时间:TimeAbility
//   - 加密访问:KeyringData + OneKeyAbility
//   - 终端命令:CmdAbility / ShAbility / BashAbility
//   - 文件与配置:ConfigData + ConfigFileAbility
//
// 使用方可在 InitStandardAtom 基础上继续注册 Docker / K8s / MQTT / InfluxDB 等能力。
func InitStandardAtom() *types.Atom {
	atom := InitAtom()

	regs := []struct {
		kind string
		name string
		fn   func() error
	}{
		{"data", "NetMapData", func() error { return atom.AddData(data.NewNetMapData()) }},
		{"data", "KeyringData", func() error { return atom.AddData(data.NewKeyringData()) }},
		{"data", "ConfigData", func() error { return atom.AddData(data.NewConfigData()) }},
		{"ability", "RoleAbility", func() error { return atom.AddAbility(&ability.RoleAbility{}) }},
		{"ability", "TimeAbility", func() error {
			ta, err := ability.NewTimeAbility()
			if err != nil {
				return err
			}
			return atom.AddAbility(ta)
		}},
		{"ability", "NetMapAbility", func() error { return atom.AddAbility(ability.NewNetMapAbility()) }},
		{"ability", "OneKeyAbility", func() error { return atom.AddAbility(ability.NewOneKeyAbility()) }},
		{"ability", "CmdAbility", func() error { return atom.AddAbility(ability.NewCmdAbility()) }},
		{"ability", "ShAbility", func() error { return atom.AddAbility(ability.NewShAbility()) }},
		{"ability", "BashAbility", func() error { return atom.AddAbility(ability.NewBashAbility()) }},
		{"ability", "ConfigFileAbility", func() error { return atom.AddAbility(ability.NewConfigFileAbility()) }},
	}
	for _, r := range regs {
		if err := r.fn(); err != nil {
			panic(fmt.Sprintf("InitStandardAtom register %s %s: %v", r.kind, r.name, err))
		}
	}
	return atom
}
