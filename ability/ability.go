package ability

import (
	"github.com/FasterEdge/FasterEdge/data"
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
	return true
}

func (b *BaseAbility) Mount(atmo types.Atom) bool {
	val, _ := data.GetData("_data_list")
	list, _ := val.([]string)
	list = append(list, b.GetName())
	data.SetData("_data_list", list)
	return true
}

func (b *BaseAbility) Command(atmo types.Atom, act string, args ...string) bool {
	switch act {
	case "list_data":
		val, _ := data.GetData("_data_list")
		list, _ := val.([]string)
		for _, item := range list {
			println(item)
		}
		return true
	case "list_ability":
		val, _ := data.GetData("_ability_list")
		list, _ := val.([]string)
		for _, item := range list {
			println(item)
		}
		return true
	}

	return true
}
