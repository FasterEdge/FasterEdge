// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package main

import (
	"bytes"
	"encoding/json"
)

// marshalJSON 输出稳定的 JSON：键序由结构体字段顺序决定（天然稳定），
// 切片按 go 侧写入顺序输出（extract 内已排序），因此两次运行字节一致。
func marshalJSON(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(err)
	}
	out := bytes.TrimRight(buf.Bytes(), "\n")
	return out
}
