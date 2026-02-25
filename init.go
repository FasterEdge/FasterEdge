package FasterEdge

import (
	"github.com/FasterEdge/FasterEdge/ability"
	"github.com/FasterEdge/FasterEdge/data"
	"github.com/FasterEdge/FasterEdge/types"
)

func InitAtom() types.Atom {
	atom := types.Atom{}
	atom.AddData(&data.BaseData{})
	atom.AddAbility(&ability.BaseAbility{})
	return atom
}

// 只挂载数据和能力，不执行任何命令，用于给用户提供自定义开发的基础环境
func PreRunAtom(atom types.Atom) {
	// 对所有的Data进行挂载
	for _, data := range atom.GetAllData() {
		data.Mount(atom)
	}
	// 对所有的Ability进行挂载
	for _, ability := range atom.GetAllAbility() {
		ability.Mount(atom)
	}

	if data, ok := atom.GetAllData()["BaseData"]; ok {
		data.Command(atom, "print_logo")
		data.Command(atom, "print_info")
	}
}

// 挂载并直接使用携程运行所有Ability里面的run指令（如果runable返回true）
func RunAtom(atom types.Atom) {
	// 对所有的Data进行挂载
	for _, data := range atom.GetAllData() {
		data.Mount(atom)
	}
	// 对所有的Ability进行挂载
	for _, ability := range atom.GetAllAbility() {
		ability.Mount(atom)
	}

	// 直接使用携程运行所有Ability里面的run指令（如果runable返回true）
	for _, ability := range atom.GetAllAbility() {
		if ability.Command(atom, "runnable") {
			go ability.Command(atom, "run")
		}
	}

	if data, ok := atom.GetAllData()["BaseData"]; ok {
		data.Command(atom, "print_logo")
		data.Command(atom, "print_info")
	}

	if base, ok := atom.GetAllAbility()["BaseAbility"]; ok {
		// blocking 会永久阻塞，因此放到 goroutine，避免主流程被卡住
		base.Command(atom, "blocking")
	}
}
