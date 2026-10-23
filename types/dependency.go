// ─────────────────────────────────────────────────────────────
// FasterEdge 开源项目
// Github: https://github.com/FasterEdge
// Gitee:  https://gitee.com/FasterEdge
// ─────────────────────────────────────────────────────────────
package types

import "fmt"

// DependencyKind identifies which component registry must provide a dependency.
type DependencyKind uint8

const (
	// 零值 0 是无效 Kind: 从 1 起编号, 组件漏填 Kind 不再静默声明为
	// data 依赖(旧实现 iota 从 0 起, 零值=DependencyData——漏填时若名字
	// 恰好命中 data 组件则"正确"运行, 掩盖声明错误)。
	DependencyData DependencyKind = iota + 1
	DependencyAbility
)

func (k DependencyKind) String() string {
	switch k {
	case DependencyData:
		return "data"
	case DependencyAbility:
		return "ability"
	default:
		return fmt.Sprintf("DependencyKind(%d)", k)
	}
}

// Dependency declares a named lifecycle prerequisite.
type Dependency struct {
	Kind DependencyKind
	Name string
}

// DependencyProvider is optional. Components implementing it participate in
// preflight validation and deterministic topological lifecycle ordering.
type DependencyProvider interface {
	Dependencies() []Dependency
}
