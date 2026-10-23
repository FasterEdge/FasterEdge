// ─────────────────────────────────────────────────────────────
// FasterEdge 开源项目
// Github: https://github.com/FasterEdge
// Gitee:  https://gitee.com/FasterEdge
// ─────────────────────────────────────────────────────────────
package ability

import (
	"encoding/json"
	"fmt"
	"io"
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

	// configFileMaxBytes 是 load 读取的配置文件大小上限,防 /dev/zero 等
	// 无限流设备导致挂死/内存耗尽。
	configFileMaxBytes = 1 << 20
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
// 所有 load/save/exists/set_path 路径都被约束在 root 目录内, 防止任意文件
// 读写(旧实现接受任意路径: load 可读任意 JSON 配置文件、save 可覆盖任意
// 进程可写文件——能力超出"配置文件管理"语义且可绕过 CmdAbility 的 allowlist 控制)。
type ConfigFileAbility struct {
	mu   sync.RWMutex
	path string
	root string
}

func NewConfigFileAbility() *ConfigFileAbility { return &ConfigFileAbility{} }

// SetRoot 设置配置文件的允许根目录(必须在 Mount 前调用);
// 未设置时 Mount 默认使用当前工作目录。
func (a *ConfigFileAbility) SetRoot(dir string) {
	a.mu.Lock()
	a.root = filepath.Clean(dir)
	a.mu.Unlock()
}

// confine 把用户输入的路径解析到 root 内并拒绝逃逸。
// 相对路径基于 root 解析; 绝对路径直接使用但必须位于 root 内
// (Windows 盘符/POSIX 根路径不参与 Join, 避免 Join 把盘符当普通段)。
// root 未配置时回退到当前工作目录(与 Mount 默认一致)。
func (a *ConfigFileAbility) confine(p string) (string, error) {
	a.mu.RLock()
	root := a.root
	a.mu.RUnlock()
	if root == "" {
		if wd, err := os.Getwd(); err == nil {
			root = wd
		} else {
			root = "."
		}
	}
	clean := filepath.Clean(p)
	var joined string
	if filepath.IsAbs(clean) {
		joined = clean
	} else {
		joined = filepath.Join(root, clean)
	}
	rel, err := filepath.Rel(root, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q escapes root %q: %w", p, root, types.ErrInvalidArguments)
	}
	// 词法 confine 不防符号链接: root 内 planted symlink 指向 root 外时,
	// os.Open/os.WriteFile/os.Stat 跟随链接读写 root 之外的文件(load 读
	// 任意文件, save(Overwrite=true) 截断/覆写任意进程可写文件)——逐组件
	// Lstat 拒绝链接(root 本身不查——它是用户显式配置的基目录)。
	if err := rejectSymlinkComponents(root, joined); err != nil {
		return "", fmt.Errorf("path %q rejected: %v: %w", p, err, types.ErrInvalidArguments)
	}
	return joined, nil
}

// rejectSymlinkComponents 检查 joined(root 内) 的每个路径组件(不含 root
// 本身)是否为符号链接。
func rejectSymlinkComponents(root, joined string) error {
	rel, err := filepath.Rel(root, joined)
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	cur := root
	for _, seg := range strings.Split(rel, string(os.PathSeparator)) {
		cur = filepath.Join(cur, seg)
		if li, lerr := os.Lstat(cur); lerr == nil && li.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path component %q is a symlink", cur)
		}
	}
	return nil
}

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

func (a *ConfigFileAbility) Mount(atom *types.Atom) error {
	if err := a.Check(atom); err != nil {
		return err
	}
	a.mu.Lock()
	if a.root == "" {
		if wd, err := os.Getwd(); err == nil {
			a.root = wd
		} else {
			a.root = "."
		}
	}
	a.mu.Unlock()
	return nil
}

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
		cleaned, err := a.confine(typed.Path)
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %v", act, types.ErrInvalidArguments, err)}
		}
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
		cleaned, err := a.confine(typed.Path)
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %v", act, types.ErrInvalidArguments, err)}
		}
		if _, err := os.Stat(cleaned); err != nil {
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
		cleaned, err := a.confine(typed.Path)
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %v", act, types.ErrInvalidArguments, err)}
		}
		f, err := os.Open(cleaned)
		if err != nil {
			if typed.Strict {
				// 文件 I/O 运行期失败(含 ErrNotExist)是操作失败——双 %w 链底层
				return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: read %s: %w: %w", act, cleaned, types.ErrOperationFailed, err)}
			}
			return types.CommandOutput{Name: act, Value: cfg.Snapshot()}
		}
		defer f.Close()
		raw, err := io.ReadAll(io.LimitReader(f, configFileMaxBytes+1))
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: read %s: %w: %w", act, cleaned, types.ErrOperationFailed, err)}
		}
		if len(raw) > configFileMaxBytes {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: file %s exceeds %d bytes: %w", act, cleaned, configFileMaxBytes, types.ErrOperationFailed)}
		}
		parsed, perr := parseFlatJSON(raw)
		if perr != nil {
			if typed.Strict {
				return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: parse %s: %w: %v", act, cleaned, types.ErrInvalidArguments, perr)}
			}
			return types.CommandOutput{Name: act, Value: cfg.Snapshot()}
		}
		if err := cfg.ReplaceAll(parsed); err != nil {
			if typed.Strict {
				return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: apply %s: %w: %v", act, cleaned, types.ErrInvalidArguments, err)}
			}
			return types.CommandOutput{Name: act, Value: cfg.Snapshot()}
		}
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
		cleaned, err := a.confine(path)
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %v", act, types.ErrInvalidArguments, err)}
		}
		if !typed.Overwrite {
			if _, err := os.Stat(cleaned); err == nil {
				return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: file exists, pass Overwrite=true: %w", act, types.ErrInvalidArguments)}
			}
		}
		payload, err := cfg.JSONMarshal()
		if err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: marshal: %w: %w", act, types.ErrOperationFailed, err)}
		}
		if err := os.MkdirAll(filepath.Dir(cleaned), 0o755); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: mkdir: %w: %w", act, types.ErrOperationFailed, err)}
		}
		if err := os.WriteFile(cleaned, payload, 0o644); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: write: %w: %w", act, types.ErrOperationFailed, err)}
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
