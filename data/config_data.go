package data

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/FasterEdge/FasterEdge/types"
)

const (
	ConfigCommandGet      = "get"
	ConfigCommandSet      = "set"
	ConfigCommandDelete   = "delete"
	ConfigCommandList     = "list"
	ConfigCommandSnapshot = "snapshot"
)

// ConfigData 以键值树形式存储配置项(扁平点号路径,如 "server.port")。
// 它只持有内存态,文件 I/O 由 ConfigFileAbility 负责。
type ConfigData struct {
	mu     sync.RWMutex
	values map[string]string
}

func NewConfigData() *ConfigData { return &ConfigData{values: make(map[string]string)} }

func (c *ConfigData) GetName() string { return "ConfigData" }

func (c *ConfigData) Describe() string {
	return "ConfigData以键值对形式存储节点配置项,支持点号路径扁平命名。"
}

func (c *ConfigData) Check(_ *types.Atom) error { return nil }

func (c *ConfigData) Mount(_ *types.Atom) error { return nil }

// Get 返回指定键的当前值与是否存在。
func (c *ConfigData) Get(key string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.values[key]
	return v, ok
}

// Set 设置键值;key 必须非空且仅由字母数字、点、下划线、连字符组成。
func (c *ConfigData) Set(key, value string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	c.mu.Lock()
	c.values[key] = value
	c.mu.Unlock()
	return nil
}

// Delete 删除键,返回是否存在。
func (c *ConfigData) Delete(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.values[key]
	if ok {
		delete(c.values, key)
	}
	return ok
}

// List 返回所有键(按字典序排序)。
func (c *ConfigData) List() []string {
	c.mu.RLock()
	out := make([]string, 0, len(c.values))
	for k := range c.values {
		out = append(out, k)
	}
	c.mu.RUnlock()
	sort.Strings(out)
	return out
}

// ReplaceAll 整体覆盖当前所有键值(供 Ability 加载文件后调用)。
func (c *ConfigData) ReplaceAll(values map[string]string) {
	c.mu.Lock()
	c.values = make(map[string]string, len(values))
	for k, v := range values {
		c.values[k] = v
	}
	c.mu.Unlock()
}

// Snapshot 返回当前所有键值的深拷贝。
func (c *ConfigData) Snapshot() map[string]string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]string, len(c.values))
	for k, v := range c.values {
		out[k] = v
	}
	return out
}

// GetSet is a helper used by Ability when applying a parsed file in-place.
func (c *ConfigData) GetSet() map[string]string { return c.Snapshot() }

// ConfigGetArgs 是 get 命令的参数。
type ConfigGetArgs struct {
	Key string
}

// ConfigSetArgs 是 set 命令的参数。
type ConfigSetArgs struct {
	Key   string
	Value string
}

// ConfigDeleteArgs 是 delete 命令的参数。
type ConfigDeleteArgs struct {
	Key string
}

// ValidateConfigKeyExternal 暴露给 ability 包使用,避免在两个包内重复校验逻辑。
func ValidateConfigKeyExternal(key string) error { return validateKey(key) }

func validateKey(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("config: empty key: %w", types.ErrInvalidArguments)
	}
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-':
		default:
			return fmt.Errorf("config: invalid key %q: %w", key, types.ErrInvalidArguments)
		}
	}
	return nil
}

func (c *ConfigData) Command(_ *types.Atom, act string, args any) types.CommandOutput {
	switch act {
	case ConfigCommandGet:
		typed, ok := args.(ConfigGetArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		if err := validateKey(typed.Key); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, err)}
		}
		v, ok := c.Get(typed.Key)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: key %q not found: %w", act, typed.Key, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: v}
	case ConfigCommandSet:
		typed, ok := args.(ConfigSetArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		if err := c.Set(typed.Key, typed.Value); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, err)}
		}
		return types.CommandOutput{Name: act, Value: typed.Value}
	case ConfigCommandDelete:
		typed, ok := args.(ConfigDeleteArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		if err := validateKey(typed.Key); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, err)}
		}
		if !c.Delete(typed.Key) {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: key %q not found: %w", act, typed.Key, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: typed.Key}
	case ConfigCommandList:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: c.List()}
	case ConfigCommandSnapshot:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: c.Snapshot()}
	}
	return types.CommandOutput{Name: act, Err: fmt.Errorf("command %s: %w", act, types.ErrUnsupportedCommand)}
}

// JSONMarshal 返回整个配置的 JSON 表示,供 ConfigFileAbility 保存到文件。
func (c *ConfigData) JSONMarshal() ([]byte, error) {
	return json.MarshalIndent(c.Snapshot(), "", "  ")
}
