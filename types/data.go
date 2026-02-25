package types

// Data marks a data element.
type Data interface {
	Check(atmo Atom) bool // 检查方法
	Mount(atmo Atom) bool // 挂载方法
}
