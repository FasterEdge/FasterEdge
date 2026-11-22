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
	"strconv"
	"testing"

	"github.com/FasterEdge/FasterEdge/data"
	"github.com/FasterEdge/FasterEdge/types"
)

// 本文件守护"工业 + 外部集成组"(Modbus / Serial / TSN / MQTT / InfluxDB /
// EKuiper / Docker / Kubernetes)的命令契约自描述: 每个组件的 ListCommands()
// 必须与它 Command() 里 switch act 的真实分派分支**集合相等、顺序即对外承诺**。
//
// 分派分支一侧不靠手抄第二份清单: 用 go/parser 直接读组件源码, 取 Command
// 方法体内以 act 为 tag 的 switch, 把 case 标签的常量标识符回查同文件 const
// 声明的字符串字面量。这八个组件的命令常量都声明在自己文件里, 因此同文件
// 回查就够(跨文件常量会让这里直接失败而不是静默放过)。

// externalAbilityCase 描述一个组件的命令契约核对项。
type externalAbilityCase struct {
	name      string            // 组件名, 仅用于失败信息
	file      string            // 声明 Command 分派的源文件(与测试同目录)
	receiver  string            // Command 方法接收者类型名
	component externalCataloged // 供 ListCommands 与 Command 同时调用
	atom      func(t *testing.T) *types.Atom
	// probeArgs 是类型不匹配的非 nil 探针: 八个组件的每个分支都在第一行做
	// 类型断言/参数校验, 因此探针只会被拒(ErrInvalidArguments), 不会产生
	// 副作用(不留连接、不写状态、不打网络)。既证明进入了该分支, 又不执行它。
	probeArgs any
	// want 是对外承诺的目录: 顺序即 ListCommands 返回顺序。刻意手写而非从
	// 实现里取——常量被误删/误改时, 这条断言才会响。
	want []string
}

// externalCataloged 聚合 Component(分派契约)与 CommandLister(目录契约)。
type externalCataloged interface {
	types.Component
	types.CommandLister
}

func externalAbilityAtom(t *testing.T) *types.Atom {
	t.Helper()
	a := &types.Atom{}
	if err := a.AddData(&data.BaseData{}); err != nil {
		t.Fatal(err)
	}
	return a
}

func externalInfluxAtom(t *testing.T) *types.Atom {
	t.Helper()
	a := externalAbilityAtom(t)
	if err := a.AddData(data.NewInfluxDBData()); err != nil {
		t.Fatal(err)
	}
	return a
}

func externalAbilityTable() []externalAbilityCase {
	probe := struct{ Unrelated string }{Unrelated: "probe"}
	return []externalAbilityCase{
		{
			name: "ModbusAbility", file: "modbus_ability.go", receiver: "ModbusAbility",
			component: NewModbusAbility(), atom: externalAbilityAtom, probeArgs: probe,
			want: []string{
				ModbusCommandSetEndpoint, ModbusCommandGetEndpoint,
				ModbusCommandSetUnitID, ModbusCommandGetUnitID,
				ModbusCommandReadHolding, ModbusCommandReadInput,
				ModbusCommandReadCoils, ModbusCommandReadDiscrete,
				ModbusCommandWriteHolding, ModbusCommandWriteCoil, ModbusCommandWriteMultiReg,
			},
		},
		{
			name: "SerialAbility", file: "serial_ability.go", receiver: "SerialAbility",
			component: NewSerialAbility(), atom: externalAbilityAtom, probeArgs: probe,
			want: []string{
				SerialCommandOpen, SerialCommandClose, SerialCommandIsOpen,
				SerialCommandWrite, SerialCommandRead,
				SerialCommandSetConfig, SerialCommandGetConfig, SerialCommandListPorts,
			},
		},
		{
			name: "TSNAbility", file: "tsn_ability.go", receiver: "TSNAbility",
			component: NewTSNAbility(), atom: externalAbilityAtom, probeArgs: probe,
			want: []string{
				TSNCommandSetInterface, TSNCommandGetInterface,
				TSNCommandRegisterTalker, TSNCommandRegisterListener,
				TSNCommandUnregister, TSNCommandListStreams,
				TSNCommandSetPriority, TSNCommandGetPriority,
				TSNCommandSetTimeAware, TSNCommandGetTimeAware,
			},
		},
		{
			name: "MQTTAbility", file: "mqtt_ability.go", receiver: "MQTTAbility",
			component: NewMQTTAbility(), atom: externalAbilityAtom, probeArgs: probe,
			want: []string{
				MQTTCommandSetBroker, MQTTCommandGetBroker,
				MQTTCommandSetCredentials, MQTTCommandSetClientID,
				MQTTCommandConnect, MQTTCommandDisconnect, MQTTCommandIsConnected,
				MQTTCommandPublish, MQTTCommandSubscribe, MQTTCommandUnsubscribe,
				MQTTCommandListSubs, MQTTCommandDrain,
			},
		},
		{
			name: "InfluxDBAbility", file: "influxdb_ability.go", receiver: "InfluxAbility",
			component: NewInfluxAbility(), atom: externalInfluxAtom, probeArgs: probe,
			want: []string{
				InfluxCommandSetEndpoint, InfluxCommandSetToken,
				InfluxCommandSetOrg, InfluxCommandSetBucket,
				InfluxCommandGetEndpoint, InfluxCommandGetConfig,
				InfluxCommandPing, InfluxCommandWrite, InfluxCommandQuery,
				InfluxCommandListSeries, InfluxCommandDeleteSeries,
			},
		},
		{
			name: "EKuiperAbility", file: "ekuiper_ability.go", receiver: "EKuiperAbility",
			component: NewEKuiperAbility(), atom: externalAbilityAtom, probeArgs: probe,
			want: []string{
				EKuiperCommandSetEndpoint, EKuiperCommandGetEndpoint,
				EKuiperCommandCreateStream, EKuiperCommandDropStream,
				EKuiperCommandListStreams, EKuiperCommandGetStream,
				EKuiperCommandCreateRule, EKuiperCommandDropRule,
				EKuiperCommandStartRule, EKuiperCommandStopRule,
				EKuiperCommandShowRules, EKuiperCommandGetRuleStatus,
			},
		},
		{
			name: "DockerAbility", file: "docker_ability.go", receiver: "DockerAbility",
			component: NewDockerAbility(), atom: externalAbilityAtom, probeArgs: probe,
			want: []string{
				DockerCommandSetEndpoint, DockerCommandGetEndpoint,
				DockerCommandListContainers,
				DockerCommandStart, DockerCommandStop, DockerCommandRestart,
				DockerCommandRemove, DockerCommandInspect, DockerCommandGetLogs,
				DockerCommandPullImage, DockerCommandCreate,
			},
		},
		{
			name: "KubernetesAbility", file: "kubernetes_ability.go", receiver: "K8sAbility",
			component: NewK8sAbility(), atom: externalAbilityAtom, probeArgs: probe,
			want: []string{
				K8sCommandSetContext, K8sCommandGetContext,
				K8sCommandApply, K8sCommandDelete,
				K8sCommandList, K8sCommandGet, K8sCommandScale, K8sCommandGetLogs,
			},
		},
	}
}

// TestExternalAbilityListCommandsExactOrder 守护目录内容与顺序(对外承诺清单)。
func TestExternalAbilityListCommandsExactOrder(t *testing.T) {
	for _, tc := range externalAbilityTable() {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.component.ListCommands()
			if len(got) != len(tc.want) {
				t.Fatalf("ListCommands() = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("ListCommands()[%d] = %q, want %q (full: %v)", i, got[i], tc.want[i], got)
				}
			}
			seen := make(map[string]struct{}, len(got))
			for _, act := range got {
				if act == "" {
					t.Fatalf("%s: 命令目录含空字符串 %v", tc.name, got)
				}
				if _, dup := seen[act]; dup {
					t.Fatalf("%s: 命令目录重复登记 %q", tc.name, act)
				}
				seen[act] = struct{}{}
			}
		})
	}
}

// TestExternalAbilityListCommandsMatchesDispatch 是本任务的核心断言:
// ListCommands() 的集合 == Command 的 switch act 分支集合(双向比对),
// 分派表直接来自源码 AST, 不依赖人工同步的第二份 list。
func TestExternalAbilityListCommandsMatchesDispatch(t *testing.T) {
	for _, tc := range externalAbilityTable() {
		t.Run(tc.name, func(t *testing.T) {
			declared := tc.component.ListCommands()
			dispatched := externalDispatchActs(t, tc.file, tc.receiver)
			if len(dispatched) != len(declared) {
				t.Fatalf("%s: %d 个分派分支 %v vs 目录 %d 条 %v",
					tc.name, len(dispatched), dispatched, len(declared), declared)
			}
			declaredSet := make(map[string]struct{}, len(declared))
			for _, act := range declared {
				declaredSet[act] = struct{}{}
			}
			// 方向一: 分派存在但目录漏登记 —— 调用方无法发现该命令。
			for _, act := range dispatched {
				if _, ok := declaredSet[act]; !ok {
					t.Fatalf("%s: Command 分派 %q 未出现在 ListCommands %v", tc.name, act, declared)
				}
				delete(declaredSet, act)
			}
			// 方向二: 目录登记但分派不存在 —— 目录宣称了调不通的假命令。
			if len(declaredSet) != 0 {
				extra := make([]string, 0, len(declaredSet))
				for act := range declaredSet {
					extra = append(extra, act)
				}
				sort.Strings(extra)
				t.Fatalf("%s: ListCommands 登记 %v, 但 Command 中无对应 case 分支", tc.name, extra)
			}
		})
	}
}

// TestExternalAbilityListedCommandsAreDispatched 从运行时侧复核: 目录中每条命令
// 都能被 Command 识别(不落到 default 的 ErrUnsupportedCommand), 并回显 act;
// 目录外的 act 必须被显式拒绝为 ErrUnsupportedCommand。
func TestExternalAbilityListedCommandsAreDispatched(t *testing.T) {
	for _, tc := range externalAbilityTable() {
		t.Run(tc.name, func(t *testing.T) {
			atom := tc.atom(t)
			for _, act := range tc.component.ListCommands() {
				out := tc.component.Command(atom, act, tc.probeArgs)
				if errors.Is(out.Err, types.ErrUnsupportedCommand) {
					t.Fatalf("%s: 目录中的 %q 落到 default 分支: %v", tc.name, act, out.Err)
				}
				if out.Name != act {
					t.Fatalf("%s: 命令 %q 回显的 Name 是 %q", tc.name, act, out.Name)
				}
			}
			for _, act := range []string{"", "nope", "list_commands", "SET_ENDPOINT"} {
				out := tc.component.Command(atom, act, nil)
				if !errors.Is(out.Err, types.ErrUnsupportedCommand) {
					t.Fatalf("%s: 目录外的 %q 应以 ErrUnsupportedCommand 拒绝, got %v", tc.name, act, out.Err)
				}
			}
		})
	}
}

// TestExternalAbilityProbeArgsHaveNoSideEffect 证明探针不会被误当成有效入参:
// 目录里每条命令在探针下都必须以 ErrInvalidArguments 拒绝(而不是"成功"或
// "操作失败")。这条同时也守住了"探针不触发网络/不留下连接"的前提。
func TestExternalAbilityProbeArgsHaveNoSideEffect(t *testing.T) {
	for _, tc := range externalAbilityTable() {
		t.Run(tc.name, func(t *testing.T) {
			atom := tc.atom(t)
			for _, act := range tc.component.ListCommands() {
				out := tc.component.Command(atom, act, tc.probeArgs)
				if !errors.Is(out.Err, types.ErrInvalidArguments) {
					t.Fatalf("%s: %q 对探针入参应返回 ErrInvalidArguments, got %v", tc.name, act, out.Err)
				}
			}
		})
	}
}

// TestExternalAbilityListCommandsReturnsCopy 返回值必须是副本: types.CommandNames
// 会把结果交给调用方, 调用方就地改写不得污染组件目录。
func TestExternalAbilityListCommandsReturnsCopy(t *testing.T) {
	for _, tc := range externalAbilityTable() {
		t.Run(tc.name, func(t *testing.T) {
			first := tc.component.ListCommands()
			if len(first) == 0 {
				t.Fatalf("%s: 空命令目录", tc.name)
			}
			original := append([]string(nil), first...)
			for i := range first {
				first[i] = "mutated"
			}
			first = append(first, "appended")
			second := tc.component.ListCommands()
			if len(second) != len(original) {
				t.Fatalf("%s: 目录长度被调用方改写污染: %v", tc.name, second)
			}
			for i := range original {
				if second[i] != original[i] {
					t.Fatalf("%s: ListCommands 泄露内部存储: idx %d = %q, want %q", tc.name, i, second[i], original[i])
				}
			}
		})
	}
}

// TestExternalAbilityCatalogSurfacedByAtomCommandNames 验证目录经
// Atom.CommandNames(ListCommands 的唯一框架调用点)对外可见。
func TestExternalAbilityCatalogSurfacedByAtomCommandNames(t *testing.T) {
	atom := externalInfluxAtom(t)
	for _, tc := range externalAbilityTable() {
		if err := atom.AddAbility(tc.component); err != nil {
			t.Fatalf("AddAbility %s: %v", tc.name, err)
		}
	}
	names := atom.CommandNames()
	for _, tc := range externalAbilityTable() {
		got, ok := names[tc.name]
		if !ok {
			t.Fatalf("CommandNames 缺少 %s", tc.name)
		}
		want := append([]string(nil), tc.component.ListCommands()...)
		sort.Strings(want) // Atom.CommandNames 对每个组件的目录排序后返回
		if len(got) != len(want) {
			t.Fatalf("%s: CommandNames = %v, want %v", tc.name, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s: CommandNames = %v, want %v", tc.name, got, want)
			}
		}
	}
}

// externalDispatchActs 解析 srcFile 中 receiver.Command 方法体内以 act 为 tag
// 的 switch, 返回各 case 标签(不含 default)解析出的命令字符串, 已排序。
func externalDispatchActs(t *testing.T, srcFile, receiver string) []string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(thisFile), srcFile)
	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", srcFile, err)
	}
	consts := externalFileStringConsts(parsed)

	var out []string
	found := false
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Command" || fn.Recv == nil || fn.Body == nil {
			continue
		}
		if len(fn.Recv.List) == 0 || externalRecvTypeName(fn.Recv.List[0].Type) != receiver {
			continue
		}
		// 只认函数体直接子语句里的 switch: 内部辅助函数(如 docker 的
		// simpleContainerAction)不是分派表本身, 不在 fn.Body.List 里。
		for _, stmt := range fn.Body.List {
			sw, ok := stmt.(*ast.SwitchStmt)
			if !ok || sw.Tag == nil {
				continue
			}
			if tag, ok := sw.Tag.(*ast.Ident); !ok || tag.Name != "act" {
				continue
			}
			found = true
			for _, clause := range sw.Body.List {
				cc, ok := clause.(*ast.CaseClause)
				if !ok {
					continue // default
				}
				for _, expr := range cc.List {
					switch v := expr.(type) {
					case *ast.Ident:
						val, ok := consts[v.Name]
						if !ok {
							t.Fatalf("%s: case 标签 %q 无法在同文件 const 中解析为字符串", srcFile, v.Name)
						}
						out = append(out, val)
					case *ast.BasicLit:
						if v.Kind != token.STRING {
							t.Fatalf("%s: case 标签不是字符串字面量: %s", srcFile, v.Value)
						}
						val, err := strconv.Unquote(v.Value)
						if err != nil {
							t.Fatalf("%s: case 标签无法解析: %v", srcFile, err)
						}
						out = append(out, val)
					default:
						t.Fatalf("%s: 不支持的 case 标签类型 %T", srcFile, expr)
					}
				}
			}
			break
		}
		if found {
			break
		}
	}
	if !found {
		t.Fatalf("%s: 未找到 %s.Command 中以 act 为 tag 的 switch", srcFile, receiver)
	}
	if len(out) == 0 {
		t.Fatalf("%s: %s.Command 的 switch act 没有任何 case 分支", srcFile, receiver)
	}
	sort.Strings(out)
	return out
}

func externalRecvTypeName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// externalFileStringConsts 收集单个文件内可解析为字符串字面量的包级常量。
func externalFileStringConsts(file *ast.File) map[string]string {
	consts := map[string]string{}
	var pending [][2]string
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
				switch v := vs.Values[i].(type) {
				case *ast.BasicLit:
					if v.Kind != token.STRING {
						continue
					}
					if val, err := strconv.Unquote(v.Value); err == nil {
						consts[name.Name] = val
					}
				case *ast.Ident:
					pending = append(pending, [2]string{name.Name, v.Name})
				}
			}
		}
	}
	// 常量可以引用同文件其它常量, 迭代到不动点。
	for pass := 0; pass < 8 && len(pending) > 0; pass++ {
		progressed := false
		remaining := pending[:0]
		for _, p := range pending {
			if val, ok := consts[p[1]]; ok {
				consts[p[0]] = val
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
	return consts
}
