package types

// Ability 表示一种能力，Command 接受任意参数。
type Ability interface {
	GetName() string
	Describe() string
	Check(atmo Atom) bool
	Mount(atmo Atom) bool
	Command(atmo Atom, act string, args any) AbilityOutput
}

type AbilityOutput struct {
	Name    string // 输出名称
	Value   any    // 输出值
	Success bool   // 操作是否成功
	Error   string // 错误信息（若有）
}
