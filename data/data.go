package data

import "github.com/FasterEdge/FasterEdge/types"

type BaseData struct {
}

func (b *BaseData) GetName() string {
	return "BaseData"
}

func (b *BaseData) Describe() string {
	return "BaseData存储一些基本数据，简化通过DataPool进行的访问。"
}

func (b *BaseData) Check(atmo types.Atom) bool {
	return true
}

func (b *BaseData) Mount(atmo types.Atom) bool {
	val, _ := GetData("_data_list")
	list, _ := val.([]string)
	list = append(list, b.GetName())
	SetData("_data_list", list)
	return true
}

func (b *BaseData) Command(atmo types.Atom, act string, args ...string) bool {
	return false
}
