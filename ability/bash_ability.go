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
)

// BashAbility 是 ShAbility 的特化:把单条命令包装为 "bash --noprofile --norc -c <cmd>",
// 再交由 ShAbility 实际执行。这样既复用了 ShAbility 的任务管理,又保证使用 bash
// 语义并避免读取用户登录配置。
//
// allowlist 语义:BashAbility 自身维护一份子命令白名单,只匹配用户原始命令的首词;
// ShAbility 的 allowlist 始终只允许 "bash" 这一项(通过 SetShell + SetAllowed 注入),
// 避免重复检查与首词被包装字符串替换的问题。
type BashAbility struct {
	sh        *ShAbility
	mu        sync.RWMutex
	allowlist map[string]struct{}
}

func NewBashAbility() *BashAbility {
	b := &BashAbility{sh: NewShAbility(), allowlist: make(map[string]struct{})}
	b.syncUnderlyingAllowlist()
	return b
}

func (b *BashAbility) GetName() string { return "BashAbility" }

func (b *BashAbility) Describe() string {
	return "BashAbility是ShAbility的bash特化:把命令包装为 'bash --noprofile --norc -c <cmd>' 后执行,避免读取用户登录配置。"
}

func (b *BashAbility) Check(atom *types.Atom) error { return b.sh.Check(atom) }

func (b *BashAbility) Mount(atom *types.Atom) error { return b.sh.Mount(atom) }

// SetAllowed 设置用户命令首词 allowlist(空表示不限制)。
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
	b.mu.Unlock()
	b.syncUnderlyingAllowlist()
}

func (b *BashAbility) syncUnderlyingAllowlist() {
	// ShAbility 始终只允许 "bash"(由 wrapForBash 注入),用户级 allowlist 由 BashAbility 自行检查。
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
	if len(b.allowlist) == 0 {
		return nil
	}
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return fmt.Errorf("no command word: %w", types.ErrInvalidArguments)
	}
	if _, ok := b.allowlist[fields[0]]; !ok {
		return fmt.Errorf("subcommand %q not allowed: %w", fields[0], types.ErrInvalidArguments)
	}
	return nil
}

func (b *BashAbility) Command(atom *types.Atom, act string, args any) types.CommandOutput {
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

func (b *BashAbility) snapshotAllowlist(atom *types.Atom) types.CommandOutput {
	b.mu.RLock()
	out := make([]string, 0, len(b.allowlist))
	for k := range b.allowlist {
		out = append(out, k)
	}
	sort.Strings(out)
	b.mu.RUnlock()
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
