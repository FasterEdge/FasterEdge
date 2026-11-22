// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

const (
	generatorName = "fasteredge-contract-extract"

	flagOut     = "out"
	flagRepo    = "repo"
	flagStdout  = "stdout"
	flagProject = "project"
)

func main() {
	out := flag.String(flagOut, "", "contract.json 输出路径")
	repo := flag.String(flagRepo, "", "FasterEdge 仓库根目录（含 ability/ 与 data/）")
	toStdout := flag.Bool(flagStdout, false, "把契约写到标准输出而不是文件")
	project := flag.String(flagProject, "", "project 元信息名（默认取仓库目录名）")
	flag.Parse()

	root := *repo
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			fatal("getwd: %v", err)
		}
		// 默认从当前工作目录向上找 go.mod
		root = findRepoRoot(wd)
		if root == "" {
			fatal("未找到含 ability/ 与 data/ 的仓库根目录，请用 -repo 指定")
		}
	}
	if *out == "" && !*toStdout {
		*out = filepath.Join(root, "dist", "contract.json")
	}
	name := *project
	if name == "" {
		name = filepath.Base(filepath.Clean(root))
	}

	contract, err := extract(root, fmt.Sprintf("%s %s", generatorName, name))
	if err != nil {
		fatal("提取契约失败: %v", err)
	}
	data := marshalJSON(contract)
	if *toStdout {
		fmt.Println(string(data))
		return
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		fatal("创建输出目录: %v", err)
	}
	if err := os.WriteFile(*out, append(data, '\n'), 0o644); err != nil {
		fatal("写出 contract.json: %v", err)
	}
	fmt.Printf("契约已写入 %s\n", *out)
	fmt.Printf("组件 %d 个，命令 %d 条（去重 %d），命令常量 %d 个，未引用常量 %d 个\n",
		contract.Stats.Components, contract.Stats.Commands, contract.Stats.DistinctCommandVals,
		contract.Stats.CommandConstants, len(contract.UnusedConstants))
	fmt.Printf("参数结构体 %d，无参 %d，未知 %d；字段必填 %d / 可选 %d / 未知 %d\n",
		contract.Stats.ArgsStruct, contract.Stats.ArgsNone, contract.Stats.ArgsUnknown,
		contract.Stats.FieldsRequired, contract.Stats.FieldsOptional, contract.Stats.FieldsUnknown)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "contract-extract: "+format+"\n", args...)
	os.Exit(1)
}

func findRepoRoot(start string) string {
	dir := start
	for i := 0; i < 8; i++ {
		if isDir(filepath.Join(dir, "ability")) && isDir(filepath.Join(dir, "data")) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
