package ability

import (
	"fmt"

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

func (b *BaseAbility) Command(atmo types.Atom, act string, args ...string) bool {
	switch act {
	case "list_data_name":
		fmt.Printf("[%s] 正在执行 list_data_name\n", b.GetName())
		for key := range atmo.GetAllData() { // print map keys
			println(key)
		}
		return true

	case "list_ability_name":
		fmt.Printf("[%s] 正在执行 list_ability_name\n", b.GetName())
		for key := range atmo.GetAllAbility() { // print map keys
			println(key)
		}
		return true

	case "blocking":
		fmt.Printf("[%s] 正在执行 blocking\n", b.GetName())
		// 这个指令会永久阻塞，以防止其他携程运行被打断
		select {}
	}

	return false
}
