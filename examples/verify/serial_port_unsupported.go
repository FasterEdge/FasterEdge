// ─────────────────────────────────────────────────────────────
// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
// ─────────────────────────────────────────────────────────────
//go:build !windows && !linux

// 未适配平台的串口后端: open/read/write/close 一律返回明确 error, list_ports
// 返回空列表。这样 examples/verify 在 darwin/wasm 等 GOOS 下也能编译和运行,
// 只是 SerialAbility 的联调段会按"该平台未实现"跳过, 而不是编译失败。
package main

import (
	"fmt"
	"runtime"

	"github.com/FasterEdge/FasterEdge/ability"
)

func openSerialDevice(port string, _ ability.SerialConfig) (serialDevice, error) {
	return nil, fmt.Errorf("serial transport is not implemented on %s/%s (port %q)", runtime.GOOS, runtime.GOARCH, port)
}

func listSerialPorts() ([]string, error) {
	return []string{}, nil
}
