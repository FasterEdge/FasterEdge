package data

import (
	"errors"
	"testing"

	"github.com/FasterEdge/FasterEdge/types"
)

func TestBaseDataCommandsReturnValues(t *testing.T) {
	d := &BaseData{}
	for _, tc := range []struct {
		name string
		want string
	}{
		{CommandLogo, logo},
		{CommandInfo, "FasterEdge v" + version + " - 对称、可靠、安全的多场景边缘计算框架"},
	} {
		out := d.Command(nil, tc.name, nil)
		if out.Err != nil {
			t.Fatalf("%s returned error: %v", tc.name, out.Err)
		}
		value, ok := out.Value.(string)
		if !ok || value != tc.want {
			t.Fatalf("%s value = %#v, want %q", tc.name, out.Value, tc.want)
		}
	}
}

func TestBaseDataCommandValidation(t *testing.T) {
	d := &BaseData{}
	if out := d.Command(nil, CommandLogo, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("invalid args error = %v", out.Err)
	}
	if out := d.Command(nil, "unknown", nil); !errors.Is(out.Err, types.ErrUnsupportedCommand) {
		t.Fatalf("unknown command error = %v", out.Err)
	}
}
