package types

type Ability interface {
	Describe() string     // 获得描述
	Check(atmo Atom) bool // 检查方法
	Mount(atmo Atom) bool // 挂载方法
}
