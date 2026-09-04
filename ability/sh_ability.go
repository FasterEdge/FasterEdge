// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package ability

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/FasterEdge/FasterEdge/types"
)

const (
	ShCommandRun          = "run"
	ShCommandStart        = "start"
	ShCommandWait         = "wait"
	ShCommandKill         = "kill"
	ShCommandList         = "list"
	ShCommandSetAllowlist = "set_allowlist"
	ShCommandGetAllowlist = "get_allowlist"
)

// ShRunArgs 是 run/start 命令的参数。
type ShRunArgs struct {
	Command string
	Timeout time.Duration
	Env     []string
}

// ShWaitArgs 是 wait 命令的参数。
type ShWaitArgs struct {
	JobID string
	Wait  time.Duration
}

// ShKillArgs 是 kill 命令的参数。
type ShKillArgs struct {
	JobID string
}

// ShSetAllowlistArgs 是 set_allowlist 命令的参数。
// 传入允许执行的子命令前缀列表(精确字符串匹配,空表示允许任意)。
type ShSetAllowlistArgs struct {
	Allowed []string
}

// ShAllowlist 是 get_allowlist 的返回结构。
type ShAllowlist struct {
	Shell   string
	Allowed []string
}

// ShAbility 在 CmdAbility 之上提供 "sh -c <command>" 的高层抽象。
// 它自身不直接调用 os/exec,而是把单字符串命令翻译为 CmdAbility 调用,
// 避免重复实现任务管理、allowlist、并发限制等逻辑。
type ShAbility struct {
	mu        sync.RWMutex
	cmd       *CmdAbility
	shell     string
	allowlist map[string]struct{} // 允许的命令前缀,空表示不限制
}

func NewShAbility() *ShAbility {
	s := &ShAbility{
		cmd:       NewCmdAbility(),
		shell:     "sh",
		allowlist: make(map[string]struct{}),
	}
	s.refreshUnderlyingAllowlist()
	return s
}

// NewShAbilityWithCmd 允许外部注入 CmdAbility(便于复用同一个底层执行器)。
func NewShAbilityWithCmd(cmd *CmdAbility) *ShAbility {
	if cmd == nil {
		cmd = NewCmdAbility()
	}
	s := &ShAbility{
		cmd:       cmd,
		shell:     "sh",
		allowlist: make(map[string]struct{}),
	}
	s.refreshUnderlyingAllowlist()
	return s
}

func (s *ShAbility) GetName() string { return "ShAbility" }

func (s *ShAbility) Describe() string {
	return "ShAbility封装CmdAbility,提供 sh -c <command> 形式的单字符串命令执行,支持子命令 allowlist 管控。"
}

func (s *ShAbility) Check(atom *types.Atom) error {
	if atom == nil {
		return types.ErrMissingDependency
	}
	if _, ok := atom.Data("BaseData"); !ok {
		return types.ErrMissingDependency
	}
	return nil
}

func (s *ShAbility) Mount(atom *types.Atom) error { return s.Check(atom) }

// SetShell 设置底层 shell 名称(供跨平台测试或用户自定义 shell)。
func (s *ShAbility) SetShell(name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	s.mu.Lock()
	s.shell = name
	s.mu.Unlock()
	s.refreshUnderlyingAllowlist()
}

// SetAllowed 设置子命令 allowlist。空切片表示不限制。
func (s *ShAbility) SetAllowed(allowed []string) {
	set := make(map[string]struct{}, len(allowed))
	for _, a := range allowed {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		set[a] = struct{}{}
	}
	s.mu.Lock()
	s.allowlist = set
	s.mu.Unlock()
	s.refreshUnderlyingAllowlist()
}

func (s *ShAbility) refreshUnderlyingAllowlist() {
	s.mu.RLock()
	shell := s.shell
	hasAllow := len(s.allowlist) > 0
	s.mu.RUnlock()
	// 始终允许 sh -c 调用;子命令约束通过 ShAbility 自己的匹配检查来强制。
	s.cmd.SetAllowlist([]CmdAllowlistEntry{
		{Name: shell, ArgsPrefix: []string{"-c"}, MaxArgs: 2},
	})
	_ = hasAllow
}

// hasShellChaining 检测命令中"引号外"是否含 shell 链接/重定向/替换元字符。
// allowlist 非空时用于拒绝复合命令: 首词白名单可被 "printf x; touch f" 之类
// 的分号/管道/重定向绕过, 必须整体拒绝以保持白名单语义。
// 单引号内一切为字面量; 双引号内除 \$ ` \ 外的字符为字面量, 而这三者
// 可构成命令替换/转义, 故同样拦截。
func hasShellChaining(cmd string) string {
	quoted := byte(0)
	esc := false
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		if esc {
			esc = false
			continue
		}
		if quoted == '\'' {
			if c == '\'' {
				quoted = 0
			}
			continue
		}
		if quoted == '"' {
			switch c {
			case '\\':
				esc = true
			case '"':
				quoted = 0
			case '$', '`':
				return string(c)
			}
			continue
		}
		switch c {
		case '\'', '"':
			quoted = c
		case ';', '&', '|', '<', '>', '`', '$', '(', ')', '{', '}':
			return string(c)
		case '\\':
			// 引号外的 \x 是字面量转义 (如 '\'' 序列、转义的分号/空格),
			// 并不构成命令链接; 跳过下一个字符避免误报。
			i++
		case '\n':
			return "newline"
		}
	}
	return ""
}

func (s *ShAbility) matchInner(cmd string) error {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return fmt.Errorf("empty command: %w", types.ErrInvalidArguments)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.allowlist) == 0 {
		return nil
	}
	// 提取首词作为子命令名
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return fmt.Errorf("no command word: %w", types.ErrInvalidArguments)
	}
	head := fields[0]
	if _, ok := s.allowlist[head]; ok {
		// 首词命中白名单还不够: 命令其余部分含 shell 链接元字符即可绕过白名单
		if ch := hasShellChaining(cmd); ch != "" {
			return fmt.Errorf("command contains shell metacharacter %q after allowlisted subcommand: %w", ch, types.ErrInvalidArguments)
		}
		return nil
	}
	return fmt.Errorf("subcommand %q not allowed: %w", head, types.ErrInvalidArguments)
}

func (s *ShAbility) Command(atom *types.Atom, act string, args any) types.CommandOutput {
	if err := s.Check(atom); err != nil {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, err)}
	}
	switch act {
	case ShCommandSetAllowlist:
		typed, ok := args.(ShSetAllowlistArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		s.SetAllowed(typed.Allowed)
		return types.CommandOutput{Name: act, Value: s.snapshotAllowlist()}
	case ShCommandGetAllowlist:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: s.snapshotAllowlist()}
	case ShCommandRun:
		typed, ok := args.(ShRunArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		if err := s.matchInner(typed.Command); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, err)}
		}
		s.mu.RLock()
		shell := s.shell
		s.mu.RUnlock()
		return s.cmd.Command(atom, CmdCommandRun, CmdRunArgs{
			Name:    shell,
			Args:    []string{"-c", typed.Command},
			Timeout: typed.Timeout,
			Env:     typed.Env,
		})
	case ShCommandStart:
		typed, ok := args.(ShRunArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		if err := s.matchInner(typed.Command); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, err)}
		}
		s.mu.RLock()
		shell := s.shell
		s.mu.RUnlock()
		return s.cmd.Command(atom, CmdCommandStart, CmdRunArgs{
			Name:    shell,
			Args:    []string{"-c", typed.Command},
			Timeout: typed.Timeout,
			Env:     typed.Env,
		})
	case ShCommandWait:
		typed, ok := args.(ShWaitArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		return s.cmd.Command(atom, CmdCommandWait, CmdWaitArgs{JobID: typed.JobID, Wait: typed.Wait})
	case ShCommandKill:
		typed, ok := args.(ShKillArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		return s.cmd.Command(atom, CmdCommandKill, CmdKillArgs{JobID: typed.JobID})
	case ShCommandList:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		return s.cmd.Command(atom, CmdCommandList, nil)
	}
	return types.CommandOutput{Name: act, Err: fmt.Errorf("command %s: %w", act, types.ErrUnsupportedCommand)}
}

func (s *ShAbility) snapshotAllowlist() ShAllowlist {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.allowlist))
	for k := range s.allowlist {
		out = append(out, k)
	}
	sort.Strings(out)
	return ShAllowlist{Shell: s.shell, Allowed: out}
}
