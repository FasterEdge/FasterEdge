package data

import "github.com/FasterEdge/FasterEdge/types"

type BaseData struct {
}

func (b *BaseData) GetName() string {
	return "BaseData"
}

func (b *BaseData) Describe() string {
	return "BaseData存储一些基本数据，可以在其中存储各种基本信息。"
}

func (b *BaseData) Check(atmo types.Atom) bool {
	return true
}

func (b *BaseData) Mount(atmo types.Atom) bool {
	b.Check(atmo)
	atmo.AddData(b)
	return true
}

func (b *BaseData) Command(atmo types.Atom, act string, args ...string) bool {
	switch act {
	}

	return true
}
