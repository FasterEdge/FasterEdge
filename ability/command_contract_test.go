// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package ability

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"testing"

	"github.com/FasterEdge/FasterEdge/types"
)

// 本文件是"命令契约自描述"的守门测试: 断言每个组件的 ListCommands() 与
// 其 Command() 中 act 的 switch 分派分支集合**互相包含**(集合相等)。
//
// 分派分支一侧不靠手抄: contractDispatchCases 用 go/parser 直接解析组件源码,
// 取 Command 里以 act 为 tag 的 switch, 把 case 标签的常量标识符回查同文件
// 的 const 声明解析出字符串。因此新增分支却忘了登记命令目录(或反之)时,
// 测试会以分支级粒度失败, 而不是靠维护者记得同步两张表。

// commandContractCase 描述一个组件的命令契约核对项。
type commandContractCase struct {
	name      string // 组件名, 仅用于失败信息
	file      string // Command 分派所在的源文件(与本测试同目录)
	receiver  string // Command 方法接收者类型名
	lister    types.CommandLister
	commander types.Component
	atom      func(t *testing.T) *types.Atom
	probeArgs any // 探测入参: 必须能在产生任何副作用前被各分支拒绝(或本身无副作用)
}

func commandContractCases() []commandContractCase {
	return []commandContractCase{
		{
			name: "BaseAbility", file: "base_ability.go", receiver: "BaseAbility",
			lister: new(BaseAbility), commander: new(BaseAbility),
			atom: func(*testing.T) *types.Atom { return &types.Atom{} },
			// BaseAbility 的 Command 在 switch 之前就拒绝非 nil args,
			// 用非 nil 会让所有 act 都返回同样的 ErrInvalidArguments,
			// 无法区分"已分派"与"未支持", 故只能传 nil。
			probeArgs: nil,
		},
		{
			name: "RoleAbility", file: "role_ability.go", receiver: "RoleAbility",
			lister: new(RoleAbility), commander: new(RoleAbility),
			atom: func(*testing.T) *types.Atom { return &types.Atom{} },
		},
		{
			name: "CloudRoleAbility", file: "cloud_role_ability.go", receiver: "CloudRoleAbility",
			lister: NewCloudRoleAbility(), commander: NewCloudRoleAbility(),
			atom: newCloudRoleAtom,
		},
		{
			name: "EdgeRoleAbility", file: "edge_role_ability.go", receiver: "EdgeRoleAbility",
			lister: NewEdgeRoleAbility(), commander: NewEdgeRoleAbility(),
			atom: newEdgeRoleAtom,
		},
		{
			name: "NetMapAbility", file: "net_map_ability.go", receiver: "NetMapAbility",
			lister: NewNetMapAbility(), commander: NewNetMapAbility(),
			atom: newNetMapAtom,
		},
		{
			name: "OneKeyAbility", file: "one_key_ability.go", receiver: "OneKeyAbility",
			lister: NewOneKeyAbility(), commander: NewOneKeyAbility(),
			atom: func(t *testing.T) *types.Atom {
				a := newOneKeyAtom(t)
				if err := a.MountAll(); err != nil {
					t.Fatal(err)
				}
				return a
			},
		},
		{
			// TimeAbility 的目录先于本测试存在, 一并纳入来源核对以证明其完整性。
			// 探测入参用非 nil 哨兵: sync_net/sync_ntp 对 nil args 会走默认
			// URL 发起真实网络对时, 而两个分支都在类型断言处就先返回
			// ErrInvalidArguments, 既证明已分派又不触网。
			name: "TimeAbility", file: "time_ability.go", receiver: "TimeAbility",
			lister: new(TimeAbility), commander: new(TimeAbility),
			atom:      func(*testing.T) *types.Atom { return nil },
			probeArgs: struct{}{},
		},
	}
}

// TestAbilityListCommandsMatchesDispatch 断言 ListCommands 与源码分派分支集合相等。
func TestAbilityListCommandsMatchesDispatch(t *testing.T) {
	for _, tc := range commandContractCases() {
		t.Run(tc.name, func(t *testing.T) {
			dispatched := contractDispatchCases(t, tc.file, tc.receiver)
			listed := tc.lister.ListCommands()
			contractAssertNoDuplicates(t, tc.name, listed)

			listedSet := make(map[string]bool, len(listed))
			for _, c := range listed {
				listedSet[c] = true
			}
			dispatchSet := make(map[string]bool, len(dispatched))
			for _, c := range dispatched {
				dispatchSet[c] = true
			}
			// 方向一: 分派存在但目录缺登记 —— 调用方无法发现该命令。
			for _, c := range dispatched {
				if !listedSet[c] {
					t.Errorf("%s: Command 分派 %q 未出现在 ListCommands 中", tc.name, c)
				}
			}
			// 方向二: 目录登记但分派不存在 —— 目录宣称了假命令。
			for _, c := range listed {
				if !dispatchSet[c] {
					t.Errorf("%s: ListCommands 登记 %q, 但 Command 中无对应 case 分支", tc.name, c)
				}
			}
		})
	}
}

// TestAbilityListedCommandsAreDispatched 从运行时侧复核方向一: 目录中每条命令
// 都能被 Command 识别(返回非 ErrUnsupportedCommand), 而不是落到 default 分支。
func TestAbilityListedCommandsAreDispatched(t *testing.T) {
	for _, tc := range commandContractCases() {
		t.Run(tc.name, func(t *testing.T) {
			atom := tc.atom(t)
			for _, act := range tc.lister.ListCommands() {
				out := tc.commander.Command(atom, act, tc.probeArgs)
				if errors.Is(out.Err, types.ErrUnsupportedCommand) {
					t.Errorf("%s: 目录中的 %q 落到 default 分支(ErrUnsupportedCommand)", tc.name, act)
				}
			}
		})
	}
}

// TestAbilityListCommandsReturnsCopy 断言返回切片是副本: types.CommandNames 会
// 把结果交给调用方, 调用方就地排序/改写不得污染组件的命令目录。
func TestAbilityListCommandsReturnsCopy(t *testing.T) {
	for _, tc := range commandContractCases() {
		t.Run(tc.name, func(t *testing.T) {
			first := tc.lister.ListCommands()
			if len(first) == 0 {
				t.Fatalf("%s: 空命令目录", tc.name)
			}
			want := append([]string(nil), first...)
			first[0] = "mutated-by-caller"
			second := tc.lister.ListCommands()
			if len(second) != len(want) {
				t.Fatalf("%s: 目录长度变化 %d -> %d", tc.name, len(want), len(second))
			}
			for i := range want {
				if second[i] != want[i] {
					t.Fatalf("%s: 目录被调用方改写污染: idx %d = %q, want %q", tc.name, i, second[i], want[i])
				}
			}
		})
	}
}

func contractAssertNoDuplicates(t *testing.T, name string, cmds []string) {
	t.Helper()
	seen := make(map[string]bool, len(cmds))
	for _, c := range cmds {
		if c == "" {
			t.Fatalf("%s: 命令目录含空字符串", name)
		}
		if seen[c] {
			t.Fatalf("%s: 命令目录重复登记 %q", name, c)
		}
		seen[c] = true
	}
}

// contractDispatchCases 解析 srcFile, 返回 receiver.Command 中 act switch
// 各 case 分支对应的命令字面量(已排序去重)。
func contractDispatchCases(t *testing.T, srcFile, receiver string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, srcFile, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", srcFile, err)
	}
	consts := contractStringConsts(file)

	var out []string
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Command" || fn.Recv == nil || len(fn.Recv.List) == 0 {
			return true
		}
		if contractRecvType(fn.Recv.List[0].Type) != receiver {
			return true
		}
		sw := contractActSwitch(fn.Body)
		if sw == nil {
			return true
		}
		found = true
		for _, stmt := range sw.Body.List {
			clause, ok := stmt.(*ast.CaseClause)
			if !ok || clause.List == nil {
				continue // default 分支
			}
			for _, expr := range clause.List {
				name, ok := expr.(*ast.Ident)
				if !ok {
					t.Fatalf("%s: %s.Command 的 case 标签不是标识符: %T", srcFile, receiver, expr)
				}
				val, ok := consts[name.Name]
				if !ok {
					t.Fatalf("%s: case 标签 %q 无法在同文件 const 中解析为字符串字面量", srcFile, name.Name)
				}
				out = append(out, val)
			}
		}
		return false
	})
	if !found {
		t.Fatalf("%s: 未找到 %s.Command 中以 act 为 tag 的 switch", srcFile, receiver)
	}
	sort.Strings(out)
	return contractDedupe(out)
}

// contractActSwitch 返回 fn 体内第一个 `switch act { ... }` 语句。
func contractActSwitch(body *ast.BlockStmt) *ast.SwitchStmt {
	if body == nil {
		return nil
	}
	var found *ast.SwitchStmt
	ast.Inspect(body, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		sw, ok := n.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		if id, ok := sw.Tag.(*ast.Ident); ok && id.Name == "act" {
			found = sw
			return false
		}
		return true
	})
	return found
}

func contractRecvType(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// contractStringConsts 收集文件内所有可解析为字符串字面量的常量声明
// (含 iota 之外的显式赋值; 非字符串常量直接忽略)。
func contractStringConsts(file *ast.File) map[string]string {
	consts := map[string]string{}
	for _, decl := range file.Decls {
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
				if v, ok := contractConstValue(vs.Values[i], consts); ok {
					consts[name.Name] = v
				}
			}
		}
	}
	return consts
}

func contractConstValue(expr ast.Expr, consts map[string]string) (string, bool) {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(e.Value)
		if err != nil {
			return "", false
		}
		return s, true
	case *ast.Ident:
		v, ok := consts[e.Name]
		return v, ok
	}
	return "", false
}

func contractDedupe(sorted []string) []string {
	if len(sorted) == 0 {
		return nil
	}
	out := make([]string, 0, len(sorted))
	for i, s := range sorted {
		if i > 0 && s == sorted[i-1] {
			continue
		}
		out = append(out, s)
	}
	return out
}
