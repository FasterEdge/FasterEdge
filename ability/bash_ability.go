// ─────────────────────────────────────────────────────────────
// FasterEdge 开源项目
// Github: https://github.com/FasterEdge
// Gitee:  https://gitee.com/FasterEdge
// ─────────────────────────────────────────────────────────────
package ability

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/FasterEdge/FasterEdge/types"
)

const (
	BashCommandRun          = "run"
	BashCommandStart        = "start"
	BashCommandWait         = "wait"
	BashCommandKill         = "kill"
	BashCommandList         = "list"
	BashCommandSetAllowlist = "set_allowlist"
	BashCommandGetAllowlist = "get_allowlist"
	BashCommandSetAllowAny  = "set_allowlist_any"
)

// BashAbility 是 ShAbility 的特化:把单条命令包装为 "bash --noprofile --norc -c <cmd>",
// 再交由 ShAbility 实际执行。这样既复用了 ShAbility 的任务管理,又保证使用 bash
// 语义并避免读取用户登录配置。
//
// allowlist 语义:BashAbility 自身维护一份子命令白名单,只匹配用户原始命令的首词;
// ShAbility 的 allowlist 始终只允许 "bash" 这一项(通过 SetShell + SetAllowed 注入),
// 避免重复检查与首词被包装字符串替换的问题。
//
// 授权语义为 fail-closed: 白名单为空且未显式 SetAllowAny(true) 时拒绝一切执行
// (旧实现"空 allowlist = 不限制"是默认放行的授权洞)。
type BashAbility struct {
	sh        *ShAbility
	mu        sync.RWMutex
	allowlist map[string]struct{}
	allowAny  bool
}

func NewBashAbility() *BashAbility {
	b := &BashAbility{sh: NewShAbility(), allowlist: make(map[string]struct{})}
	b.syncUnderlyingAllowlist()
	return b
}

func (b *BashAbility) GetName() string { return "BashAbility" }

func (b *BashAbility) Describe() string {
	return "BashAbility是ShAbility的bash特化:把命令包装为 'bash --noprofile --norc -c <cmd>' 后执行,避免读取用户登录配置。授权语义为 fail-closed: allowlist 为空时拒绝一切执行,需显式 set_allowlist_any 打开全放行。"
}

func (b *BashAbility) Check(atom *types.Atom) error { return b.sh.Check(atom) }

func (b *BashAbility) Mount(atom *types.Atom) error { return b.sh.Mount(atom) }

// SetAllowed 设置用户命令首词 allowlist。空列表 = fail-closed: 拒绝一切执行;
// 需要全放行必须显式 SetAllowAny(true)。
func (b *BashAbility) SetAllowed(allowed []string) {
	set := make(map[string]struct{}, len(allowed))
	for _, a := range allowed {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		set[a] = struct{}{}
	}
	b.mu.Lock()
	b.allowlist = set
	b.allowAny = false
	b.mu.Unlock()
	b.syncUnderlyingAllowlist()
}

// SetAllowAny 显式打开/关闭"允许任意命令"的 opt-in。
// true 时 run/start 不再受用户命令白名单约束; false(默认)时回到 fail-closed。
func (b *BashAbility) SetAllowAny(any_ bool) {
	b.mu.Lock()
	b.allowAny = any_
	if !any_ {
		b.allowlist = make(map[string]struct{})
	}
	b.mu.Unlock()
	b.syncUnderlyingAllowlist()
}

// AllowAny 返回当前是否处于显式"允许任意命令"模式。
func (b *BashAbility) AllowAny() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.allowAny
}

func (b *BashAbility) syncUnderlyingAllowlist() {
	// ShAbility 始终只允许 "bash"(由 wrapForBash 注入),用户级 allowlist 由 BashAbility 自行检查。
	// allowAny 时同步把底层也设为显式全放行(否则底层 ShAbility/CmdAbility 的
	// fail-closed 会在下一跳再拒一次, opt-in 传不下去)。
	b.mu.RLock()
	any_ := b.allowAny
	b.mu.RUnlock()
	if any_ {
		b.sh.SetAllowAny(true)
		return
	}
	b.sh.SetAllowed([]string{"bash"})
}

// Sh 返回底层 ShAbility,用于在更细粒度上自定义(慎用)。
func (b *BashAbility) Sh() *ShAbility { return b.sh }

func (b *BashAbility) checkInner(cmd string) error {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return fmt.Errorf("empty command: %w", types.ErrInvalidArguments)
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	// fail-closed: 白名单为空且未显式 opt-in 时拒绝一切执行
	// (旧实现"空 allowlist = 不限制"是默认放行的授权洞)。
	if !b.allowAny && len(b.allowlist) == 0 {
		return fmt.Errorf("%w: 需要先 set_allowlist 或显式 SetAllowAny(true): %w", ErrAllowlistEmpty, types.ErrInvalidArguments)
	}
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return fmt.Errorf("no command word: %w", types.ErrInvalidArguments)
	}
	if !b.allowAny {
		if _, ok := b.allowlist[fields[0]]; !ok {
			return fmt.Errorf("subcommand %q not allowed: %w", fields[0], types.ErrInvalidArguments)
		}
	}
	// 与 ShAbility 同理: 首词命中后仍须拒绝 shell 链接元字符, 防白名单绕过
	if ch := hasShellChaining(cmd); ch != "" {
		return fmt.Errorf("command contains shell metacharacter %q after allowlisted subcommand: %w", ch, types.ErrInvalidArguments)
	}
	return nil
}

func (b *BashAbility) Command(atom *types.Atom, act string, args any) types.CommandOutput {
	// 先做依赖检查(与 ShAbility 一致): 旧实现直接进入 checkInner, 缺依赖时
	// 只有"白名单为空即放行"的 fail-open 语义才会恰好把请求转发到 ShAbility
	// 从而间接触发 Check。fail-closed 之后 checkInner 会先拒绝, 因此这里显式
	// 前置依赖校验, 让缺失依赖与授权拒绝两个错误原因保持可区分。
	if err := b.Check(atom); err != nil {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, err)}
	}
	switch act {
	case BashCommandSetAllowlist:
		typed, ok := args.(ShSetAllowlistArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		b.SetAllowed(typed.Allowed)
		return b.snapshotAllowlist(atom)
	case BashCommandGetAllowlist:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		return b.snapshotAllowlist(atom)
	case BashCommandSetAllowAny:
		typed, ok := args.(CmdSetAllowAnyArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		b.SetAllowAny(typed.Any)
		return types.CommandOutput{Name: act, Value: b.AllowAny()}
	case BashCommandRun, BashCommandStart:
		typed, ok := args.(ShRunArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		if err := b.checkInner(typed.Command); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, err)}
		}
		typed.Command = wrapForBash(typed.Command)
		if act == BashCommandRun {
			return b.sh.Command(atom, ShCommandRun, typed)
		}
		return b.sh.Command(atom, ShCommandStart, typed)
	case BashCommandWait, BashCommandKill, BashCommandList:
		return b.sh.Command(atom, mapBashCommand(act), args)
	}
	return types.CommandOutput{Name: act, Err: fmt.Errorf("command %s: %w", act, types.ErrUnsupportedCommand)}
}

// bashAbilityCommands 是 BashAbility 的命令目录, 与 Command 的 case 分支一一对应。
//
// 继承语义(由 TestShBashAbilityInheritanceSemantics 断言):
// BashAbility 逐层经 ShAbility→CmdAbility 拿到执行能力, 但它自己的 Command 只暴露
// 上述 8 个 act; run/start 在转发前把命令重写为 "bash --noprofile --norc -c <cmd>",
// wait/kill/list 原样透传。祖先组件目录中不被本组件转发的 act(get_job / clear_finished)
// 一概不出现。
var bashAbilityCommands = []string{
	BashCommandRun,
	BashCommandStart,
	BashCommandWait,
	BashCommandKill,
	BashCommandList,
	BashCommandSetAllowlist,
	BashCommandGetAllowlist,
	BashCommandSetAllowAny,
}

// bashCommandsDelegatedToSh 记录 BashAbility 直接转发给 ShAbility 的 act 集合。
var bashCommandsDelegatedToSh = []string{ShCommandWait, ShCommandKill, ShCommandList}

// ListCommands satisfies types.CommandLister.
func (b *BashAbility) ListCommands() []string { return append([]string(nil), bashAbilityCommands...) }

// Compile-time guarantee that BashAbility declares its command catalogue.
var _ types.CommandLister = (*BashAbility)(nil)

func (b *BashAbility) snapshotAllowlist(atom *types.Atom) types.CommandOutput {
	b.mu.RLock()
	out := make([]string, 0, len(b.allowlist))
	for k := range b.allowlist {
		out = append(out, k)
	}
	any_ := b.allowAny
	b.mu.RUnlock()
	sort.Strings(out)
	if any_ {
		return types.CommandOutput{Name: BashCommandGetAllowlist, Value: ShAllowlist{Shell: "bash", Allowed: []string{"*"}, AllowAny: true}}
	}
	return types.CommandOutput{Name: BashCommandGetAllowlist, Value: ShAllowlist{Shell: "bash", Allowed: out}}
}

// wrapForBash 把单条命令包装为 `bash --noprofile --norc -c <cmd>`,以避免在 ShAbility
// 内部更换 shell 时丢失 bash 特性。注意:底层仍由 ShAbility 启动一个 sh,这个 sh 再 fork 出 bash。
func wrapForBash(cmd string) string {
	return "bash --noprofile --norc -c " + shellQuote(cmd)
}

// shellQuote 用单引号包裹字符串,并把内嵌单引号替换为 '\”' 序列,POSIX 兼容。
func shellQuote(s string) string {
	const q = byte('\'')
	out := make([]byte, 0, len(s)+2)
	out = append(out, q)
	for i := 0; i < len(s); i++ {
		if s[i] == q {
			out = append(out, "'\\''"...)
		} else {
			out = append(out, s[i])
		}
	}
	out = append(out, q)
	return string(out)
}

func mapBashCommand(act string) string {
	switch act {
	case BashCommandWait:
		return ShCommandWait
	case BashCommandKill:
		return ShCommandKill
	case BashCommandList:
		return ShCommandList
	}
	return act
}
