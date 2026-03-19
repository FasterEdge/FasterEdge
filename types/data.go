package types

// Data marks a data element with typed args/output.
type Data[Args any, Out any] interface {
	GetName() string      // 获得名称
	Describe() string     // 获得描述
	Check(atmo Atom) bool // 检查方法
	Mount(atmo Atom) bool // 挂载方法
	Command(atmo Atom, act string, args Args) DataOutput[Out]
}

// AnyData provides an untyped entry-point for heterogeneous storage.
type AnyData interface {
	GetName() string
	Describe() string
	Check(atmo Atom) bool
	Mount(atmo Atom) bool
	CommandAny(atmo Atom, act string, args any) DataOutput[any]
}

// DataArgs is a generic type for data arguments.
type DataArgs[T any] struct {
	Name        string // The name of the data
	Description string // A brief description of the data
	Value       T      // The value of the data
}

// DataOutput is a generic type for data output results.
type DataOutput[T any] struct {
	Name    string // The name of the output
	Value   T      // The value of the output
	Success bool   // Indicates if the operation was successful
	Error   string // Contains error message if any
}
