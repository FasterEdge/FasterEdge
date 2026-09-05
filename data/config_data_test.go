package data

import (
	"errors"
	"fmt"
	"testing"

	"github.com/FasterEdge/FasterEdge/types"
)

func TestConfigDataSetGetDeleteList(t *testing.T) {
	c := NewConfigData()
	// 类型错误
	if out := c.Command(nil, ConfigCommandSet, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set wrong type error = %v", out.Err)
	}
	// 空 key
	if out := c.Command(nil, ConfigCommandSet, ConfigSetArgs{Key: "", Value: "v"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("empty key error = %v", out.Err)
	}
	// 非法字符
	if out := c.Command(nil, ConfigCommandSet, ConfigSetArgs{Key: "bad key!", Value: "v"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("bad key error = %v", out.Err)
	}
	// 正常 set
	if out := c.Command(nil, ConfigCommandSet, ConfigSetArgs{Key: "server.port", Value: "8080"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// get 类型错误
	if out := c.Command(nil, ConfigCommandGet, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get wrong type error = %v", out.Err)
	}
	// get 不存在
	if out := c.Command(nil, ConfigCommandGet, ConfigGetArgs{Key: "missing"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get missing error = %v", out.Err)
	}
	// 正常 get
	out := c.Command(nil, ConfigCommandGet, ConfigGetArgs{Key: "server.port"})
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	if out.Value != "8080" {
		t.Fatalf("get value = %q", out.Value)
	}
	// list
	if out := c.Command(nil, ConfigCommandList, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("list with args error = %v", out.Err)
	}
	listOut := c.Command(nil, ConfigCommandList, nil)
	if listOut.Err != nil {
		t.Fatal(listOut.Err)
	}
	keys, ok := listOut.Value.([]string)
	if !ok || len(keys) != 1 || keys[0] != "server.port" {
		t.Fatalf("list = %#v", listOut.Value)
	}
	// delete
	if out := c.Command(nil, ConfigCommandDelete, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("delete wrong type error = %v", out.Err)
	}
	if out := c.Command(nil, ConfigCommandDelete, ConfigDeleteArgs{Key: "missing"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("delete missing error = %v", out.Err)
	}
	if out := c.Command(nil, ConfigCommandDelete, ConfigDeleteArgs{Key: "server.port"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// snapshot
	if out := c.Command(nil, ConfigCommandSnapshot, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("snapshot with args error = %v", out.Err)
	}
	snapOut := c.Command(nil, ConfigCommandSnapshot, nil)
	if snapOut.Err != nil {
		t.Fatal(snapOut.Err)
	}
	if m, _ := snapOut.Value.(map[string]string); len(m) != 0 {
		t.Fatalf("snapshot = %#v", m)
	}
	// unknown
	if out := c.Command(nil, "nope", nil); !errors.Is(out.Err, types.ErrUnsupportedCommand) {
		t.Fatalf("unknown error = %v", out.Err)
	}
}

func TestConfigDataReplaceAll(t *testing.T) {
	c := NewConfigData()
	if err := c.ReplaceAll(map[string]string{"a": "1", "b": "2"}); err != nil {
		t.Fatal(err)
	}
	if out := c.Command(nil, ConfigCommandList, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if keys, _ := out.Value.([]string); len(keys) != 2 {
		t.Fatalf("list after replace = %#v", keys)
	}
	if err := c.ReplaceAll(map[string]string{"c": "3"}); err != nil {
		t.Fatal(err)
	}
	if v, ok := c.Get("a"); ok {
		t.Fatalf("a should be gone, got %q", v)
	}
	if v, _ := c.Get("c"); v != "3" {
		t.Fatalf("c = %q, want 3", v)
	}
	// 非法键整体拒绝(事务性)
	if err := c.ReplaceAll(map[string]string{"good": "1", "bad key!": "2"}); err == nil {
		t.Fatal("invalid key should reject")
	}
	// 裁剪后收敛: 带空格键与规范键同义
	if err := c.ReplaceAll(map[string]string{" x.y ": "1"}); err != nil {
		t.Fatal(err)
	}
	if v, ok := c.Get("x.y"); !ok || v != "1" {
		t.Fatalf("trimmed key x.y = %q, %v", v, ok)
	}
}

func TestConfigDataJSONMarshal(t *testing.T) {
	c := NewConfigData()
	c.Set("a.b", "1")
	c.Set("c", "2")
	raw, err := c.JSONMarshal()
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		t.Fatal("marshal returned empty")
	}
}

func TestConfigDataSetTrimsKey(t *testing.T) {
	c := NewConfigData()
	// 带空格键与规范键必须收敛为同一键(校验裁剪但存储原始会形成影子键)
	if err := c.Set(" server.port ", "8080"); err != nil {
		t.Fatal(err)
	}
	if v, ok := c.Get("server.port"); !ok || v != "8080" {
		t.Fatalf("trimmed key get = %q, %v", v, ok)
	}
	if keys := c.List(); len(keys) != 1 || keys[0] != "server.port" {
		t.Fatalf("list = %#v, want single trimmed key", keys)
	}
	if !c.Delete(" server.port ") {
		t.Fatal("delete with padded key should find trimmed key")
	}
	if _, ok := c.Get("server.port"); ok {
		t.Fatal("key should be gone after delete")
	}
}

func TestConfigDataLimits(t *testing.T) {
	c := NewConfigData()
	// 单值上限
	big := make([]byte, maxConfigValueBytes+1)
	if err := c.Set("big", string(big)); !errors.Is(err, types.ErrInvalidArguments) {
		t.Fatalf("oversized value error = %v", err)
	}
	// 键数上限
	for i := 0; i < maxConfigKeys; i++ {
		if err := c.Set(fmt.Sprintf("k%d", i), "v"); err != nil {
			t.Fatalf("set %d: %v", i, err)
		}
	}
	if err := c.Set("overflow", "v"); !errors.Is(err, types.ErrInvalidArguments) {
		t.Fatalf("overflow key error = %v", err)
	}
}
