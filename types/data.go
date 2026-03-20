package types

// Data 表示一种可存储的数据，Command 接受任意参数。
type Data interface {
	GetName() string
	Describe() string
	Check(atmo Atom) bool
	Mount(atmo Atom) bool
	Command(atmo Atom, act string, args any) DataOutput
}

// DataOutput 描述 Data 操作的返回结果。
type DataOutput struct {
	Name    string // 输出名称
	Value   any    // 输出值
	Success bool   // 操作是否成功
	Error   string // 错误信息（若有）
}
