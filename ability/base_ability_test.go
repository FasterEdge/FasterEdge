package ability

import (
	"errors"
	"reflect"
	"testing"

	"github.com/FasterEdge/FasterEdge/data"
	"github.com/FasterEdge/FasterEdge/types"
)

func TestBaseAbilityListsSortedNames(t *testing.T) {
	a := &types.Atom{}
	for _, d := range []types.Data{&data.BaseData{}} {
		if err := a.AddData(d); err != nil {
			t.Fatal(err)
		}
	}
	for _, ab := range []types.Ability{&BaseAbility{}, &RoleAbility{}} {
		if err := a.AddAbility(ab); err != nil {
			t.Fatal(err)
		}
	}
	b := &BaseAbility{}
	out := b.Command(a, CommandListAbilityNames, nil)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	if got, want := out.Value, []string{"BaseAbility", "RoleAbility"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ability names = %#v, want %#v", got, want)
	}
}

func TestBaseAbilityCommandValidation(t *testing.T) {
	b := &BaseAbility{}
	a := &types.Atom{}
	var typedNil *struct{}
	if out := b.Command(a, CommandListDataNames, typedNil); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("typed nil error = %v", out.Err)
	}
	if out := b.Command(a, "blocking", nil); !errors.Is(out.Err, types.ErrUnsupportedCommand) {
		t.Fatalf("blocking error = %v", out.Err)
	}
}
