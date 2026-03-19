package types

// Ability is a generic capability with typed input args and output.
type Ability[Args any, Out any] interface {
	GetName() string      // 获得名称
	Describe() string     // 获得描述
	Check(atmo Atom) bool // 检查方法
	Mount(atmo Atom) bool // 挂载方法
	Command(atmo Atom, act string, args Args) AbilityOutput[Out]
}

// AnyAbility is implemented by concrete abilities to allow storage without type params.
type AnyAbility interface {
	GetName() string
	Describe() string
	Check(atmo Atom) bool
	Mount(atmo Atom) bool
	CommandAny(atmo Atom, act string, args any) AbilityOutput[any]
}

// AbilituyArgs 描述能力调用的参数。
type AbilituyArgs[T any] struct {
	Name        string
	Description string
	Value       T
}

type AbilityOutput[T any] struct {
	Name    string
	Value   T
	Success bool
	Error   string
}
