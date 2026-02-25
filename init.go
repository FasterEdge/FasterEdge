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
