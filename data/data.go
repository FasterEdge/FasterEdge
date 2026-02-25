package data

import "github.com/FasterEdge/FasterEdge/types"

var version string = "1.0.20260225"

type BaseData struct {
}

func (b *BaseData) GetName() string {
	return "BaseData"
}

func (b *BaseData) Describe() string {
	return "BaseData存储一些基本数据，可以在其中存储各种基本信息。"
}

func (b *BaseData) Check(atmo types.Atom) bool {
	// 最最基础的一个属性，不检查任何东西，直接返回true
	return true
}

func (b *BaseData) Mount(atmo types.Atom) bool {
	b.Check(atmo)
	atmo.AddData(b)
	return true
}

func (b *BaseData) Command(atmo types.Atom, act string, args ...string) bool {
	switch act {
	case "version":
		println(version)
		return true
	}

	return false
}
