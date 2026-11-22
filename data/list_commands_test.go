package data

import (
	"errors"
	"sort"
	"testing"

	"github.com/FasterEdge/FasterEdge/types"
)

// commandCase 描述一个组件在"目录与分派表一致性"测试中的用例。
// prepare 在组件首次分派前调用, 用于把组件推进到命令可用的状态(如挂载)。
type commandCase struct {
	name    string
	ctor    func() types.CommandLister
	prepare func(t *testing.T, c types.Component)
	// want 是该组件的规范命令目录(以常量名声明, 避免字符串重复漂移)。
	want []string
}

// dataCommandCases 覆盖 data 包下的全部组件。新增组件必须在此登记,
// 否则其 ListCommands 不会得到"与 Command 分派表一致"的断言守护。
func dataCommandCases() []commandCase {
	return []commandCase{
		{
			name: "BaseData",
			ctor: func() types.CommandLister { return &BaseData{} },
			want: baseDataCommands,
		},
		{
			name: "ConfigData",
			ctor: func() types.CommandLister { return NewConfigData() },
			want: configCommands,
		},
		{
			name: "NetMapData",
			ctor: func() types.CommandLister { return NewNetMapData() },
			prepare: func(t *testing.T, c types.Component) {
				// Mount 负责首次扫描网卡; 未扫描时 set_default_iface
				// 找不到任何接口会返回 ErrInvalidArguments。
				if err := c.Mount(nil); err != nil {
					t.Fatalf("mount NetMapData: %v", err)
				}
			},
			want: netMapCommands,
		},
		{
			name: "KeyringData",
			ctor: func() types.CommandLister { return NewKeyringData() },
			prepare: func(t *testing.T, c types.Component) {
				if err := c.Mount(nil); err != nil {
					t.Fatalf("mount KeyringData: %v", err)
				}
			},
			want: keyringCommands,
		},
		{
			name: "MySQLData",
			ctor: func() types.CommandLister { return NewMySQLData() },
			want: databaseSecretCommands(),
		},
		{
			name: "PostgreSQLData",
			ctor: func() types.CommandLister { return NewPostgreSQLData() },
			want: databaseSecretCommands(),
		},
		{
			// SQLiteData 是唯一没有独立密钥的数据库组件: 目录必须
			// 明确少掉 set_secret / clear_secret, 而非与其他 DB 相同。
			name: "SQLiteData",
			ctor: func() types.CommandLister { return NewSQLiteData() },
			want: databaseLocalCommands(),
		},
		{
			name: "RedisData",
			ctor: func() types.CommandLister { return NewRedisData() },
			want: databaseSecretCommands(),
		},
		{
			name: "MongoDBData",
			ctor: func() types.CommandLister { return NewMongoDBData() },
			want: databaseSecretCommands(),
		},
		{
			name: "InfluxDBData",
			ctor: func() types.CommandLister { return NewInfluxDBData() },
			want: databaseSecretCommands(),
		},
	}
}

// TestDataListCommandsMatchesDispatch 断言每个 data 组件的
// ListCommands() 集合 == Command() 能识别的 act 集合:
//  1. 目录中每个命令逐个试运行, 不得返回 ErrUnsupportedCommand
//     (说明分派表确实认识它);
//  2. 逐个 act 探测分派表能识别的集合, 不得出现"目录有、分派表无"或
//     "分派表有、目录无"的漂移。
//
// 用错误分类而非"返回 nil"作判据: 某些命令(如 set_default_iface 未配
// 置网卡)会因参数/前置状态返回 ErrInvalidArguments, 那仍是"已识别"。
func TestDataListCommandsMatchesDispatch(t *testing.T) {
	for _, tc := range dataCommandCases() {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.ctor()
			comp, ok := c.(types.Component)
			if !ok {
				t.Fatalf("%s is not a types.Component", tc.name)
			}
			if tc.prepare != nil {
				tc.prepare(t, comp)
			}

			got := c.ListCommands()
			// 目录必须是常量级声明, 顺序与数量都与期望集合一致。
			if !sameStringSet(got, tc.want) {
				t.Fatalf("ListCommands() = %v, want %v (as sets)", got, tc.want)
			}

			// (1) 目录内每个命令都必须被分派表识别。
			for _, act := range got {
				out := comp.Command(nil, act, nil)
				if errors.Is(out.Err, types.ErrUnsupportedCommand) {
					t.Fatalf("ListCommands() advertises %q but Command() rejects it as unsupported", act)
				}
			}

			// (2) 探测分派表实际能识别的集合。候选集 = 期望目录 + 各组件
			// 的其它已知命令名 + 一个必然未知的名字, 覆盖三类漂移:
			// 目录漏登记、目录登记了不存在的命令、目录与他人命令名撞名。
			for _, act := range dispatchProbeSet(tc) {
				out := comp.Command(nil, act, nil)
				recognized := !errors.Is(out.Err, types.ErrUnsupportedCommand)
				inCatalogue := containsString(got, act)
				switch {
				case recognized && !inCatalogue:
					t.Fatalf("Command() recognizes %q but ListCommands() omits it", act)
				case !recognized && inCatalogue:
					t.Fatalf("ListCommands() advertises %q but Command() rejects it as unsupported", act)
				}
			}
		})
	}
}

// TestDataListCommandsReturnsDefensiveCopy: ListCommands 返回的切片若与
// 包级目录共享底层数组, 调用方改写返回值会腐蚀后续所有调用方——包括
// Atom.CommandNames 之外的直连用户。断言两次调用互不影响。
func TestDataListCommandsReturnsDefensiveCopy(t *testing.T) {
	for _, tc := range dataCommandCases() {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.ctor()
			first := c.ListCommands()
			if len(first) > 0 {
				first[0] = "mutated-by-caller"
			}
			second := c.ListCommands()
			for _, v := range second {
				if v == "mutated-by-caller" {
					t.Fatalf("ListCommands() shares backing storage: %v", second)
				}
			}
		})
	}
}

// TestDataListCommandsNoDuplicateNames: 目录内重复项会让 Atom.CommandNames
// 的去重行为变成静默掩盖——调用方无法分辨"声明两次"与"分派两次"。
func TestDataListCommandsNoDuplicateNames(t *testing.T) {
	for _, tc := range dataCommandCases() {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.ctor().ListCommands()
			sorted := append([]string(nil), got...)
			sort.Strings(sorted)
			for i := 1; i < len(sorted); i++ {
				if sorted[i] == sorted[i-1] {
					t.Fatalf("ListCommands() has duplicate %q: %v", sorted[i], got)
				}
			}
		})
	}
}

// TestDataListCommandsNoEmptyNames: 空命令名会与"未登记"在展示层混淆,
// 且 dispatch 的 default 分支必然拒绝它——属登记错误。
func TestDataListCommandsNoEmptyNames(t *testing.T) {
	for _, tc := range dataCommandCases() {
		t.Run(tc.name, func(t *testing.T) {
			for _, act := range tc.ctor().ListCommands() {
				if act == "" {
					t.Fatalf("ListCommands() contains an empty command name: %v", tc.ctor().ListCommands())
				}
			}
		})
	}
}

// dispatchProbeSet 构造"分派表探测集": 期望目录 ∪ 全部已知命令常量 ∪
// 一个必然未知的名字。加入跨组件的已知常量是为了捕捉"某个组件误把别组
// 件的命令写进自己目录"这类复制粘贴漂移。
func dispatchProbeSet(tc commandCase) []string {
	probe := append([]string(nil), tc.want...)
	probe = append(probe, baseDataCommands...)
	probe = append(probe, configCommands...)
	probe = append(probe, netMapCommands...)
	probe = append(probe, keyringCommands...)
	probe = append(probe, databaseSecretCommands()...)
	probe = append(probe, databaseLocalCommands()...)
	// 必然未知: 保证探测集总有一个"两个集合都不含"的基准点,
	// 否则 recognized==false 且 inCatalogue==false 的分支永不执行。
	probe = append(probe, "__fasteredge_no_such_command__")
	return dedupeStrings(probe)
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
