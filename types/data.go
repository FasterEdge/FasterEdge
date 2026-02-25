package types

// Data marks a data element.
type Data interface {
	GetName() string      // 获得名称
	Describe() string     // 获得描述
	Check(atmo Atom) bool // 检查方法
	Mount(atmo Atom) bool // 挂载方法
	Command(atmo Atom, act string, args ...string) bool
}
