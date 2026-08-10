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

// 只挂载数据和能力，用于给用户提供自定义开发的基础环境
func PreRunAtom(atom *types.Atom) {
	// 对所有的Data进行挂载
	for _, d := range atom.GetAllData() {
		_ = d.Mount(atom)
	}
	// 对所有的Ability进行挂载
	for _, ab := range atom.GetAllAbility() {
		_ = ab.Mount(atom)
	}

	if d, ok := atom.GetAllData()["BaseData"]; ok {
		d.Command(atom, "print_logo", nil)
		d.Command(atom, "print_info", nil)
	}
}

// 使用携程运行所有Ability里面的run指令（如果runable返回true）
func RunAtom(atom *types.Atom) {
	// 直接使用携程运行所有Ability里面的run指令（如果runable返回true）
	for _, ab := range atom.GetAllAbility() {
		if ab.Command(atom, "runnable", nil).Success() {
			go ab.Command(atom, "run", nil)
		}
	}

	if base, ok := atom.GetAllAbility()["BaseAbility"]; ok {
		base.Command(atom, "blocking", nil)
	}
}
