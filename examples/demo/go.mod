module github.com/FasterEdge/FasterEdge/examples/demo

go 1.25.5

require github.com/FasterEdge/FasterEdge v0.0.0

require (
	github.com/beevik/ntp v1.5.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

replace github.com/FasterEdge/FasterEdge => ../..
