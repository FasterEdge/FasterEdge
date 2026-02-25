package ability

import (
	"github.com/FasterEdge/FasterEdge/types"
)

type BaseAbility struct {
}

func (a *BaseAbility) GetName() string {
	return "BaseAbility"
}

func (b *BaseAbility) Describe() string {
	return "BaseAbility是一个基础能力，提供一些基本功能。"
}

func (b *BaseAbility) Check(atmo types.Atom) bool {
	// 检查BaseDaata是否已经被挂载
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

func (b *BaseAbility) Command(atmo types.Atom, act string, args ...string) bool {
	switch act {
	case "list_data_name":
		for key := range atmo.GetAllData() { // print map keys
			println(key)
		}
		return true
	case "list_ability_name":
		for key := range atmo.GetAllAbility() { // print map keys
			println(key)
		}
		return true
	case "blocking":
		select {}
		return true
	}

	return false
}
