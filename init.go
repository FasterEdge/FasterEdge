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
func PreRunAtom(atom *types.Atom) error {
	return atom.PreRun()
}
