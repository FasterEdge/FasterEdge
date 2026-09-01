// ─────────────────────────────────────────────────────────────
// FasterEdge 开源项目
// Github: https://github.com/FasterEdge
// Gitee:  https://gitee.com/FasterEdge
// ─────────────────────────────────────────────────────────────
// Package main 演示 FasterEdge 框架的最小可运行用法:
//  1. 启动一个挂载了常用组件的 Atom
//  2. 通过 NetMap 注册对等节点
//  3. 通过 OneKey 为其签发短期令牌并验证
//  4. 通过 TimeAbility 完成系统时间同步
//  5. 通过 ConfigFileAbility 把运行时配置持久化到 JSON 文件
//  6. 通过 ShAbility 在本地 sh 中执行 echo
//  7. 优雅退出
//
// 运行:
//
//	go run .
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	fasteredge "github.com/FasterEdge/FasterEdge"
	"github.com/FasterEdge/FasterEdge/ability"
	"github.com/FasterEdge/FasterEdge/data"
	"github.com/FasterEdge/FasterEdge/types"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("demo failed: %v", err)
	}
}

func run() error {
	// 1. 一行注册完整骨架
	atom := fasteredge.InitStandardAtom()

	// 2. 挂载所有组件
	if err := fasteredge.PreRunAtom(atom); err != nil {
		return fmt.Errorf("pre-run: %w", err)
	}
	fmt.Printf("[demo] atom mounted: %s\n", atom.GetName())

	// 3. NetMap: 注册对等节点 edge-2
	nm, _ := atom.Ability("NetMapAbility")
	if out := nm.Command(atom, ability.NetMapCommandRegisterPeer, ability.NetMapRegisterPeerArgs{
		Name:    "edge-2",
		Address: "10.0.0.2:7000",
		Role:    "edge",
	}); out.Err != nil {
		return fmt.Errorf("register peer: %w", out.Err)
	}
	fmt.Println("[demo] netmap: registered edge-2@10.0.0.2:7000")

	// 4. OneKey: 为 edge-2 签发 1 小时令牌
	ok, _ := atom.Ability("OneKeyAbility")
	issueOut := ok.Command(atom, ability.OneKeyCommandIssueToken, ability.OneKeyIssueTokenArgs{
		Subject: "edge-2",
		TTL:     time.Hour,
	})
	if issueOut.Err != nil {
		return fmt.Errorf("issue token: %w", issueOut.Err)
	}
	tok, _ := issueOut.Value.(ability.OneKeyToken)
	fmt.Printf("[demo] onekey: issued token for edge-2 (sig=%s...)\n", tok.Signature[:12])

	// 把签名后的 token 编码后跨节点传输,然后在远端用 verify 校验
	wire := ability.EncodeForTransmission(tok)
	fmt.Printf("[demo] onekey: wire format = %s\n", wire)
	dec, err := ability.DecodeFromTransmission(wire)
	if err != nil {
		return fmt.Errorf("decode wire token: %w", err)
	}
	verifyOut := ok.Command(atom, ability.OneKeyCommandVerifyToken, ability.OneKeyVerifyTokenArgs{
		Subject:   dec.Subject,
		IssuedAt:  dec.IssuedAt,
		ExpiresAt: dec.ExpiresAt,
		Signature: dec.Signature,
	})
	if verifyOut.Err != nil {
		return fmt.Errorf("verify token: %w", verifyOut.Err)
	}
	fmt.Printf("[demo] onekey: verify ok, subject = %s\n", verifyOut.Value)

	// 5. Time: 把本地时间作为基准
	ta, _ := atom.Ability("TimeAbility")
	if out := ta.Command(atom, ability.TimeCommandSyncSystem, nil); out.Err != nil {
		return fmt.Errorf("sync system: %w", out.Err)
	}
	if out := ta.Command(atom, ability.TimeCommandGetTime, nil); out.Err != nil {
		return fmt.Errorf("get time: %w", out.Err)
	} else if snap, ok := out.Value.(ability.TimeSnapshot); ok && !snap.Time.IsZero() {
		fmt.Printf("[demo] time: now = %s (source=%s)\n", snap.Time.Format(time.RFC3339), snap.Source)
	}

	// 6. ConfigFile: 把当前 NetMap 节点名写进 config.json 并落盘
	cfgData, _ := atom.Data("ConfigData")
	if out := cfgData.Command(atom, data.ConfigCommandSet, data.ConfigSetArgs{
		Key: "node.name", Value: "demo-node-1",
	}); out.Err != nil {
		return fmt.Errorf("config set: %w", out.Err)
	}
	cfa, _ := atom.Ability("ConfigFileAbility")
	cfgPath := filepath.Join(os.TempDir(), "FasterEdge-demo-config.json")
	if out := cfa.Command(atom, ability.ConfigFileCommandSave, ability.ConfigFileSaveArgs{
		Path:      cfgPath,
		Overwrite: true,
	}); out.Err != nil {
		return fmt.Errorf("config save: %w", out.Err)
	}
	fmt.Printf("[demo] config: saved to %s\n", cfgPath)

	// 7. Sh: 用白名单 sh -c 执行 echo(白名单仅允许 printf)
	sh, _ := atom.Ability("ShAbility")
	if out := sh.Command(atom, ability.ShCommandSetAllowlist, ability.ShSetAllowlistArgs{
		Allowed: []string{"printf"},
	}); out.Err != nil {
		return fmt.Errorf("set sh allowlist: %w", out.Err)
	}
	if out := sh.Command(atom, ability.ShCommandRun, ability.ShRunArgs{
		Command: "printf 'hello from sh\\n'",
		Timeout: 3 * time.Second,
	}); out.Err != nil {
		return fmt.Errorf("sh run: %w", out.Err)
	} else if res, ok := out.Value.(ability.CmdResult); ok && res.ExitCode == 0 {
		fmt.Printf("[demo] sh: stdout = %q\n", res.Stdout)
	}

	// 8. 优雅退出:100ms 后取消上下文,RunAtom 在没有 Runner 的情况下会立刻返回
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	if err := fasteredge.RunAtom(ctx, atom, fasteredge.WithShutdownTimeout(2*time.Second)); err != nil && err != context.Canceled {
		return fmt.Errorf("run atom: %w", err)
	}
	fmt.Println("[demo] done")
	return nil
}

// 静态检查:确保所有用到的类型仍然被引用,避免未来重构时 example 静默失效。
var _ = types.CommandOutput{}
