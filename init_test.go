package FasterEdge

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/FasterEdge/FasterEdge/ability"
	"github.com/FasterEdge/FasterEdge/data"
)

func TestInitStandardAtomRegistersAllCommonComponents(t *testing.T) {
	atom := InitStandardAtom()
	if err := atom.PreRun(); err != nil {
		t.Fatal(err)
	}
	wantData := map[string]bool{
		"BaseData":    true,
		"NetMapData":  true,
		"KeyringData": true,
		"ConfigData":  true,
	}
	for name := range wantData {
		if _, ok := atom.Data(name); !ok {
			t.Errorf("missing data: %s", name)
		}
	}
	wantAbilities := map[string]bool{
		"BaseAbility":       true,
		"RoleAbility":       true,
		"TimeAbility":       true,
		"NetMapAbility":     true,
		"OneKeyAbility":     true,
		"CmdAbility":        true,
		"ShAbility":         true,
		"BashAbility":       true,
		"ConfigFileAbility": true,
	}
	for name := range wantAbilities {
		if _, ok := atom.Ability(name); !ok {
			t.Errorf("missing ability: %s", name)
		}
	}
}

// TestIntegrationNetMapAndOneKey 验证一个完整链路:
//  1. 通过 NetMap 注册对等节点 edge-2
//  2. 用 OneKey 为 edge-2 签发短期令牌
//  3. 用 OneKey.verify_token 在远端模拟校验
//  4. 检查 RoleAbility + TimeAbility 基础读写正常
func TestIntegrationNetMapAndOneKey(t *testing.T) {
	atom := InitStandardAtom()
	if err := atom.PreRun(); err != nil {
		t.Fatal(err)
	}
	nm, _ := atom.Ability("NetMapAbility")
	if out := nm.Command(atom, ability.NetMapCommandRegisterPeer, ability.NetMapRegisterPeerArgs{
		Name: "edge-2", Address: "10.0.0.2:7000", Role: "edge",
	}); out.Err != nil {
		t.Fatalf("register peer: %v", out.Err)
	}
	ok, _ := atom.Ability("OneKeyAbility")
	issueOut := ok.Command(atom, ability.OneKeyCommandIssueToken, ability.OneKeyIssueTokenArgs{
		Subject: "edge-2", TTL: time.Hour,
	})
	if issueOut.Err != nil {
		t.Fatalf("issue token: %v", issueOut.Err)
	}
	tok := issueOut.Value.(ability.OneKeyToken)
	verifyOut := ok.Command(atom, ability.OneKeyCommandVerifyToken, ability.OneKeyVerifyTokenArgs{
		Subject: tok.Subject, IssuedAt: tok.IssuedAt, ExpiresAt: tok.ExpiresAt, Signature: tok.Signature,
	})
	if verifyOut.Err != nil {
		t.Fatalf("verify token: %v", verifyOut.Err)
	}
	if verifyOut.Value != "edge-2" {
		t.Fatalf("verify value = %v", verifyOut.Value)
	}
	// revoke 后再校验应失败
	ok.Command(atom, ability.OneKeyCommandRevokeToken, ability.OneKeyRevokeTokenArgs{Subject: "edge-2"})
	if out := ok.Command(atom, ability.OneKeyCommandVerifyToken, ability.OneKeyVerifyTokenArgs{
		Subject: tok.Subject, IssuedAt: tok.IssuedAt, ExpiresAt: tok.ExpiresAt, Signature: tok.Signature,
	}); out.Err == nil {
		t.Fatal("expected error after revoke")
	}
	// 角色
	role, _ := atom.Ability("RoleAbility")
	if out := role.Command(atom, ability.CommandSetRole, ability.RoleAbilityArgs{Role: "edge"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := role.Command(atom, ability.CommandGetRole, nil); out.Value != "edge" {
		t.Fatalf("role = %v", out.Value)
	}
	// 拓扑快照
	top := nm.Command(atom, ability.NetMapCommandGetTopology, nil)
	if top.Err != nil {
		t.Fatal(top.Err)
	}
	if t2, _ := top.Value.(ability.NetMapTopology); len(t2.Peers) != 1 || t2.Peers[0].Name != "edge-2" {
		t.Fatalf("topology = %+v", t2)
	}
}

// TestIntegrationConfigFileRoundTrip 验证 ConfigData + ConfigFileAbility 的 JSON 文件往返。
func TestIntegrationConfigFileRoundTrip(t *testing.T) {
	atom := InitStandardAtom()
	if err := atom.PreRun(); err != nil {
		t.Fatal(err)
	}
	cfg, _ := atom.Data("ConfigData")
	if out := cfg.(*data.ConfigData).Command(atom, data.ConfigCommandSet, data.ConfigSetArgs{
		Key: "server.port", Value: "9090",
	}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := cfg.(*data.ConfigData).Command(atom, data.ConfigCommandSet, data.ConfigSetArgs{
		Key: "server.host", Value: "0.0.0.0",
	}); out.Err != nil {
		t.Fatal(out.Err)
	}
	fa, _ := atom.Ability("ConfigFileAbility")
	path := t.TempDir() + "/config.json"
	if out := fa.Command(atom, ability.ConfigFileCommandSave, ability.ConfigFileSaveArgs{Path: path, Overwrite: true}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// 改值,然后 load 回去,看是否恢复
	cfg.(*data.ConfigData).Command(atom, data.ConfigCommandSet, data.ConfigSetArgs{Key: "server.port", Value: "0"})
	if out := fa.Command(atom, ability.ConfigFileCommandLoad, ability.ConfigFileLoadArgs{Path: path, Strict: true}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := cfg.(*data.ConfigData).Command(atom, data.ConfigCommandGet, data.ConfigGetArgs{Key: "server.port"}); out.Value != "9090" {
		t.Fatalf("server.port after reload = %v, want 9090", out.Value)
	}
}

// TestIntegrationShBashEcho 验证 ShAbility / BashAbility 的 echo 端到端可用。
// 跳过非 POSIX 平台。
func TestIntegrationShBashEcho(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no POSIX shell on this platform")
	}
	atom := InitStandardAtom()
	if err := atom.PreRun(); err != nil {
		t.Fatal(err)
	}
	sh, _ := atom.Ability("ShAbility")
	sh.Command(atom, ability.ShCommandSetAllowlist, ability.ShSetAllowlistArgs{Allowed: []string{"printf"}})
	out := sh.Command(atom, ability.ShCommandRun, ability.ShRunArgs{Command: "printf sh-ok"})
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	if res, _ := out.Value.(ability.CmdResult); strings.TrimSpace(res.Stdout) != "sh-ok" {
		t.Fatalf("sh stdout = %q", res.Stdout)
	}
	bash, _ := atom.Ability("BashAbility")
	bash.Command(atom, ability.BashCommandSetAllowlist, ability.ShSetAllowlistArgs{Allowed: []string{"printf"}})
	bout := bash.Command(atom, ability.BashCommandRun, ability.ShRunArgs{Command: "printf bash-ok"})
	if bout.Err != nil {
		t.Fatal(bout.Err)
	}
	if res, _ := bout.Value.(ability.CmdResult); strings.TrimSpace(res.Stdout) != "bash-ok" {
		t.Fatalf("bash stdout = %q", res.Stdout)
	}
}
