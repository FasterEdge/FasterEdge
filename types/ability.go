package types

type Ability interface {
	Check(atmo Atom) bool // 检查方法
	Mount(atmo Atom) bool // 挂载方法
}
