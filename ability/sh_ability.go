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
	ShCommandSetAllowAny  = "set_allowlist_any"
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
// 传入允许执行的子命令前缀列表(精确字符串匹配)。空列表 = fail-closed,
// 拒绝一切执行; 需要全放行必须显式调用 SetAllowAny(true)(Go API)。
type ShSetAllowlistArgs struct {
	Allowed []string
}

// ShAllowlist 是 get_allowlist 的返回结构。
// AllowAny 为 true 表示已显式 opt-in 全放行(此时 Allowed 返回 ["*"]);
// AllowAny 为 false 且 Allowed 为空表示 fail-closed: 拒绝一切执行。
type ShAllowlist struct {
	Shell    string
	Allowed  []string
	AllowAny bool
}

// ShAbility 在 CmdAbility 之上提供 "sh -c <command>" 的高层抽象。
// 它自身不直接调用 os/exec,而是把单字符串命令翻译为 CmdAbility 调用,
// 避免重复实现任务管理、allowlist、并发限制等逻辑。
//
// 授权语义(fail-closed): allowlist 为空时 run/start 一律拒绝(ErrAllowlistEmpty),
// 不再等价于"不限制"; 需要全放行必须显式 SetAllowAny(true)。
type ShAbility struct {
	mu        sync.RWMutex
	cmd       *CmdAbility
	shell     string
	allowlist map[string]struct{} // 允许的命令前缀; 空 + allowAny=false 表示拒绝一切
	allowAny  bool                // 显式 opt-in: 全放行
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
	return "ShAbility封装CmdAbility,提供 sh -c <command> 形式的单字符串命令执行,支持子命令 allowlist 管控。授权语义为 fail-closed: allowlist 为空时拒绝一切执行,需显式 set_allowlist_any 打开全放行。"
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

// SetAllowed 设置子命令 allowlist。空切片表示 fail-closed: 拒绝一切执行;
// 需要全放行必须显式 SetAllowAny(true)。
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
	s.allowAny = false
	s.mu.Unlock()
	s.refreshUnderlyingAllowlist()
}

// SetAllowAny 显式打开/关闭"允许任意命令"的 opt-in。
// true 时 run/start 不再被子命令白名单拦截; false(默认)时回到 fail-closed。
func (s *ShAbility) SetAllowAny(any_ bool) {
	s.mu.Lock()
	s.allowAny = any_
	if !any_ {
		s.allowlist = make(map[string]struct{})
	}
	s.mu.Unlock()
	s.refreshUnderlyingAllowlist()
}

// AllowAny 返回当前是否处于显式"允许任意命令"模式。
func (s *ShAbility) AllowAny() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.allowAny
}

func (s *ShAbility) refreshUnderlyingAllowlist() {
	s.mu.RLock()
	shell := s.shell
	any_ := s.allowAny
	s.mu.RUnlock()
	// 始终允许 sh -c 调用;子命令约束通过 ShAbility 自己的匹配检查来强制。
	// allowAny 时同步把底层 CmdAbility 也设为显式全放行(否则空 allowlist 的
	// fail-closed 会在底层再拒一次, opt-in 传不下去)。
	if any_ {
		s.cmd.SetAllowAny(true)
		return
	}
	s.cmd.SetAllowlist([]CmdAllowlistEntry{
		{Name: shell, ArgsPrefix: []string{"-c"}, MaxArgs: 2},
	})
}

// hasShellChaining 检测命令中"引号外"是否含 shell 链接/重定向/替换元字符。
// allowlist 非空时用于拒绝复合命令: 首词白名单可被 "printf x; touch f" 之类
// 的分号/管道/重定向绕过, 必须整体拒绝以保持白名单语义。
// 单引号内一切为字面量; 双引号内除 \$ ` \ 外的字符为字面量, 而这三者
// 可构成命令替换/转义, 故同样拦截。
//
// 已知局限(诚实表述, 不谎称完备): 该扫描是引号感知的字符级检查, 不是 shell
// 解析器。已在拦截范围内的形态由 Test*RejectsExtendedChainingBypass 固定:
// 换行/回车等控制字符(引号外)、|& 、<(...) 与 >(...) 进程替换、$(...) 与
// 反引号替换、${...} 与 {a,b} 扩展、ANSI-C 引号 $'...'。
// 仍不识别(由调用方以收紧白名单或坚持 fail-closed 兜底)的仅有两类:
//   - 编码绕过: \x3b(分号) \u003c 之类反斜杠转义序列不被解码为元字符;
//   - 引号状态混淆: 双引号内的控制字符(换行/回车)按字面量放过。
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
			// 局限: \x3b 这类"转义后再解码为元字符"的形态此处不识别,
			// 由 TestShAbilityRejectsExtendedChainingBypass 固定当前行为并显式声明。
			i++
		case '\n':
			return "newline"
		case '\r':
			// 回车与 NUL 等控制字符: 不依赖具体 shell 是否把它们当作命令分隔符,
			// 白名单语义下按默认拒绝处理(避免 CR/LF 混用平台的语义漂移)。
			return "carriage return"
		default:
			if c < 0x20 || c == 0x7f {
				return "control character"
			}
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
	allowAny := s.allowAny
	defer s.mu.RUnlock()
	// fail-closed: 白名单为空且未显式 opt-in 时拒绝一切执行。
	// 旧实现"空 allowlist = 不限制"是一个默认放行的授权洞。
	if !allowAny && len(s.allowlist) == 0 {
		return fmt.Errorf("%w: 需要先 set_allowlist 或显式 SetAllowAny(true): %w", ErrAllowlistEmpty, types.ErrInvalidArguments)
	}
	// 提取首词作为子命令名
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return fmt.Errorf("no command word: %w", types.ErrInvalidArguments)
	}
	head := fields[0]
	if !allowAny {
		if _, ok := s.allowlist[head]; !ok {
			return fmt.Errorf("subcommand %q not allowed: %w", head, types.ErrInvalidArguments)
		}
	}
	// 首词命中白名单还不够: 命令其余部分含 shell 链接元字符即可绕过白名单
	if ch := hasShellChaining(cmd); ch != "" {
		return fmt.Errorf("command contains shell metacharacter %q after allowlisted subcommand: %w", ch, types.ErrInvalidArguments)
	}
	return nil
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
	case ShCommandSetAllowAny:
		typed, ok := args.(CmdSetAllowAnyArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		s.SetAllowAny(typed.Any)
		return types.CommandOutput{Name: act, Value: s.AllowAny()}
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

// shAbilityCommands 是 ShAbility 的命令目录, 与 Command 的 case 分支一一对应。
//
// 继承语义(显式声明, 由 TestShBashAbilityInheritanceSemantics 断言):
// ShAbility 在构造上持有 *CmdAbility 并复用它执行任务, 但 Command 只把 run/start/
// wait/kill/list 五个 act 委派下去, set_allowlist/get_allowlist 换成了自己的子命令
// 白名单语义, 而 CmdAbility 的 get_job / clear_finished 没有被转发——因此
// ListCommands 不得包含这两个父命令。谎报会让 Atom.CommandNames 导出一份调不通
// 的命令目录(Command 返回 ErrUnsupportedCommand)。
var shAbilityCommands = []string{
	ShCommandRun,
	ShCommandStart,
	ShCommandWait,
	ShCommandKill,
	ShCommandList,
	ShCommandSetAllowlist,
	ShCommandGetAllowlist,
	ShCommandSetAllowAny,
}

// shCommandsDelegatedToCmd 记录 ShAbility 真正转发给 CmdAbility 的 act 集合,
// 用于在测试里把"共享实现"与"暴露面"两件事分开断言。
var shCommandsDelegatedToCmd = []string{
	CmdCommandRun, CmdCommandStart, CmdCommandWait, CmdCommandKill, CmdCommandList,
}

// ListCommands satisfies types.CommandLister.
func (s *ShAbility) ListCommands() []string { return append([]string(nil), shAbilityCommands...) }

// Compile-time guarantee that ShAbility declares its command catalogue.
var _ types.CommandLister = (*ShAbility)(nil)

func (s *ShAbility) snapshotAllowlist() ShAllowlist {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.allowlist))
	for k := range s.allowlist {
		out = append(out, k)
	}
	sort.Strings(out)
	if s.allowAny {
		// 全放行是可观察状态: 用显式 AllowAny=true + Allowed=["*"] 表达,
		// 与"未配置(拒绝一切)"的空列表区分开。
		return ShAllowlist{Shell: s.shell, Allowed: []string{"*"}, AllowAny: true}
	}
	return ShAllowlist{Shell: s.shell, Allowed: out}
}
