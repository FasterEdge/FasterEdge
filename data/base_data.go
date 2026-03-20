package data

import (
	"fmt"

	"github.com/FasterEdge/FasterEdge/types"
)

var logo string = `
 _______ _______ _______ _______ _______  ______ _______ ______   ______ _______
 |______ |_____| |______    |    |______ |_____/ |______ |     \ |  ____ |______
 |       |     | ______|    |    |______ |    \_ |______ |_____/ |_____| |______
`
var version string = "1.0.20260225"

// BaseDataArgs defines accepted arguments for BaseData commands.
type BaseDataArgs struct{}

// BaseDataOutput represents command outputs.
type BaseDataOutput struct {
	Message string
	Success bool
	Error   string
}

type BaseData struct{}

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

func (b *BaseData) Command(atmo types.Atom, act string, args any) types.DataOutput {
	_ = atmo
	switch act {
	case "print_logo":
		fmt.Println(logo)
		return types.DataOutput{Name: act, Success: true}
	case "print_info":
		fmt.Println("FasterEdge v" + version + " - 对称、可靠、安全的多场景边缘计算框架")
		return types.DataOutput{Name: act, Success: true}
	}

	return types.DataOutput{Name: act, Success: false, Error: "unsupported act"}
}
