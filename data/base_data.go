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
var version = "1.0.20260225"

// BaseDataArgs 定义
type BaseDataArgs struct{}

// BaseDataArgs 出参
type BaseDataOutput struct {
	Message string
	Success bool
	Error   string
}

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
func (b *BaseData) Check(atmo types.Atom) bool {
	// 最最基础的一个属性，不检查任何东西，直接返回true
	return true
}

// 挂载 Data
func (b *BaseData) Mount(atmo types.Atom) bool {
	if b.Check(atmo) {
		fmt.Printf("[%s] 挂载成功\n", b.GetName())
	}
	atmo.AddData(b)
	return true
}

// 指令入口
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
