package ability

import (
	"errors"
	"sync"
	"testing"

	"github.com/FasterEdge/FasterEdge/types"
)

func TestRoleAbilityRejectsWrongAndBlankSetRoleArguments(t *testing.T) {
	r := &RoleAbility{}
	for _, args := range []any{nil, RoleAbilityArgs{}, RoleAbilityArgs{Role: "  "}, &RoleAbilityArgs{Role: "worker"}} {
		if out := r.Command(nil, CommandSetRole, args); !errors.Is(out.Err, types.ErrInvalidArguments) {
			t.Fatalf("args %#v error = %v", args, out.Err)
		}
	}
}

func TestRoleAbilitySetAndGetRole(t *testing.T) {
	r := &RoleAbility{}
	if out := r.Command(nil, CommandSetRole, RoleAbilityArgs{Role: "worker"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	out := r.Command(nil, CommandGetRole, nil)
	if out.Err != nil || out.Value != "worker" {
		t.Fatalf("get role = %#v, error %v", out.Value, out.Err)
	}
}

func TestRoleAbilityConcurrentCommands(t *testing.T) {
	r := &RoleAbility{}
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if out := r.Command(nil, CommandSetRole, RoleAbilityArgs{Role: "worker"}); out.Err != nil {
				errs <- out.Err
			}
			if out := r.Command(nil, CommandGetRole, nil); out.Err != nil {
				errs <- out.Err
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
