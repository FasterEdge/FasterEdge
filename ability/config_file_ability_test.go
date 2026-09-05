package ability

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/FasterEdge/FasterEdge/data"
	"github.com/FasterEdge/FasterEdge/types"
)

func newConfigFileAtom(t *testing.T) *types.Atom {
	t.Helper()
	a := &types.Atom{}
	if err := a.AddData(&data.BaseData{}); err != nil {
		t.Fatal(err)
	}
	if err := a.AddData(data.NewConfigData()); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestConfigFileAbilityRejectsMissingDependencies(t *testing.T) {
	a := NewConfigFileAbility()
	if out := a.Command(&types.Atom{}, ConfigFileCommandGetPath, nil); !errors.Is(out.Err, types.ErrMissingDependency) {
		t.Fatalf("missing deps error = %v", out.Err)
	}
	atom := &types.Atom{}
	atom.AddData(&data.BaseData{})
	if out := a.Command(atom, ConfigFileCommandGetPath, nil); !errors.Is(out.Err, types.ErrMissingDependency) {
		t.Fatalf("missing ConfigData error = %v", out.Err)
	}
}

func TestConfigFileAbilitySetGetPath(t *testing.T) {
	a := NewConfigFileAbility()
	root := t.TempDir()
	a.SetRoot(root)
	atom := newConfigFileAtom(t)
	if out := a.Command(atom, ConfigFileCommandSetPath, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set wrong type error = %v", out.Err)
	}
	if out := a.Command(atom, ConfigFileCommandSetPath, ConfigFilePathArg{Path: ""}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set empty path error = %v", out.Err)
	}
	if out := a.Command(atom, ConfigFileCommandSetPath, ConfigFilePathArg{Path: "  "}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set blank path error = %v", out.Err)
	}
	// 逃逸路径必须拒绝(任意文件读写防线)
	if out := a.Command(atom, ConfigFileCommandSetPath, ConfigFilePathArg{Path: "../../etc/passwd"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set escape path error = %v", out.Err)
	}
	if out := a.Command(atom, ConfigFileCommandSetPath, ConfigFilePathArg{Path: "sub/cfg.json"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := a.Command(atom, ConfigFileCommandGetPath, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get with args error = %v", out.Err)
	}
	wantPath := filepath.Join(root, "sub", "cfg.json")
	if out := a.Command(atom, ConfigFileCommandGetPath, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if out.Value != wantPath {
		t.Fatalf("get path = %q, want %q", out.Value, wantPath)
	}
}

func TestConfigFileAbilityExists(t *testing.T) {
	a := NewConfigFileAbility()
	root := t.TempDir()
	a.SetRoot(root)
	atom := newConfigFileAtom(t)
	if out := a.Command(atom, ConfigFileCommandExists, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("exists wrong type error = %v", out.Err)
	}
	if out := a.Command(atom, ConfigFileCommandExists, ConfigFilePathArg{Path: ""}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("exists empty error = %v", out.Err)
	}
	// 逃逸路径拒绝
	if out := a.Command(atom, ConfigFileCommandExists, ConfigFilePathArg{Path: "../secret.json"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("exists escape error = %v", out.Err)
	}
	tmpFile := filepath.Join(root, "exists.json")
	if out := a.Command(atom, ConfigFileCommandExists, ConfigFilePathArg{Path: tmpFile}); out.Err != nil {
		t.Fatal(out.Err)
	} else if out.Value.(bool) {
		t.Fatal("expected false for non-existent file")
	}
	if err := os.WriteFile(tmpFile, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := a.Command(atom, ConfigFileCommandExists, ConfigFilePathArg{Path: tmpFile}); out.Err != nil {
		t.Fatal(out.Err)
	} else if !out.Value.(bool) {
		t.Fatal("expected true for existing file")
	}
}

func TestConfigFileAbilityLoadSave(t *testing.T) {
	a := NewConfigFileAbility()
	atom := newConfigFileAtom(t)
	dir := t.TempDir()
	a.SetRoot(dir)
	path := filepath.Join(dir, "config.json")
	// 类型错误
	if out := a.Command(atom, ConfigFileCommandLoad, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("load wrong type error = %v", out.Err)
	}
	// 空路径
	if out := a.Command(atom, ConfigFileCommandLoad, ConfigFileLoadArgs{Path: ""}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("load empty path error = %v", out.Err)
	}
	// 文件不存在,strict=true
	if out := a.Command(atom, ConfigFileCommandLoad, ConfigFileLoadArgs{Path: path, Strict: true}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("load missing strict error = %v", out.Err)
	}
	// 文件不存在,strict=false → 返回当前快照
	if out := a.Command(atom, ConfigFileCommandLoad, ConfigFileLoadArgs{Path: path}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// 准备配置
	cfg, _ := atom.Data("ConfigData")
	if out := cfg.(*data.ConfigData).Command(atom, data.ConfigCommandSet, data.ConfigSetArgs{Key: "server.port", Value: "9090"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// save 类型错误
	if out := a.Command(atom, ConfigFileCommandSave, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("save wrong type error = %v", out.Err)
	}
	// save
	if out := a.Command(atom, ConfigFileCommandSave, ConfigFileSaveArgs{Path: path}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// 文件应存在且内容包含 server.port
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		t.Fatal("saved file is empty")
	}
	// overwrite=false 且文件已存在
	if out := a.Command(atom, ConfigFileCommandSave, ConfigFileSaveArgs{Path: path}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("overwrite=false should reject, err = %v", out.Err)
	}
	// overwrite=true
	if out := a.Command(atom, ConfigFileCommandSave, ConfigFileSaveArgs{Path: path, Overwrite: true}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// 加载回去
	if out := a.Command(atom, ConfigFileCommandLoad, ConfigFileLoadArgs{Path: path, Strict: true}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// 解析失败的文件
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte("not-json"), 0o644)
	if out := a.Command(atom, ConfigFileCommandLoad, ConfigFileLoadArgs{Path: bad, Strict: true}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("bad json strict error = %v", out.Err)
	}
	if out := a.Command(atom, ConfigFileCommandLoad, ConfigFileLoadArgs{Path: bad}); out.Err != nil {
		t.Fatalf("bad json non-strict should be silent, got err=%v", out.Err)
	}
	// value 不是 string
	bad2 := filepath.Join(dir, "bad2.json")
	os.WriteFile(bad2, []byte(`{"a": 1}`), 0o644)
	if out := a.Command(atom, ConfigFileCommandLoad, ConfigFileLoadArgs{Path: bad2, Strict: true}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("non-string value strict error = %v", out.Err)
	}
	// key 不合法
	bad3 := filepath.Join(dir, "bad3.json")
	os.WriteFile(bad3, []byte(`{"bad key!": "v"}`), 0o644)
	if out := a.Command(atom, ConfigFileCommandLoad, ConfigFileLoadArgs{Path: bad3, Strict: true}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("bad key strict error = %v", out.Err)
	}
	// 绝对路径逃逸(root 父目录/系统目录)必须拒绝——含 Windows 盘符路径
	escape := filepath.Join(filepath.Dir(dir), "outside.json")
	if out := a.Command(atom, ConfigFileCommandLoad, ConfigFileLoadArgs{Path: escape, Strict: true}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("absolute escape load error = %v", out.Err)
	}
	if out := a.Command(atom, ConfigFileCommandSave, ConfigFileSaveArgs{Path: escape, Overwrite: true}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("absolute escape save error = %v", out.Err)
	}
}

func TestConfigFileAbilitySaveWithoutPath(t *testing.T) {
	a := NewConfigFileAbility()
	atom := newConfigFileAtom(t)
	if out := a.Command(atom, ConfigFileCommandSave, ConfigFileSaveArgs{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("save no path error = %v", out.Err)
	}
}

func TestConfigFileAbilityUnknownCommand(t *testing.T) {
	a := NewConfigFileAbility()
	atom := newConfigFileAtom(t)
	if out := a.Command(atom, "nope", nil); !errors.Is(out.Err, types.ErrUnsupportedCommand) {
		t.Fatalf("unknown error = %v", out.Err)
	}
}
