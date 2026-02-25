package types

// Data marks a data element.
type Data interface {
	Describe() string     // 获得描述
	Check(atmo Atom) bool // 检查方法
	Mount(atmo Atom) bool // 挂载方法

}
