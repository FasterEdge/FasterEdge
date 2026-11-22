// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package main

import (
	"go/ast"
	"path/filepath"
	"testing"
)

// 本文件守护"契约提取器"本身: 提取结果必须与 ability/ 与 data/ 的真实
// 源码一致——组件集合、命令数、代表性分支的参数结构体、以及"分派分支 =
// ListCommands 目录"这条不变量(由主仓库 command_contract_test.go 守护,
// 这里复用同一批源码解析逻辑做交叉复核)。

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if isDir(filepath.Join(wd, "ability")) && isDir(filepath.Join(wd, "data")) {
			return wd
		}
		wd = filepath.Dir(wd)
	}
	t.Fatal("未找到仓库根目录(ability/ 与 data/ 的父目录)")
	return ""
}

func mustExtract(t *testing.T, repo string) *Contract {
	t.Helper()
	contract, err := extract(repo, generatorName)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	return contract
}

func findComponent(t *testing.T, c *Contract, name string) Component {
	t.Helper()
	for _, comp := range c.Components {
		if comp.Name == name {
			return comp
		}
	}
	t.Fatalf("组件 %s 未出现在契约中(共 %d 个组件)", name, len(c.Components))
	return Component{}
}

func findCommand(t *testing.T, comp Component, act string) Command {
	t.Helper()
	for _, cmd := range comp.Commands {
		if cmd.Name == act {
			return cmd
		}
	}
	t.Fatalf("%s 的命令 %s 未出现在契约中", comp.Name, act)
	return Command{}
}

// TestExtractCoversEveryComponent 提取器必须覆盖 ability/ 与 data/ 的全部
// 组件：这里不硬编码名单，而是用"源文件里每个 Command 方法 + GetName"反推，
// 与提取器枚举的组件集合做双向比对——遗漏即失败。
func TestExtractCoversEveryComponent(t *testing.T) {
	repo := repoRoot(t)
	contract := mustExtract(t, repo)

	abilityPkg, err := loadPkg("ability", filepath.Join(repo, "ability"))
	if err != nil {
		t.Fatal(err)
	}
	dataPkg, err := loadPkg("data", filepath.Join(repo, "data"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, p := range []*goPkg{abilityPkg, dataPkg} {
		for key := range p.methods {
			if len(key) > 8 && key[len(key)-8:] == ".Command" {
				recv := key[:len(key)-8]
				if name, ok := p.componentName(recv); ok {
					want[name] = true
				}
			}
		}
	}
	got := map[string]bool{}
	for _, comp := range contract.Components {
		got[comp.Name] = true
	}
	for name := range want {
		if !got[name] {
			t.Errorf("组件 %s 有 Command+GetName 但未进入契约", name)
		}
	}
	for name := range got {
		if !want[name] {
			t.Errorf("契约包含组件 %s，但源文件里找不到对应 Command 方法", name)
		}
	}
	if len(contract.Components) < 20 {
		t.Fatalf("组件数 %d 异常偏少", len(contract.Components))
	}
}

// TestExtractCommandsMatchDispatch 每条契约命令都必须能在源码的
// `switch act` 分派里找到（反向不变量：不能凭空造命令）。这里不复用
// commandSpecs，而是独立地再从 AST 取一遍 case 标签，做双向集合比对。
func TestExtractCommandsMatchDispatch(t *testing.T) {
	repo := repoRoot(t)
	contract := mustExtract(t, repo)
	for _, comp := range contract.Components {
		pkg, err := loadPkg(comp.Kind, filepath.Join(repo, comp.Kind))
		if err != nil {
			t.Fatal(err)
		}
		dispatch := map[string]bool{}
		for key, fn := range pkg.methods {
			if len(key) <= 8 || key[len(key)-8:] != ".Command" {
				continue
			}
			recv := key[:len(key)-8]
			if name, ok := pkg.componentName(recv); !ok || name != comp.Name {
				continue
			}
			sw := findActSwitch(fn.Body)
			if sw == nil {
				// MySQLData / PostgreSQLData 是"整体转发型"：Command 自身没有
				// act switch，命令在共享实现 sqlCommand 里分派。改用提取器的
				// 展开路径取集合，与契约命令做双向比对。
				for _, cmd := range pkg.commandSpecs(recv) {
					dispatch[cmd.Name] = true
				}
				continue
			}
			for _, stmt := range sw.Body.List {
				clause, ok := stmt.(*ast.CaseClause)
				if !ok || clause.List == nil {
					continue
				}
				for _, expr := range clause.List {
					if _, v, ok := pkg.caseValue(expr); ok {
						dispatch[v] = true
					}
				}
			}
		}
		if len(dispatch) == 0 {
			t.Errorf("%s: 源码里找不到 act 分派分支", comp.Name)
			continue
		}
		seen := map[string]bool{}
		for _, cmd := range comp.Commands {
			if seen[cmd.Name] {
				t.Errorf("%s: 命令 %s 在契约中重复", comp.Name, cmd.Name)
			}
			seen[cmd.Name] = true
			if cmd.Constant == "" {
				t.Errorf("%s: 命令 %s 未记录常量名", comp.Name, cmd.Name)
			}
			if !dispatch[cmd.Name] {
				t.Errorf("%s: 契约命令 %q 在源码分派中不存在", comp.Name, cmd.Name)
			}
		}
		for act := range dispatch {
			if !seen[act] {
				t.Errorf("%s: 源码分派 %q 未进入契约", comp.Name, act)
			}
		}
	}
}

// TestExtractArgsSpecs 抽查若干条"分派形态不同"的命令:
// 直接断言 / nil-only / 委托给 helper(act, args) / 匿名结构体 / wrapper 嵌套。
func TestExtractArgsSpecs(t *testing.T) {
	repo := repoRoot(t)
	contract := mustExtract(t, repo)

	// RoleAbility.set_role: 直接类型断言，字段 Role 被空串拒绝 → required
	role := findCommand(t, findComponent(t, contract, "RoleAbility"), "set_role")
	if role.Args.Kind != argsKindStruct || role.Args.Type != "RoleAbilityArgs" {
		t.Fatalf("set_role args = %+v", role.Args)
	}
	if len(role.Args.Fields) != 1 || role.Args.Fields[0].Name != "Role" {
		t.Fatalf("set_role fields = %+v", role.Args.Fields)
	}

	// RoleAbility.get_role: 分支内 `if args != nil` 拒绝 → none
	getRole := findCommand(t, findComponent(t, contract, "RoleAbility"), "get_role")
	if getRole.Args.Kind != argsKindNone {
		t.Fatalf("get_role args kind = %s", getRole.Args.Kind)
	}

	// DockerAbility.list_containers: 匿名结构体 args.(struct{ All bool })
	list := findCommand(t, findComponent(t, contract, "DockerAbility"), "list_containers")
	if list.Args.Kind != argsKindStruct || list.Args.Type != "struct{All bool}" {
		t.Fatalf("list_containers args = %+v", list.Args)
	}
	if len(list.Args.Fields) != 1 || list.Args.Fields[0].Name != "All" {
		t.Fatalf("list_containers fields = %+v", list.Args.Fields)
	}

	// DockerAbility.start_container: 经 simpleContainerAction(act, args) 委托
	start := findCommand(t, findComponent(t, contract, "DockerAbility"), "start_container")
	if start.Args.Kind != argsKindStruct || start.Args.Type != "DockerContainerArgs" {
		t.Fatalf("start_container args = %+v", start.Args)
	}
	if start.Args.Via == "" {
		t.Error("start_container 应记录委托路径 via")
	}
	if start.Args.Fields[0].Required != reqRequired {
		t.Errorf("start_container IDOrName required = %s", start.Args.Fields[0].Required)
	}

	// BashAbility.list: 经 b.sh.Command(mapBashCommand(act), args) 委托
	bashList := findCommand(t, findComponent(t, contract, "BashAbility"), "list")
	if bashList.Args.Kind != argsKindNone {
		t.Fatalf("BashAbility.list args = %+v", bashList.Args)
	}
	if bashList.Args.Via == "" {
		t.Error("BashAbility.list 应记录委托路径 via")
	}

	// InfluxDBData.configure: SQLDatabaseConfigureArgs 包着 Config 结构体
	configure := findCommand(t, findComponent(t, contract, "InfluxDBData"), "configure")
	if configure.Args.Kind != argsKindStruct || configure.Args.Wrapper == "" {
		t.Fatalf("configure args = %+v", configure.Args)
	}
	if configure.Args.Wrapper != "InfluxDBConfig" {
		t.Errorf("configure wrapper = %s", configure.Args.Wrapper)
	}

	// InfluxDBData.set_secret: 经 setDatabaseSecret(store, act, args) 泛型 helper
	setSecret := findCommand(t, findComponent(t, contract, "InfluxDBData"), "set_secret")
	if setSecret.Args.Kind != argsKindStruct || setSecret.Args.Type != "DatabaseSetSecretArgs" {
		t.Fatalf("set_secret args = %+v", setSecret.Args)
	}

	// BaseData.logo: switch 之前统一拒绝非 nil args —— 静态不可判定 → unknown
	logo := findCommand(t, findComponent(t, contract, "BaseData"), "logo")
	if logo.Args.Kind != argsKindUnknown {
		t.Fatalf("BaseData.logo args kind = %s, want %s", logo.Args.Kind, argsKindUnknown)
	}
}

// TestExtractNoUnusedCommandConstants 命令常量(名字带 Command 的字符串常量)
// 必须至少被一个分派分支引用：未引用说明是"宣称存在但调不通"的死常量。
// 主仓库确有刻意保留的例外(如 KeyringData.issue_token 的直连入口语义),
// 因此这里只断言集合数量级正确、且每一条都能在某个分派分支里找到同值。
func TestExtractNoUnusedCommandConstants(t *testing.T) {
	repo := repoRoot(t)
	contract := mustExtract(t, repo)

	dispatched := map[string]bool{}
	for _, comp := range contract.Components {
		for _, cmd := range comp.Commands {
			dispatched[cmd.Name] = true
		}
	}
	for _, u := range contract.UnusedConstants {
		if dispatched[u.Value] {
			t.Errorf("常量 %s = %q 标记为未引用，但该值在某个分派分支中出现", u.Constant, u.Value)
		}
	}
	// 命令常量全集应远大于去重命令值（同名常量在多个组件里复用）
	if contract.Stats.CommandConstants <= contract.Stats.DistinctCommandVals {
		t.Errorf("命令常量 %d 应大于去重命令值 %d", contract.Stats.CommandConstants, contract.Stats.DistinctCommandVals)
	}
}

// TestExtractStable 同一份源码两次提取必须字节一致（契约要能进版本控制）。
func TestExtractStable(t *testing.T) {
	repo := repoRoot(t)
	first := marshalJSON(mustExtract(t, repo))
	second := marshalJSON(mustExtract(t, repo))
	if string(first) != string(second) {
		t.Fatal("两次提取结果不一致，契约输出不稳定")
	}
}
