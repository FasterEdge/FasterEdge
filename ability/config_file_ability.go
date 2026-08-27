package ability

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/FasterEdge/FasterEdge/data"
	"github.com/FasterEdge/FasterEdge/types"
)

const (
	ConfigFileCommandLoad    = "load"
	ConfigFileCommandSave    = "save"
	ConfigFileCommandSetPath = "set_path"
	ConfigFileCommandGetPath = "get_path"
	ConfigFileCommandExists  = "exists"
)

// ConfigFileLoadArgs 是 load 命令的参数。
type ConfigFileLoadArgs struct {
	Path   string
	Strict bool // 严格模式下文件不存在/解析失败返回错误,否则静默返回当前快照
}

// ConfigFileSaveArgs 是 save 命令的参数。
type ConfigFileSaveArgs struct {
	Path      string
	Overwrite bool
}

// ConfigFilePathArg 是 set_path / exists / get_path 的参数。
type ConfigFilePathArg struct {
	Path string
}

// ConfigFileAbility 在 ConfigData 之上提供 JSON 配置文件的加载与保存。
type ConfigFileAbility struct {
	mu   sync.RWMutex
	path string
}

func NewConfigFileAbility() *ConfigFileAbility { return &ConfigFileAbility{} }

func (a *ConfigFileAbility) GetName() string { return "ConfigFileAbility" }
func (a *ConfigFileAbility) Dependencies() []types.Dependency {
	return []types.Dependency{{Kind: types.DependencyData, Name: "BaseData"}, {Kind: types.DependencyData, Name: "ConfigData"}}
}

func (a *ConfigFileAbility) Describe() string {
	return "ConfigFileAbility在ConfigData之上提供 JSON 配置文件的加载/保存/路径管理,支持扁平点号键。"
}

func (a *ConfigFileAbility) Check(atom *types.Atom) error {
	if atom == nil {
		return types.ErrMissingDependency
	}
	if _, ok := atom.Data("BaseData"); !ok {
		return types.ErrMissingDependency
	}
	if _, ok := atom.Data("ConfigData"); !ok {
		return types.ErrMissingDependency
	}
	return nil
}

func (a *ConfigFileAbility) Mount(atom *types.Atom) error { return a.Check(atom) }

func (a *ConfigFileAbility) Command(atom *types.Atom, act string, args any) types.CommandOutput {
	if err := a.Check(atom); err != nil {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, err)}
	}
	cfgRaw, _ := atom.Data("ConfigData")
	cfg, _ := cfgRaw.(*data.ConfigData)
	if cfg == nil {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: ConfigData type mismatch: %w", act, types.ErrInvalidArguments)}
	}
	switch act {
	case ConfigFileCommandSetPath:
		typed, ok := args.(ConfigFilePathArg)
		if !ok || strings.TrimSpace(typed.Path) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		cleaned := filepath.Clean(strings.TrimSpace(typed.Path))
		a.mu.Lock()
		a.path = cleaned
		a.mu.Unlock()
		return types.CommandOutput{Name: act, Value: cleaned}
	case ConfigFileCommandGetPath:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		a.mu.RLock()
		defer a.mu.RUnlock()
		return types.CommandOutput{Name: act, Value: a.path}
	case ConfigFileCommandExists:
		typed, ok := args.(ConfigFilePathArg)
		if !ok || strings.TrimSpace(typed.Path) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		if _, err := os.Stat(filepath.Clean(typed.Path)); err != nil {
			return types.CommandOutput{Name: act, Value: false}
		}
		return types.CommandOutput{Name: act, Value: true}
	case ConfigFileCommandLoad:
		typed, ok := args.(ConfigFileLoadArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		if strings.TrimSpace(typed.Path) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: empty path: %w", act, types.ErrInvalidArguments)}
		}
		cleaned := filepath.Clean(typed.Path)
		raw, err := os.ReadFile(cleaned)
		if err != nil {
			if typed.Strict {
				return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: read %s: %w: %v", act, cleaned, types.ErrInvalidArguments, err)}
			}
			return types.CommandOutput{Name: act, Value: cfg.Snapshot()}
		}
		parsed, perr := parseFlatJSON(raw)
		if perr != nil {
			if typed.Strict {
				return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: parse %s: %w: %v", act, cleaned, types.ErrInvalidArguments, perr)}
			}
			return types.CommandOutput{Name: act, Value: cfg.Snapshot()}
		}
		cfg.ReplaceAll(parsed)
		a.mu.Lock()
		a.path = cleaned
		a.mu.Unlock()
		return types.CommandOutput{Name: act, Value: cfg.Snapshot()}
	case ConfigFileCommandSave:
		typed, ok := args.(ConfigFileSaveArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		path := strings.TrimSpace(typed.Path)
		if path == "" {
			a.mu.RLock()
			path = a.path
			a.mu.RUnlock()
		}
		if path == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no path set: %w", act, types.ErrInvalidArguments)}
		}
		cleaned := filepath.Clean(path)
		if !typed.Overwrite {
			if _, err := os.Stat(cleaned); err == nil {
				return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: file exists, pass Overwrite=true: %w", act, types.ErrInvalidArguments)}
			}
		}
		payload, err := cfg.JSONMarshal()
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: marshal: %w: %v", act, types.ErrInvalidArguments, err)}
		}
		if err := os.MkdirAll(filepath.Dir(cleaned), 0o755); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: mkdir: %w: %v", act, types.ErrInvalidArguments, err)}
		}
		if err := os.WriteFile(cleaned, payload, 0o644); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: write: %w: %v", act, types.ErrInvalidArguments, err)}
		}
		a.mu.Lock()
		a.path = cleaned
		a.mu.Unlock()
		return types.CommandOutput{Name: act, Value: cleaned}
	}
	return types.CommandOutput{Name: act, Err: fmt.Errorf("command %s: %w", act, types.ErrUnsupportedCommand)}
}

// parseFlatJSON 解析 {"a.b": "v", "c": "d"} 形式的扁平 JSON,所有 value 必须是字符串。
// 不支持嵌套对象,以保持 ConfigData 的扁平语义一致。
func parseFlatJSON(raw []byte) (map[string]string, error) {
	var anyMap map[string]any
	if err := json.Unmarshal(raw, &anyMap); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(anyMap))
	for k, v := range anyMap {
		if err := data.ValidateConfigKeyExternal(k); err != nil {
			return nil, err
		}
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("value for %q is not a string", k)
		}
		out[k] = s
	}
	return out, nil
}
