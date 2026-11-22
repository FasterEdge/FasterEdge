// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package ability

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/FasterEdge/FasterEdge/data"
	"github.com/FasterEdge/FasterEdge/types"
)

// 本文件守护"命令契约自描述": 每个能力的 ListCommands() 必须与它 Command()
// 里真实的 switch act 分派分支逐一对应, 不多报(调不通的命令进目录是谎言)、
// 不漏报(Atom.CommandNames 是外部唯一可机器读取的能力清单)。
// 校验不靠人工维护的第二份清单: 分派分支直接从源码 AST 读出后比对。

func abilitySourceDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(file)
}

// dispatchCaseLabels 读取 srcFile 中 Command 方法体内顶层 `switch act` 的全部
// case 标签(源码顺序, 不含 default)。标签一律是包级常量标识符, 因此这里取的是
// 标识符名——与 ListCommands 里的目录元素处在同一符号层, 常量取值前后不一致
// 也能被发现(两边都会变)。
func dispatchCaseLabels(t *testing.T, srcFile string) []string {
	t.Helper()
	path := filepath.Join(abilitySourceDir(t), srcFile)
	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", srcFile, err)
	}
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Command" || fn.Recv == nil || fn.Body == nil {
			continue
		}
		// 只认函数体直接子语句里的 switch: upload/download 的 case 内部还有一个
		// 嵌套 `switch act`, 它不是分派表本身。
		for _, stmt := range fn.Body.List {
			sw, ok := stmt.(*ast.SwitchStmt)
			if !ok || sw.Tag == nil {
				continue
			}
			tag, ok := sw.Tag.(*ast.Ident)
			if !ok || tag.Name != "act" {
				continue
			}
			var labels []string
			for _, clause := range sw.Body.List {
				cc, ok := clause.(*ast.CaseClause)
				if !ok {
					continue
				}
				for _, expr := range cc.List {
					switch v := expr.(type) {
					case *ast.Ident:
						labels = append(labels, v.Name)
					case *ast.BasicLit:
						labels = append(labels, strings.Trim(v.Value, `"`))
					default:
						t.Fatalf("%s: unsupported case label %T", srcFile, expr)
					}
				}
			}
			if len(labels) == 0 {
				t.Fatalf("%s: `switch act` has no case labels", srcFile)
			}
			return labels
		}
	}
	t.Fatalf("%s: no top-level `switch act` found in Command", srcFile)
	return nil
}

// packageStringConsts 解析 ability 包内所有非测试源文件的包级 string 常量,
// 返回 常量名 → 值。用于把 AST 里的 case 标识符翻译成实际命令字符串,
// 从而与 ListCommands() 的返回值(字符串)比对。
func packageStringConsts(t *testing.T) map[string]string {
	t.Helper()
	dir := abilitySourceDir(t)
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	values := map[string]string{}
	var pending [][2]string // (name, expr ident), 需要二次解析
	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, decl := range parsed.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					switch v := vs.Values[i].(type) {
					case *ast.BasicLit:
						if v.Kind == token.STRING {
							values[name.Name] = strings.Trim(v.Value, "`\"")
						}
					case *ast.Ident:
						pending = append(pending, [2]string{name.Name, v.Name})
					}
				}
			}
		}
	}
	// 常量可以引用同包其它常量, 迭代到不动点。
	for pass := 0; pass < 8; pass++ {
		progressed := false
		remaining := pending[:0]
		for _, p := range pending {
			if val, ok := values[p[1]]; ok {
				values[p[0]] = val
				progressed = true
				continue
			}
			remaining = append(remaining, p)
		}
		pending = remaining
		if !progressed {
			break
		}
	}
	return values
}

// dispatchCommandStrings 返回 srcFile 中 Command 的 `switch act` 全部分支的
// 实际命令字符串(源码顺序, 不含 default)。标识符 case 标签经包内常量表求值;
// 查不到值即视为测试基础设施缺陷, 直接失败而不是静默跳过。
func dispatchCommandStrings(t *testing.T, srcFile string) []string {
	t.Helper()
	consts := packageStringConsts(t)
	labels := dispatchCaseLabels(t, srcFile)
	out := make([]string, 0, len(labels))
	for _, label := range labels {
		val, ok := consts[label]
		if !ok {
			// 也可能是直接被写成字符串字面量的 case
			if !isIdentifier(label) {
				out = append(out, label)
				continue
			}
			t.Fatalf("%s: cannot resolve constant %q to a string value", srcFile, label)
		}
		out = append(out, val)
	}
	return out
}

func isIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func cmdCatalogAtom(t *testing.T) *types.Atom { return catalogBaseAtom(t) }

func catalogBaseAtom(t *testing.T) *types.Atom {
	t.Helper()
	a := &types.Atom{}
	if err := a.AddData(&data.BaseData{}); err != nil {
		t.Fatal(err)
	}
	return a
}

func catalogConfigAtom(t *testing.T) *types.Atom {
	t.Helper()
	a := catalogBaseAtom(t)
	if err := a.AddData(data.NewConfigData()); err != nil {
		t.Fatal(err)
	}
	return a
}

// catalogNetMapAtom 提供 FileTransfer/AlgorithmDistribution 依赖的 NetMapData
// 与 NetMapAbility(后者是 FileTransfer 解析对端所必需)。
func catalogNetMapAtom(t *testing.T) *types.Atom {
	t.Helper()
	a := catalogBaseAtom(t)
	if err := a.AddData(data.NewNetMapData()); err != nil {
		t.Fatal(err)
	}
	if err := a.AddAbility(NewNetMapAbility()); err != nil {
		t.Fatal(err)
	}
	if err := a.AddAbility(NewFileTransferAbility()); err != nil {
		t.Fatal(err)
	}
	return a
}

type commandCatalogCase struct {
	name   string
	src    string // 声明 Command 分派的源文件
	atom   func(*testing.T) *types.Atom
	lister catalogedComponent
	// want 是"对外承诺"的目录(顺序即 ListCommands 的返回顺序)。
	// 刻意手写而非从实现里取: 常量被误删/误改时这条断言才会响。
	want []string
}

// catalogedComponent 聚合 Component(分派契约) 与 CommandLister(目录契约),
// 使测试可以同时调用 Command 与 ListCommands。
type catalogedComponent interface {
	types.Component
	types.CommandLister
}

func commandCatalogTable() []commandCatalogCase {
	return []commandCatalogCase{
		{
			name:   "CmdAbility",
			src:    "cmd_ability.go",
			atom:   cmdCatalogAtom,
			lister: NewCmdAbility(),
			want: []string{
				CmdCommandRun, CmdCommandStart, CmdCommandWait, CmdCommandKill, CmdCommandList,
				CmdCommandSetAllowlist, CmdCommandGetAllowlist, CmdCommandSetAllowAny,
				CmdCommandClearJobs, CmdCommandGetJob,
			},
		},
		{
			name:   "ShAbility",
			src:    "sh_ability.go",
			atom:   cmdCatalogAtom,
			lister: NewShAbility(),
			want: []string{
				ShCommandRun, ShCommandStart, ShCommandWait, ShCommandKill, ShCommandList,
				ShCommandSetAllowlist, ShCommandGetAllowlist, ShCommandSetAllowAny,
			},
		},
		{
			name:   "BashAbility",
			src:    "bash_ability.go",
			atom:   cmdCatalogAtom,
			lister: NewBashAbility(),
			want: []string{
				BashCommandRun, BashCommandStart, BashCommandWait, BashCommandKill, BashCommandList,
				BashCommandSetAllowlist, BashCommandGetAllowlist, BashCommandSetAllowAny,
			},
		},
		{
			name:   "ConfigFileAbility",
			src:    "config_file_ability.go",
			atom:   catalogConfigAtom,
			lister: NewConfigFileAbility(),
			want: []string{
				ConfigFileCommandSetPath, ConfigFileCommandGetPath, ConfigFileCommandExists,
				ConfigFileCommandLoad, ConfigFileCommandSave,
			},
		},
		{
			name:   "FileTransferAbility",
			src:    "file_transfer_ability.go",
			atom:   catalogNetMapAtom,
			lister: NewFileTransferAbility(),
			want: []string{
				FileTransferCommandSetTarget, FileTransferCommandGetTarget,
				FileTransferCommandUpload, FileTransferCommandDownload,
				FileTransferCommandList, FileTransferCommandGet,
				FileTransferCommandCancel, FileTransferCommandClearFinished,
			},
		},
		{
			name:   "AlgorithmDistributionAbility",
			src:    "algorithm_distribution_ability.go",
			atom:   catalogNetMapAtom,
			lister: NewAlgDistAbility(),
			want: []string{
				AlgDistCommandRegister, AlgDistCommandUnregister, AlgDistCommandList,
				AlgDistCommandGet, AlgDistCommandDistribute, AlgDistCommandCancel,
				AlgDistCommandListDistribute, AlgDistCommandClearFinished,
			},
		},
	}
}

// TestCommandCatalogDeclaredExactOrder 守护目录内容与顺序(对外承诺清单)。
func TestCommandCatalogDeclaredExactOrder(t *testing.T) {
	for _, c := range commandCatalogTable() {
		t.Run(c.name, func(t *testing.T) {
			got := c.lister.ListCommands()
			if len(got) != len(c.want) {
				t.Fatalf("ListCommands() = %v, want %v", got, c.want)
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Fatalf("ListCommands()[%d] = %q, want %q (full: %v)", i, got[i], c.want[i], got)
				}
			}
			seen := make(map[string]struct{}, len(got))
			for _, act := range got {
				if act == "" {
					t.Fatalf("%s: empty command name in %v", c.name, got)
				}
				if _, dup := seen[act]; dup {
					t.Fatalf("%s: duplicate command %q in %v", c.name, act, got)
				}
				seen[act] = struct{}{}
			}
		})
	}
}

// TestCommandCatalogMatchesDispatch 是本任务的核心断言:
// ListCommands() 的集合 == Command 的 switch act 分支集合(双向比对, 忽略顺序),
// 分派表直接来自源码 AST, 不依赖人工同步的第二份 list。
func TestCommandCatalogMatchesDispatch(t *testing.T) {
	for _, c := range commandCatalogTable() {
		t.Run(c.name, func(t *testing.T) {
			declared := c.lister.ListCommands()
			dispatched := dispatchCommandStrings(t, c.src)
			if len(dispatched) != len(declared) {
				t.Fatalf("%s: %d dispatch branches %v vs %d declared %v",
					c.name, len(dispatched), dispatched, len(declared), declared)
			}
			declaredSet := make(map[string]struct{}, len(declared))
			for _, act := range declared {
				declaredSet[act] = struct{}{}
			}
			for _, act := range dispatched {
				if _, ok := declaredSet[act]; !ok {
					t.Fatalf("%s: dispatch case %q missing from ListCommands %v", c.name, act, declared)
				}
				delete(declaredSet, act)
			}
			if len(declaredSet) != 0 {
				extra := make([]string, 0, len(declaredSet))
				for act := range declaredSet {
					extra = append(extra, act)
				}
				sort.Strings(extra)
				t.Fatalf("%s: ListCommands declares %v which Command does not dispatch", c.name, extra)
			}
		})
	}
}

// TestCommandCatalogEntriesAreDispatchable 用行为验证目录不是空头支票:
// 目录里每个 act 都能进入分派(返回的不是 ErrUnsupportedCommand),
// 目录外的 act 必须被显式拒绝为 ErrUnsupportedCommand。
func TestCommandCatalogEntriesAreDispatchable(t *testing.T) {
	// 传一个类型不匹配的非 nil 参数: 所有分支都会在类型断言处返回
	// ErrInvalidArguments, 因此既证明"进入了该分支", 又不会真的执行任何副作用。
	probe := struct{ Unrelated string }{Unrelated: "probe"}
	for _, c := range commandCatalogTable() {
		t.Run(c.name, func(t *testing.T) {
			atom := c.atom(t)
			for _, act := range c.lister.ListCommands() {
				out := c.lister.Command(atom, act, probe)
				if errors.Is(out.Err, types.ErrUnsupportedCommand) {
					t.Fatalf("%s: listed command %q is not dispatched: %v", c.name, act, out.Err)
				}
				if out.Name != act {
					t.Fatalf("%s: command %q echoed name %q", c.name, act, out.Name)
				}
			}
			for _, act := range []string{"", "nope", "list_commands", "RUN"} {
				out := c.lister.Command(atom, act, nil)
				if !errors.Is(out.Err, types.ErrUnsupportedCommand) {
					t.Fatalf("%s: unknown command %q should be unsupported, got %v", c.name, act, out.Err)
				}
			}
		})
	}
}

// TestCommandCatalogReturnsCopy 返回值必须是副本: 调用方改写切片不得污染目录。
func TestCommandCatalogReturnsCopy(t *testing.T) {
	for _, c := range commandCatalogTable() {
		t.Run(c.name, func(t *testing.T) {
			first := c.lister.ListCommands()
			original := append([]string(nil), first...)
			for i := range first {
				first[i] = "mutated"
			}
			first = append(first, "appended")
			second := c.lister.ListCommands()
			if len(second) != len(original) {
				t.Fatalf("length changed after caller mutation: %v", second)
			}
			for i := range original {
				if second[i] != original[i] {
					t.Fatalf("ListCommands leaked internal storage at %d: %q vs %q", i, second[i], original[i])
				}
			}
		})
	}
}

// TestShBashAbilityInheritanceSemantics 明确固化继承语义, 这是本组能力最容易
// 被误解的地方: ShAbility 复用 CmdAbility 的执行器, 但只转发 run/start/wait/
// kill/list; set_allowlist/get_allowlist 被替换为"子命令白名单"语义;
// CmdAbility 的 clear_finished/get_job 完全没有转发。BashAbility 与 ShAbility
// 暴露面相同。目录必须如实反映这一点, 不能因为是"子组件"就整份继承父目录。
func TestShBashAbilityInheritanceSemantics(t *testing.T) {
	atom := cmdCatalogAtom(t)
	toSet := func(list []string) map[string]struct{} {
		s := make(map[string]struct{}, len(list))
		for _, v := range list {
			s[v] = struct{}{}
		}
		return s
	}

	cmdList := NewCmdAbility().ListCommands()
	shList := NewShAbility().ListCommands()
	bashList := NewBashAbility().ListCommands()

	// 1) 父组件真正被复用的 act 必须在子组件目录里
	shSet := toSet(shList)
	for _, act := range shCommandsDelegatedToCmd {
		if _, ok := shSet[act]; !ok {
			t.Fatalf("ShAbility must expose delegated CmdAbility command %q, got %v", act, shList)
		}
	}
	bashSet := toSet(bashList)
	for _, act := range bashCommandsDelegatedToSh {
		if _, ok := bashSet[act]; !ok {
			t.Fatalf("BashAbility must expose delegated ShAbility command %q, got %v", act, bashList)
		}
	}

	// 2) 父组件独有的 act 不得出现在子组件目录里, 且确实调不通
	cmdSet := toSet(cmdList)
	for _, act := range shCommandsDelegatedToCmd {
		delete(cmdSet, act)
	}
	// 剩余 = CmdAbility 有但 ShAbility 未转发的命令(set_allowlist/get_allowlist/
	// set_allowlist_any 已由 ShAbility 自己重新定义为子命令白名单语义,
	// 因此从差集中剔除)。
	for _, redefined := range []string{CmdCommandSetAllowlist, CmdCommandGetAllowlist, CmdCommandSetAllowAny} {
		delete(cmdSet, redefined)
	}
	if len(cmdSet) == 0 {
		t.Fatal("expected CmdAbility-only commands to exist (clear_finished/get_job)")
	}
	for act := range cmdSet {
		if _, ok := shSet[act]; ok {
			t.Fatalf("ShAbility must not inherit CmdAbility command %q", act)
		}
		if out := NewShAbility().Command(atom, act, CmdJobIDArg{JobID: "job-1"}); !errors.Is(out.Err, types.ErrUnsupportedCommand) {
			t.Fatalf("ShAbility: non-inherited command %q should be unsupported, got %v", act, out.Err)
		}
		if _, ok := bashSet[act]; ok {
			t.Fatalf("BashAbility must not inherit ancestor command %q", act)
		}
		if out := NewBashAbility().Command(atom, act, CmdJobIDArg{JobID: "job-1"}); !errors.Is(out.Err, types.ErrUnsupportedCommand) {
			t.Fatalf("BashAbility: non-inherited command %q should be unsupported, got %v", act, out.Err)
		}
	}

	// 3) 子组件不得反向凭空多报父组件没有的命令
	for _, act := range shList {
		if _, ok := toSet(cmdList)[act]; !ok {
			t.Fatalf("ShAbility declares %q which CmdAbility does not have", act)
		}
	}
	for _, act := range bashList {
		if _, ok := shSet[act]; !ok {
			t.Fatalf("BashAbility declares %q which ShAbility does not have", act)
		}
	}
}

// silentData 是一个只满足 Component 契约、不实现 CommandLister 的 data 组件,
// 用于断言 Atom.CommandNames 对"未声明目录"的组件仍保留键(空切片), 与
// "未注册"可区分。不能用仓库自带的 data 组件——它们都实现了 CommandLister。
type silentData struct{}

func (silentData) GetName() string         { return "SilentData" }
func (silentData) Describe() string        { return "test-only data without command catalogue" }
func (silentData) Check(*types.Atom) error { return nil }
func (silentData) Mount(*types.Atom) error { return nil }
func (silentData) Command(*types.Atom, string, any) types.CommandOutput {
	return types.CommandOutput{}
}

// TestCommandCatalogSurfacedByAtomCommandNames 验证目录经 Atom.CommandNames
// 对外可见(这是 ListCommands 唯一的框架调用点), 且未声明目录的组件保持空切片。
func TestCommandCatalogSurfacedByAtomCommandNames(t *testing.T) {
	atom := catalogConfigAtom(t)
	if err := atom.AddData(silentData{}); err != nil {
		t.Fatal(err)
	}
	if err := atom.AddAbility(NewCmdAbility()); err != nil {
		t.Fatal(err)
	}
	if err := atom.AddAbility(NewShAbility()); err != nil {
		t.Fatal(err)
	}
	if err := atom.AddAbility(NewBashAbility()); err != nil {
		t.Fatal(err)
	}
	if err := atom.AddAbility(NewConfigFileAbility()); err != nil {
		t.Fatal(err)
	}
	if err := atom.AddData(data.NewNetMapData()); err != nil {
		t.Fatal(err)
	}
	if err := atom.AddAbility(NewNetMapAbility()); err != nil {
		t.Fatal(err)
	}
	if err := atom.AddAbility(NewFileTransferAbility()); err != nil {
		t.Fatal(err)
	}
	if err := atom.AddAbility(NewAlgDistAbility()); err != nil {
		t.Fatal(err)
	}

	names := atom.CommandNames()
	for _, c := range commandCatalogTable() {
		got, ok := names[c.name]
		if !ok {
			t.Fatalf("CommandNames missing %s", c.name)
		}
		want := append([]string(nil), c.lister.ListCommands()...)
		sort.Strings(want) // Atom.CommandNames 对每个组件的目录排序后返回
		if len(got) != len(want) {
			t.Fatalf("%s: CommandNames = %v, want %v", c.name, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s: CommandNames = %v, want %v", c.name, got, want)
			}
		}
	}
	// silentData 未实现 CommandLister: 键存在但为空, 与"未注册"可区分。
	if got, ok := names["SilentData"]; !ok || len(got) != 0 {
		t.Fatalf("SilentData should be present with empty command list, got %v (present=%v)", got, ok)
	}
	if _, ok := names["NotRegistered"]; ok {
		t.Fatal("unregistered component must not appear in CommandNames")
	}
}
