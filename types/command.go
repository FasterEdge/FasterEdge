package types

// CommandOutput is the result of invoking a component command.
type CommandOutput struct {
	Name  string
	Value any
	Err   error
}

// Success reports whether the command completed without an error.
func (o CommandOutput) Success() bool { return o.Err == nil }
