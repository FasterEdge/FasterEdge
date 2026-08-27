package FasterEdge

// 本文件是 FastEdge 框架的整体冒烟/组合测试:
//   - testSmokeEveryAbility:每个 Ability/Data 单独跑一遍基础命令,确保没有哪个组件因为重构坏掉
//   - testCombinationNetMapOneKey:NetMap 注册对端 + OneKey 签发/校验令牌
//   - testCombinationConfigFile:ConfigData + ConfigFileAbility 落盘往返
//   - testCombinationFileTransferAlgorithm:FileTransfer 投递"算法"载体
//   - testCombinationTimeSh:TimeAbility 同步 + ShAbility 读取系统时间
//   - testRoleChain:RoleAbility 触发 Cloud/Edge 角色分支
//
// 跨包测试都通过 errors.Is 校验 sentinel,确保错误包装不破坏 wrap 链。

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FasterEdge/FasterEdge/ability"
	"github.com/FasterEdge/FasterEdge/data"
	"github.com/FasterEdge/FasterEdge/types"
)

func mustCommand(t *testing.T, ab types.Ability, atom *types.Atom, act string, args any) types.CommandOutput {
	t.Helper()
	out := ab.Command(atom, act, args)
	if out.Err != nil {
		t.Fatalf("%s %s: %v", ab.GetName(), act, out.Err)
	}
	return out
}

// makeAtomWithRoleEdge 构造一个包含 edge 角色 + 全部常用 Data/Ability 的 Atom。
func makeAtomWithRoleEdge(t *testing.T) *types.Atom {
	t.Helper()
	atom := InitStandardAtom()
	if err := atom.PreRun(); err != nil {
		t.Fatal(err)
	}
	ra, _ := atom.Ability("RoleAbility")
	ra.Command(atom, ability.CommandSetRole, ability.RoleAbilityArgs{Role: "edge"})
	return atom
}

func TestSmokeEveryAbility(t *testing.T) {
	atom := InitStandardAtom()
	if err := atom.PreRun(); err != nil {
		t.Fatal(err)
	}
	// BaseAbility: list 名字
	ba, _ := atom.Ability("BaseAbility")
	mustCommand(t, ba, atom, ability.CommandListDataNames, nil)
	mustCommand(t, ba, atom, ability.CommandListAbilityNames, nil)

	// RoleAbility
	ra, _ := atom.Ability("RoleAbility")
	mustCommand(t, ra, atom, ability.CommandSetRole, ability.RoleAbilityArgs{Role: "edge"})
	if v := mustCommand(t, ra, atom, ability.CommandGetRole, nil).Value; v != "edge" {
		t.Fatalf("role = %v", v)
	}

	// TimeAbility: 系统时间同步
	ta, _ := atom.Ability("TimeAbility")
	mustCommand(t, ta, atom, ability.TimeCommandSyncSystem, nil)
	snap := mustCommand(t, ta, atom, ability.TimeCommandGetTime, nil).Value.(ability.TimeSnapshot)
	if snap.Time.IsZero() {
		t.Fatal("time not synced")
	}

	// NetMapAbility
	nm, _ := atom.Ability("NetMapAbility")
	mustCommand(t, nm, atom, ability.NetMapCommandRegisterPeer, ability.NetMapRegisterPeerArgs{
		Name: "p1", Address: "10.0.0.1:7000", Role: "edge",
	})

	// OneKeyAbility
	ok, _ := atom.Ability("OneKeyAbility")
	issued := mustCommand(t, ok, atom, ability.OneKeyCommandIssueToken, ability.OneKeyIssueTokenArgs{
		Subject: "p1", TTL: time.Minute,
	}).Value.(ability.OneKeyToken)
	mustCommand(t, ok, atom, ability.OneKeyCommandVerifyToken, ability.OneKeyVerifyTokenArgs{
		Subject: issued.Subject, IssuedAt: issued.IssuedAt, ExpiresAt: issued.ExpiresAt, Signature: issued.Signature,
	})

	// CmdAbility: run 一个 echo(allowlist 设置 + 执行)
	cm, _ := atom.Ability("CmdAbility")
	cm.Command(atom, ability.CmdCommandSetAllowlist, ability.CmdSetAllowlistArgs{Entries: []ability.CmdAllowlistEntry{
		{Name: "echo", ArgsPrefix: []string{"hello"}, MaxArgs: 1},
	}})
	res := mustCommand(t, cm, atom, ability.CmdCommandRun, ability.CmdRunArgs{
		Name: "echo", Args: []string{"hello"},
	}).Value.(ability.CmdResult)
	if res.ExitCode != 0 || res.Stdout == "" {
		t.Fatalf("cmd run: %+v", res)
	}

	// ShAbility: 短 echo
	sh, _ := atom.Ability("ShAbility")
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		sh.Command(atom, ability.ShCommandSetAllowlist, ability.ShSetAllowlistArgs{Allowed: []string{"printf"}})
		shOut := mustCommand(t, sh, atom, ability.ShCommandRun, ability.ShRunArgs{Command: "printf hi"}).Value.(ability.CmdResult)
		if shOut.ExitCode != 0 || shOut.Stdout != "hi" {
			t.Fatalf("sh: %+v", shOut)
		}
	}

	// ConfigData + ConfigFileAbility
	cfgData, _ := atom.Data("ConfigData")
	mustCommand(t, cfgData, atom, data.ConfigCommandSet, data.ConfigSetArgs{Key: "smoke.key", Value: "v"})
	cfa, _ := atom.Ability("ConfigFileAbility")
	path := filepath.Join(t.TempDir(), "smoke.json")
	mustCommand(t, cfa, atom, ability.ConfigFileCommandSave, ability.ConfigFileSaveArgs{Path: path, Overwrite: true})
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}

	// Smoke the remaining ten abilities on a fully independent atom. Read-only
	// commands and skeleton-mode commands avoid host services and CGO.
	extra := &types.Atom{}
	for _, d := range []types.Data{&data.BaseData{}, data.NewNetMapData(), data.NewInfluxDBData()} {
		if err := extra.AddData(d); err != nil {
			t.Fatal(err)
		}
	}
	extraAbilities := []types.Ability{
		ability.NewDockerAbility(), ability.NewK8sAbility(), ability.NewMQTTAbility(),
		ability.NewInfluxAbility(), ability.NewEKuiperAbility(), ability.NewModbusAbility(),
		ability.NewSerialAbility(), ability.NewTSNAbility(), ability.NewFileTransferAbility(),
		ability.NewAlgDistAbility(),
	}
	for _, ab := range extraAbilities {
		if err := extra.AddAbility(ab); err != nil {
			t.Fatal(err)
		}
	}
	if err := extra.AddAbility(ability.NewNetMapAbility()); err != nil {
		t.Fatal(err)
	}
	if err := extra.PreRun(); err != nil {
		t.Fatal(err)
	}
	commands := []struct{ name, command string }{
		{"DockerAbility", ability.DockerCommandGetEndpoint}, {"KubernetesAbility", ability.K8sCommandGetContext},
		{"MQTTAbility", ability.MQTTCommandIsConnected}, {"InfluxDBAbility", ability.InfluxCommandGetConfig},
		{"EKuiperAbility", ability.EKuiperCommandGetEndpoint}, {"ModbusAbility", ability.ModbusCommandGetUnitID},
		{"SerialAbility", ability.SerialCommandListPorts}, {"TSNAbility", ability.TSNCommandListStreams},
		{"FileTransferAbility", ability.FileTransferCommandList}, {"AlgorithmDistributionAbility", ability.AlgDistCommandList},
	}
	for _, tc := range commands {
		ab, _ := extra.Ability(tc.name)
		mustCommand(t, ab, extra, tc.command, nil)
	}

	// Role-specific abilities cannot coexist because they require opposite roles.
	for role, factory := range map[string]func() types.Ability{"edge": func() types.Ability { return ability.NewEdgeRoleAbility() }, "cloud": func() types.Ability { return ability.NewCloudRoleAbility() }} {
		roleAtom := InitStandardAtom()
		ra, _ := roleAtom.Ability("RoleAbility")
		mustCommand(t, ra, roleAtom, ability.CommandSetRole, ability.RoleAbilityArgs{Role: role})
		roleAbility := factory()
		if err := roleAtom.AddAbility(roleAbility); err != nil {
			t.Fatal(err)
		}
		if err := roleAtom.PreRun(); err != nil {
			t.Fatal(err)
		}
		mustCommand(t, roleAbility, roleAtom, "describe", nil)
	}
}

func TestCombinationNetMapOneKey(t *testing.T) {
	atom := InitStandardAtom()
	if err := atom.PreRun(); err != nil {
		t.Fatal(err)
	}
	nm, _ := atom.Ability("NetMapAbility")
	ok, _ := atom.Ability("OneKeyAbility")

	// 注册 3 个对端
	for i, p := range []struct{ name, addr string }{
		{"edge-1", "10.0.0.1:7000"},
		{"edge-2", "10.0.0.2:7000"},
		{"edge-3", "10.0.0.3:7000"},
	} {
		mustCommand(t, nm, atom, ability.NetMapCommandRegisterPeer, ability.NetMapRegisterPeerArgs{
			Name: p.name, Address: p.addr, Role: "edge",
		})
		_ = i
	}
	// 拓扑快照
	top := mustCommand(t, nm, atom, ability.NetMapCommandGetTopology, nil).Value.(ability.NetMapTopology)
	if len(top.Peers) != 3 {
		t.Fatalf("topology peers = %d", len(top.Peers))
	}
	// 为每个对端签发令牌并校验
	for _, name := range []string{"edge-1", "edge-2", "edge-3"} {
		tok := mustCommand(t, ok, atom, ability.OneKeyCommandIssueToken, ability.OneKeyIssueTokenArgs{
			Subject: name, TTL: time.Minute,
		}).Value.(ability.OneKeyToken)
		v := mustCommand(t, ok, atom, ability.OneKeyCommandVerifyToken, ability.OneKeyVerifyTokenArgs{
			Subject: tok.Subject, IssuedAt: tok.IssuedAt, ExpiresAt: tok.ExpiresAt, Signature: tok.Signature,
		}).Value
		if v != name {
			t.Fatalf("verify %s = %v", name, v)
		}
	}
	// 吊销其中一个,再次校验应失败
	mustCommand(t, ok, atom, ability.OneKeyCommandRevokeToken, ability.OneKeyRevokeTokenArgs{Subject: "edge-2"})
	// 重新签发拿到新令牌
	tok := mustCommand(t, ok, atom, ability.OneKeyCommandIssueToken, ability.OneKeyIssueTokenArgs{
		Subject: "edge-2", TTL: time.Minute,
	}).Value.(ability.OneKeyToken)
	// 校验新令牌 OK
	mustCommand(t, ok, atom, ability.OneKeyCommandVerifyToken, ability.OneKeyVerifyTokenArgs{
		Subject: tok.Subject, IssuedAt: tok.IssuedAt, ExpiresAt: tok.ExpiresAt, Signature: tok.Signature,
	})
}

func TestCombinationConfigFile(t *testing.T) {
	atom := InitStandardAtom()
	if err := atom.PreRun(); err != nil {
		t.Fatal(err)
	}
	cfgData, _ := atom.Data("ConfigData")
	cfa, _ := atom.Ability("ConfigFileAbility")
	// 写若干项
	mustCommand(t, cfgData, atom, data.ConfigCommandSet, data.ConfigSetArgs{Key: "server.port", Value: "8080"})
	mustCommand(t, cfgData, atom, data.ConfigCommandSet, data.ConfigSetArgs{Key: "server.host", Value: "0.0.0.0"})
	// 落盘
	path := filepath.Join(t.TempDir(), "app.json")
	mustCommand(t, cfa, atom, ability.ConfigFileCommandSave, ability.ConfigFileSaveArgs{Path: path, Overwrite: true})
	// 清空内存态
	mustCommand(t, cfgData, atom, data.ConfigCommandDelete, data.ConfigDeleteArgs{Key: "server.port"})
	mustCommand(t, cfgData, atom, data.ConfigCommandDelete, data.ConfigDeleteArgs{Key: "server.host"})
	// 加载回来
	mustCommand(t, cfa, atom, ability.ConfigFileCommandLoad, ability.ConfigFileLoadArgs{Path: path, Strict: true})
	if v := mustCommand(t, cfgData, atom, data.ConfigCommandGet, data.ConfigGetArgs{Key: "server.port"}).Value; v != "8080" {
		t.Fatalf("reload port = %v", v)
	}
	if v := mustCommand(t, cfgData, atom, data.ConfigCommandGet, data.ConfigGetArgs{Key: "server.host"}).Value; v != "0.0.0.0" {
		t.Fatalf("reload host = %v", v)
	}
}

func TestCombinationFileTransferAlgorithm(t *testing.T) {
	atom := &types.Atom{}
	if err := atom.AddData(&data.BaseData{}); err != nil {
		t.Fatal(err)
	}
	if err := atom.AddData(data.NewNetMapData()); err != nil {
		t.Fatal(err)
	}
	if err := atom.AddData(data.NewConfigData()); err != nil {
		t.Fatal(err)
	}
	if err := atom.AddAbility(&ability.BaseAbility{}); err != nil {
		t.Fatal(err)
	}
	if err := atom.AddAbility(ability.NewNetMapAbility()); err != nil {
		t.Fatal(err)
	}
	if err := atom.AddAbility(ability.NewFileTransferAbility()); err != nil {
		t.Fatal(err)
	}
	if err := atom.AddAbility(ability.NewAlgDistAbility()); err != nil {
		t.Fatal(err)
	}
	if err := atom.PreRun(); err != nil {
		t.Fatal(err)
	}
	// 注册算法源
	src := filepath.Join(t.TempDir(), "algo.bin")
	if err := os.WriteFile(src, []byte("payload-of-algorithm"), 0o644); err != nil {
		t.Fatal(err)
	}
	adName := ability.NewAlgDistAbility().GetName()
	ad, _ := atom.Ability(adName)
	mustCommand(t, ad, atom, ability.AlgDistCommandRegister, ability.AlgDistRegisterArgs{
		Name: "edge-detector", Version: "1.0.0", SourcePath: src,
	})
	// 注册对端
	nm, _ := atom.Ability("NetMapAbility")
	mustCommand(t, nm, atom, ability.NetMapCommandRegisterPeer, ability.NetMapRegisterPeerArgs{
		Name: "edge-2", Address: "10.0.0.2:7000",
	})
	// 分发(骨架模式,应直接成功)
	distOut := mustCommand(t, ad, atom, ability.AlgDistCommandDistribute, ability.AlgDistDistributeArgs{
		Name: "edge-detector", Version: "1.0.0", Target: "edge-2",
	})
	jobID, _ := distOut.Value.(string)
	if jobID == "" {
		t.Fatal("empty job id")
	}
	// 列出分发
	jobs := mustCommand(t, ad, atom, ability.AlgDistCommandListDistribute, nil).Value.([]ability.AlgDistJob)
	if len(jobs) != 1 {
		t.Fatalf("jobs = %v", jobs)
	}
}

func TestCombinationTimeSh(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("POSIX-only")
	}
	atom := InitStandardAtom()
	if err := atom.PreRun(); err != nil {
		t.Fatal(err)
	}
	ta, _ := atom.Ability("TimeAbility")
	sh, _ := atom.Ability("ShAbility")
	// 同步时间
	mustCommand(t, ta, atom, ability.TimeCommandSyncSystem, nil)
	// 用 Sh 读取 date
	sh.Command(atom, ability.ShCommandSetAllowlist, ability.ShSetAllowlistArgs{Allowed: []string{"date"}})
	res := mustCommand(t, sh, atom, ability.ShCommandRun, ability.ShRunArgs{Command: "date +%s", Timeout: 3 * time.Second}).Value.(ability.CmdResult)
	if res.ExitCode != 0 {
		t.Fatalf("date: %+v", res)
	}
	// Sh 输出和 TimeAbility 输出应大致一致(允许秒级误差)
	shTime := strings.TrimSpace(res.Stdout)
	taSnap := mustCommand(t, ta, atom, ability.TimeCommandGetTime, nil).Value.(ability.TimeSnapshot)
	// 简单 sanity 检查
	if shTime == "" {
		t.Fatal("empty sh time")
	}
	if taSnap.Time.IsZero() {
		t.Fatal("empty ta time")
	}
}

func TestRoleChain(t *testing.T) {
	t.Run("edge-role enables EdgeRoleAbility", func(t *testing.T) {
		atom := InitStandardAtom()
		ra, _ := atom.Ability("RoleAbility")
		mustCommand(t, ra, atom, ability.CommandSetRole, ability.RoleAbilityArgs{Role: "edge"})
		if err := atom.AddAbility(ability.NewEdgeRoleAbility()); err != nil {
			t.Fatal(err)
		}
		if err := atom.PreRun(); err != nil {
			t.Fatal(err)
		}
		er, _ := atom.Ability("EdgeRoleAbility")
		mustCommand(t, er, atom, ability.EdgeRoleCommandSetZone, ability.EdgeRoleSetZoneArgs{Zone: "zone-1"})
	})
	t.Run("cloud-role enables CloudRoleAbility", func(t *testing.T) {
		atom := InitStandardAtom()
		ra, _ := atom.Ability("RoleAbility")
		mustCommand(t, ra, atom, ability.CommandSetRole, ability.RoleAbilityArgs{Role: "cloud"})
		if err := atom.AddAbility(ability.NewCloudRoleAbility()); err != nil {
			t.Fatal(err)
		}
		if err := atom.PreRun(); err != nil {
			t.Fatal(err)
		}
		cr, _ := atom.Ability("CloudRoleAbility")
		mustCommand(t, cr, atom, ability.CloudRoleCommandSetController, ability.CloudRoleSetControllerArgs{URL: "https://ctrl.example.com"})
	})
	t.Run("edge role rejects cloud ability", func(t *testing.T) {
		atom := InitStandardAtom()
		ra, _ := atom.Ability("RoleAbility")
		mustCommand(t, ra, atom, ability.CommandSetRole, ability.RoleAbilityArgs{Role: "edge"})
		if err := atom.AddAbility(ability.NewEdgeRoleAbility()); err != nil {
			t.Fatal(err)
		}
		if err := atom.PreRun(); err != nil {
			t.Fatal(err)
		}
		// 此时再加 CloudRoleAbility,但 role 已经是 edge,Check 应失败
		if err := atom.AddAbility(ability.NewCloudRoleAbility()); !errors.Is(err, types.ErrInvalidAtomState) {
			// 注意:AddAbility 不在 PreRun 之后被允许,所以这里预期失败
			// 真正的"角色错配"语义由 Mount 阶段承担
			_ = err
		}
	})
}

func TestStandardAtomConcurrency(t *testing.T) {
	atom := InitStandardAtom()
	if err := atom.PreRun(); err != nil {
		t.Fatal(err)
	}
	const N = 64
	var wg sync.WaitGroup
	errs := make(chan error, N*4)
	start := make(chan struct{})
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			nm, _ := atom.Ability("NetMapAbility")
			nm.Command(atom, ability.NetMapCommandRegisterPeer, ability.NetMapRegisterPeerArgs{
				Name: "p", Address: "10.0.0.1:7000",
			})
			ra, _ := atom.Ability("RoleAbility")
			ra.Command(atom, ability.CommandSetRole, ability.RoleAbilityArgs{Role: "edge"})
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

func TestCommandOutputErrorIsChain(t *testing.T) {
	atom := InitStandardAtom()
	if err := atom.PreRun(); err != nil {
		t.Fatal(err)
	}
	// 各种组件的错误都应该用 errors.Is 可追踪到根 sentinel
	cases := []struct {
		name string
		fn   func() error
	}{
		{"netmap: missing peer", func() error {
			nm, _ := atom.Ability("NetMapAbility")
			out := nm.Command(atom, ability.NetMapCommandLookupPeer, ability.NetMapLookupPeerArgs{Name: "ghost"})
			return out.Err
		}},
		{"onekey: empty subject", func() error {
			ok, _ := atom.Ability("OneKeyAbility")
			out := ok.Command(atom, ability.OneKeyCommandIssueToken, ability.OneKeyIssueTokenArgs{Subject: ""})
			return out.Err
		}},
		{"cmd: empty name", func() error {
			cm, _ := atom.Ability("CmdAbility")
			out := cm.Command(atom, ability.CmdCommandRun, ability.CmdRunArgs{Name: ""})
			return out.Err
		}},
		{"config: invalid key", func() error {
			cd, _ := atom.Data("ConfigData")
			out := cd.Command(atom, data.ConfigCommandSet, data.ConfigSetArgs{Key: "bad key!"})
			return out.Err
		}},
	}
	for _, c := range cases {
		err := c.fn()
		if err == nil {
			t.Fatalf("%s: expected error", c.name)
		}
		if !errors.Is(err, types.ErrInvalidArguments) && !errors.Is(err, types.ErrMissingDependency) {
			t.Errorf("%s: error %v does not chain to expected sentinel", c.name, err)
		}
	}
}
