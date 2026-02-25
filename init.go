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

func RunAtom(atom types.Atom) {
	// 对所有的Data进行挂载
	for _, data := range atom.GetAllData() {
		data.Mount(atom)
	}
	// 对所有的Ability进行挂载
	for _, ability := range atom.GetAllAbility() {
		ability.Mount(atom)
	}

}
