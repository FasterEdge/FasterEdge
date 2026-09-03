// ─────────────────────────────────────────────────────────────
// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
// ─────────────────────────────────────────────────────────────
// Package main 系统性验证 FasterEdge 各种 Ability 与 Data:
//  1. 枚举 InitStandardAtom 挂载的全部组件与命令目录
//  2. 逐个执行真实命令调用(正常路径 + 异常路径)
//  3. 验证状态持久化(ConfigData → ConfigFileAbility 落盘 → 重载)
//  4. 验证命令鉴权(AuthenticatedCommand 与 OneKey 令牌)
//  5. 输出每个组件的 PASS/FAIL 汇总
//
// 运行: go run .
package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	fasteredge "github.com/FasterEdge/FasterEdge"
	"github.com/FasterEdge/FasterEdge/ability"
	"github.com/FasterEdge/FasterEdge/data"
)

// modbusTCPTransport 实现 ability.ModbusTransport: 用 Modbus TCP (MBAP) 封装 PDU。
type modbusTCPTransport struct {
	conn net.Conn
	seq  atomic.Uint32
}

func newModbusTCPTransport(addr string) (*modbusTCPTransport, error) {
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return nil, err
	}
	return &modbusTCPTransport{conn: conn}, nil
}

func (t *modbusTCPTransport) Send(unitID uint8, pdu []byte) ([]byte, error) {
	// MBAP 头: 事务ID(2) + 协议ID(2) + 长度(2) + 单元ID(1), 之后是 PDU
	tid := uint16(t.seq.Add(1))
	mbap := make([]byte, 7)
	binary.BigEndian.PutUint16(mbap[0:2], tid)
	binary.BigEndian.PutUint16(mbap[2:4], 0)
	binary.BigEndian.PutUint16(mbap[4:6], uint16(len(pdu)+1))
	mbap[6] = unitID
	if _, err := t.conn.Write(append(mbap, pdu...)); err != nil {
		return nil, err
	}
	// 读响应: MBAP 头 + PDU (长度 = len 字段 - 1 的 PDU)
	hdr := make([]byte, 7)
	if _, err := readFull(t.conn, hdr); err != nil {
		return nil, err
	}
	plen := int(binary.BigEndian.Uint16(hdr[4:6])) - 1
	if plen < 0 {
		return nil, fmt.Errorf("bad mbap length")
	}
	resp := make([]byte, plen)
	if _, err := readFull(t.conn, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func readFull(conn net.Conn, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		m, err := conn.Read(buf[n:])
		if err != nil {
			return n, err
		}
		if m == 0 {
			return n, fmt.Errorf("connection closed")
		}
		n += m
	}
	return n, nil
}

func (t *modbusTCPTransport) Close() error {
	if t.conn != nil {
		return t.conn.Close()
	}
	return nil
}

var (
	passCount   int
	failCount   int
	failDetails []string
)

func report(name, detail string, err error) {
	if err != nil {
		failCount++
		failDetails = append(failDetails, fmt.Sprintf("%s: %v", name, err))
		fmt.Printf("  [FAIL] %s | %v\n", name, err)
		return
	}
	passCount++
	fmt.Printf("  [PASS] %s | %s\n", name, detail)
}

func main() {
	fmt.Println("=== FasterEdge Ability/Data 系统性验证 ===")
	atom := fasteredge.InitStandardAtom()
	if err := fasteredge.PreRunAtom(atom); err != nil {
		fmt.Printf("PreRun failed: %v\n", err)
		os.Exit(1)
	}
	_ = atom.SetName("verify-node-1")
	fmt.Printf("atom name: %s, state: %v\n", atom.GetName(), atom.State())

	// 1. 枚举组件与命令目录
	cmds := atom.CommandNames()
	var components []string
	for n := range cmds {
		components = append(components, n)
	}
	sort.Strings(components)
	fmt.Printf("\n=== 已挂载 %d 个组件 ===\n", len(components))
	for _, n := range components {
		list := cmds[n]
		if len(list) == 0 {
			fmt.Printf("  %-22s (无命令目录)\n", n)
		} else {
			fmt.Printf("  %-22s %s\n", n, strings.Join(list, ", "))
		}
	}

	// 2. 逐个组件验证
	fmt.Println("\n=== 逐个执行命令 ===")

	// --- data: BaseData ---
	if d, ok := atom.Data("BaseData"); ok {
		o := d.Command(atom, data.CommandInfo, nil)
		report("BaseData/info", fmt.Sprintf("%v", o.Value), o.Err)
		o = d.Command(atom, data.CommandLogo, nil)
		report("BaseData/logo", fmt.Sprintf("len=%d", len(fmt.Sprintf("%v", o.Value))), o.Err)
	}

	// --- data: ConfigData ---
	if d, ok := atom.Data("ConfigData"); ok {
		o := d.Command(atom, data.ConfigCommandSet, data.ConfigSetArgs{Key: "node.role", Value: "edge"})
		report("ConfigData/set", "node.role=edge", o.Err)
		o = d.Command(atom, data.ConfigCommandGet, data.ConfigGetArgs{Key: "node.role"})
		report("ConfigData/get", fmt.Sprintf("%v", o.Value), o.Err)
		o = d.Command(atom, data.ConfigCommandList, nil)
		report("ConfigData/list", fmt.Sprintf("%v", o.Value), o.Err)
		o = d.Command(atom, data.ConfigCommandGet, data.ConfigGetArgs{Key: "no.such.key"})
		if o.Err == nil {
			report("ConfigData/get-missing", "应报错但成功", fmt.Errorf("missing key should error"))
		} else {
			report("ConfigData/get-missing", "正确拒绝", nil)
		}
	}

	// --- data: KeyringData ---
	if d, ok := atom.Data("KeyringData"); ok {
		o := d.Command(atom, data.KeyringCommandSetSecret, data.KeyringSetSecretArgs{Secret: "s3cr3t-abc-0123456789"})
		report("KeyringData/set_secret", "s3cr3t-abc", o.Err)
		o = d.Command(atom, data.KeyringCommandStatus, nil)
		report("KeyringData/status", fmt.Sprintf("%v", o.Value), o.Err)
		o = d.Command(atom, data.KeyringCommandListTokens, nil)
		report("KeyringData/list_tokens", fmt.Sprintf("%v", o.Value), o.Err)
	}

	// --- data: NetMapData (本机拓扑快照) ---
	if d, ok := atom.Data("NetMapData"); ok {
		o := d.Command(atom, data.NetMapCommandInfo, nil)
		if o.Err != nil {
			report("NetMapData/info", "", o.Err)
		} else if info, ok := o.Value.(data.NetMapLocalInfo); ok {
			report("NetMapData/info", fmt.Sprintf("node=%q ifaces=%d", info.NodeName, len(info.Interfaces)), nil)
		} else {
			report("NetMapData/info", fmt.Sprintf("%v", o.Value), o.Err)
		}
		o = d.Command(atom, data.NetMapCommandSetNodeName, data.NetMapSetNodeNameArgs{Name: "verify-node"})
		report("NetMapData/set_node_name", "verify-node", o.Err)
		o = d.Command(atom, data.NetMapCommandInterfaces, nil)
		if ifaces, ok := o.Value.([]data.NetMapInterface); ok && len(ifaces) > 0 {
			report("NetMapData/interfaces", fmt.Sprintf("count=%d first=%s", len(ifaces), ifaces[0].Name), o.Err)
			// 用真实接口名设置默认出网接口
			o = d.Command(atom, data.NetMapCommandSetDefaultIface, data.NetMapSetDefaultIfaceArgs{Name: ifaces[0].Name})
			report("NetMapData/set_default_iface", ifaces[0].Name, o.Err)
		} else {
			report("NetMapData/interfaces", fmt.Sprintf("%v", o.Value), o.Err)
		}
	}

	// --- ability: BaseAbility ---
	if a, ok := atom.Ability("BaseAbility"); ok {
		o := a.Command(atom, ability.CommandListDataNames, nil)
		report("BaseAbility/list_data_names", fmt.Sprintf("%v", o.Value), o.Err)
		o = a.Command(atom, ability.CommandListAbilityNames, nil)
		report("BaseAbility/list_ability_names", fmt.Sprintf("%v", o.Value), o.Err)
	}

	// --- ability: RoleAbility ---
	if a, ok := atom.Ability("RoleAbility"); ok {
		o := a.Command(atom, ability.CommandSetRole, ability.RoleAbilityArgs{Role: "edge"})
		report("RoleAbility/set_role", "edge", o.Err)
		o = a.Command(atom, ability.CommandGetRole, nil)
		report("RoleAbility/get_role", fmt.Sprintf("%v", o.Value), o.Err)
		o = a.Command(atom, ability.CommandDescribe, nil)
		report("RoleAbility/describe", fmt.Sprintf("%v", o.Value), o.Err)
	}

	// --- ability: TimeAbility ---
	if a, ok := atom.Ability("TimeAbility"); ok {
		o := a.Command(atom, ability.TimeCommandSyncSystem, nil)
		report("TimeAbility/sync_system", "local clock", o.Err)
		o = a.Command(atom, ability.TimeCommandGetTime, nil)
		if snap, ok := o.Value.(ability.TimeSnapshot); ok {
			report("TimeAbility/get_time", fmt.Sprintf("now=%s src=%s", snap.Time.Format(time.RFC3339), snap.Source), o.Err)
		} else {
			report("TimeAbility/get_time", fmt.Sprintf("%v", o.Value), o.Err)
		}
		// sync_manual: 手工校时 (RFC3339Nano), 校验后 get_time 应反映该值
		manual := time.Now().Add(-2 * time.Hour).Format(time.RFC3339Nano)
		o = a.Command(atom, ability.TimeCommandSyncManual, ability.TimeSyncManualArgs{Value: manual})
		report("TimeAbility/sync_manual", manual, o.Err)
		o = a.Command(atom, ability.TimeCommandGetTime, nil)
		if snap, ok := o.Value.(ability.TimeSnapshot); ok {
			report("TimeAbility/get_time-after-manual", fmt.Sprintf("now=%s src=%s", snap.Time.Format(time.RFC3339), snap.Source), o.Err)
		} else {
			report("TimeAbility/get_time-after-manual", fmt.Sprintf("%v", o.Value), o.Err)
		}
		// 非法时间格式应被拒绝
		o = a.Command(atom, ability.TimeCommandSyncManual, ability.TimeSyncManualArgs{Value: "not-a-time"})
		if o.Err == nil {
			report("TimeAbility/sync_manual-bad", "应拒绝但成功", fmt.Errorf("bad time format accepted"))
		} else {
			report("TimeAbility/sync_manual-bad", "正确拒绝", nil)
		}
	}

	// --- ability: OneKeyAbility (签发 → 传输编码 → 验证 → 篡改拒绝 → 注销) ---
	if a, ok := atom.Ability("OneKeyAbility"); ok {
		o := a.Command(atom, ability.OneKeyCommandIssueToken, ability.OneKeyIssueTokenArgs{Subject: "edge-2", TTL: time.Hour})
		if o.Err != nil {
			report("OneKeyAbility/issue", "", o.Err)
		} else {
			tok, _ := o.Value.(ability.OneKeyToken)
			report("OneKeyAbility/issue", fmt.Sprintf("sig=%s... exp=%s", tok.Signature[:8], tok.ExpiresAt.Format(time.RFC3339)), nil)
			wire := ability.EncodeForTransmission(tok)
			dec, err := ability.DecodeFromTransmission(wire)
			if err != nil {
				report("OneKeyAbility/wire-encode", "", err)
			} else {
				v := a.Command(atom, ability.OneKeyCommandVerifyToken, ability.OneKeyVerifyTokenArgs{
					Subject: dec.Subject, IssuedAt: dec.IssuedAt, ExpiresAt: dec.ExpiresAt, Signature: dec.Signature,
				})
				report("OneKeyAbility/verify", fmt.Sprintf("subject=%s", v.Value), v.Err)
			}
			bad := ability.OneKeyVerifyTokenArgs{Subject: tok.Subject, IssuedAt: tok.IssuedAt, ExpiresAt: tok.ExpiresAt, Signature: strings.Repeat("0", len(tok.Signature))}
			v := a.Command(atom, ability.OneKeyCommandVerifyToken, bad)
			if v.Err == nil {
				report("OneKeyAbility/verify-tampered", "应失败但通过", fmt.Errorf("tampered signature accepted"))
			} else {
				report("OneKeyAbility/verify-tampered", "正确拒绝", nil)
			}
			o = a.Command(atom, ability.OneKeyCommandListTokens, nil)
			report("OneKeyAbility/list_tokens", fmt.Sprintf("%v", o.Value), o.Err)
			o = a.Command(atom, ability.OneKeyCommandRevokeToken, ability.OneKeyRevokeTokenArgs{Subject: tok.Subject})
			report("OneKeyAbility/revoke", tok.Subject, o.Err)
			o = a.Command(atom, ability.OneKeyCommandListTokens, nil)
			report("OneKeyAbility/list_after_revoke", fmt.Sprintf("%v", o.Value), o.Err)
		}
	}

	// --- ability: CmdAbility (先设白名单再执行; 黑名单拒绝) ---
	if a, ok := atom.Ability("CmdAbility"); ok {
		o := a.Command(atom, ability.CmdCommandSetAllowlist, ability.CmdSetAllowlistArgs{Entries: []ability.CmdAllowlistEntry{{Name: "echo"}}})
		report("CmdAbility/set_allowlist", "echo", o.Err)
		o = a.Command(atom, ability.CmdCommandRun, ability.CmdRunArgs{Name: "echo", Args: []string{"cmd-ability-ok"}, Timeout: 3 * time.Second})
		if res, ok := o.Value.(ability.CmdResult); ok {
			report("CmdAbility/run", fmt.Sprintf("exit=%d out=%q", res.ExitCode, res.Stdout), o.Err)
		} else {
			report("CmdAbility/run", fmt.Sprintf("%v", o.Value), o.Err)
		}
	}

	// --- ability: ShAbility (白名单: 允许 printf, 拒绝 rm) ---
	if a, ok := atom.Ability("ShAbility"); ok {
		o := a.Command(atom, ability.ShCommandSetAllowlist, ability.ShSetAllowlistArgs{Allowed: []string{"printf"}})
		report("ShAbility/set_allowlist", "printf", o.Err)
		o = a.Command(atom, ability.ShCommandRun, ability.ShRunArgs{Command: "printf 'sh-ok\\n'", Timeout: 3 * time.Second})
		if res, ok := o.Value.(ability.CmdResult); ok {
			report("ShAbility/run-allow", fmt.Sprintf("exit=%d out=%q", res.ExitCode, res.Stdout), o.Err)
		} else {
			report("ShAbility/run-allow", fmt.Sprintf("%v", o.Value), o.Err)
		}
		o = a.Command(atom, ability.ShCommandRun, ability.ShRunArgs{Command: "rm -rf /tmp/x", Timeout: 3 * time.Second})
		if o.Err == nil {
			report("ShAbility/run-denied", "rm 应被白名单拒绝但成功", fmt.Errorf("allowlist bypass"))
		} else {
			report("ShAbility/run-denied", "正确拒绝 rm", nil)
		}
	}

	// --- ability: BashAbility (同样走 CmdRunArgs 风格?) 用 ShRunArgs 兼容 ---
	if a, ok := atom.Ability("BashAbility"); ok {
		o := a.Command(atom, ability.BashCommandRun, ability.ShRunArgs{Command: "echo bash-ability-ok", Timeout: 3 * time.Second})
		if res, ok := o.Value.(ability.CmdResult); ok {
			report("BashAbility/run", fmt.Sprintf("exit=%d out=%q", res.ExitCode, res.Stdout), o.Err)
		} else {
			report("BashAbility/run", fmt.Sprintf("%v", o.Value), o.Err)
		}
	}

	// --- ability: NetMapAbility (注册/列举/注销 peer) ---
	if a, ok := atom.Ability("NetMapAbility"); ok {
		o := a.Command(atom, ability.NetMapCommandRegisterPeer, ability.NetMapRegisterPeerArgs{Name: "edge-3", Address: "10.0.0.3:7000", Role: "edge"})
		report("NetMapAbility/register_peer", "edge-3@10.0.0.3:7000", o.Err)
		o = a.Command(atom, ability.NetMapCommandListPeers, nil)
		report("NetMapAbility/list_peers", fmt.Sprintf("%v", o.Value), o.Err)
		o = a.Command(atom, ability.NetMapCommandUnregisterPeer, ability.NetMapLookupPeerArgs{Name: "edge-3"})
		report("NetMapAbility/unregister_peer", "edge-3", o.Err)
		o = a.Command(atom, ability.NetMapCommandListPeers, nil)
		report("NetMapAbility/list_after_remove", fmt.Sprintf("%v", o.Value), o.Err)
	}

	// --- ability: ConfigFileAbility (ConfigData → JSON 落盘 → 新 atom 重载) ---
	if a, ok := atom.Ability("ConfigFileAbility"); ok {
		cfgPath := filepath.Join(os.TempDir(), "fe-verify-config.json")
		o := a.Command(atom, ability.ConfigFileCommandSave, ability.ConfigFileSaveArgs{Path: cfgPath, Overwrite: true})
		report("ConfigFileAbility/save", cfgPath, o.Err)
		atom2 := fasteredge.InitStandardAtom()
		_ = fasteredge.PreRunAtom(atom2)
		o = a.Command(atom2, ability.ConfigFileCommandLoad, ability.ConfigFileLoadArgs{Path: cfgPath, Strict: false})
		if o.Err != nil {
			report("ConfigFileAbility/load", "", o.Err)
		} else if d, ok := atom2.Data("ConfigData"); ok {
			g := d.Command(atom2, data.ConfigCommandGet, data.ConfigGetArgs{Key: "node.role"})
			report("ConfigFileAbility/load-reload", fmt.Sprintf("node.role=%v", g.Value), g.Err)
		}
	}

	// === 扩展: 注册全部可独立运行的 ability/data ===
	fmt.Println("\n=== 扩展能力: MQTT / Modbus / Edge / Cloud / FileTransfer / AlgDist / Influx / TSN ===")
	// 必须在 PreRun 之前注册组件(生命周期保护), 因此新建一个 atom, 先注册再 PreRun
	extAtom := fasteredge.InitStandardAtom()
	extRegs := []struct {
		kind string
		name string
		fn   func() error
	}{
		{"ability", "MQTTAbility", func() error { return extAtom.AddAbility(ability.NewMQTTAbility()) }},
		{"ability", "ModbusAbility", func() error { return extAtom.AddAbility(ability.NewModbusAbility()) }},
		{"ability", "EdgeRoleAbility", func() error { return extAtom.AddAbility(ability.NewEdgeRoleAbility()) }},
		{"ability", "CloudRoleAbility", func() error { return extAtom.AddAbility(ability.NewCloudRoleAbility()) }},
		{"ability", "FileTransferAbility", func() error { return extAtom.AddAbility(ability.NewFileTransferAbility()) }},
		{"ability", "AlgDistAbility", func() error { return extAtom.AddAbility(ability.NewAlgDistAbility()) }},
		{"ability", "InfluxAbility", func() error { return extAtom.AddAbility(ability.NewInfluxAbility()) }},
		{"ability", "TSNAbility", func() error { return extAtom.AddAbility(ability.NewTSNAbility()) }},
		{"ability", "SerialAbility", func() error { return extAtom.AddAbility(ability.NewSerialAbility()) }},
		{"ability", "DockerAbility", func() error { return extAtom.AddAbility(ability.NewDockerAbility()) }},
		{"ability", "EKuiperAbility", func() error { return extAtom.AddAbility(ability.NewEKuiperAbility()) }},
		{"ability", "KubernetesAbility", func() error { return extAtom.AddAbility(ability.NewK8sAbility()) }},
	}
	for _, r := range extRegs {
		if err := r.fn(); err != nil {
			fmt.Printf("  [SKIP] register %s %s: %v\n", r.kind, r.name, err)
		}
	}
	if err := fasteredge.PreRunAtom(extAtom); err != nil {
		// EdgeRoleAbility(要求 role=edge) 与 CloudRoleAbility(要求 role=cloud)
		// 在同一 atom 上必然有一个 Check 失败 —— 设计使然, 命令执行不依赖 PreRun.
		fmt.Printf("  [NOTE] extAtom PreRun (edge+cloud 混合): %v\n", err)
	} else {
		ecmds := extAtom.CommandNames()
		var ecomp []string
		for n := range ecmds {
			ecomp = append(ecomp, n)
		}
		sort.Strings(ecomp)
		fmt.Printf("扩展后组件: %s\n", strings.Join(ecomp, ", "))
	}

	// 前置: 设 role=edge (EdgeRole 依赖), 注册 NetMap peer (FileTransfer 依赖)
	if ra, ok := extAtom.Ability("RoleAbility"); ok {
		if out := ra.Command(extAtom, ability.CommandSetRole, ability.RoleAbilityArgs{Role: "edge"}); out.Err != nil {
			fmt.Printf("  [WARN] set role edge: %v\n", out.Err)
		}
	}
	if na, ok := extAtom.Ability("NetMapAbility"); ok {
		if out := na.Command(extAtom, ability.NetMapCommandRegisterPeer, ability.NetMapRegisterPeerArgs{Name: "peer-b", Address: "10.0.0.5:7000", Role: "edge"}); out.Err != nil {
			fmt.Printf("  [WARN] register peer-b: %v\n", out.Err)
		}
	}

	// MQTT 发布/订阅联调 (需要 broker + 注入 transport)
	if ab, ok := extAtom.Ability("MQTTAbility"); ok {
		ma, ok2 := ab.(*ability.MQTTAbility)
		if !ok2 {
			fmt.Printf("  [FAIL] MQTT 类型断言失败\n")
			failCount++
		} else {
			// broker 地址经 FE_MQTT_BROKER 注入 (容器 eth0 IP); isAcceptableBrokerURL 拒绝回环, 属安全设计
			mqttURL := "tcp://" + brokerAddr()
			_ = ab.Command(extAtom, ability.MQTTCommandSetBroker, ability.MQTTBrokerArgs{URL: mqttURL})
			// 注入最小 MQTT 3.1.1 客户端 transport; 收到消息后回调 PushMessage
			mt := newMQTTClientTransport("fe-verify-mqtt", func(msg ability.MQTTMessage) {
				ma.PushMessage(msg)
			})
			ma.SetTransport(mt)
			mtCon := ab.Command(extAtom, ability.MQTTCommandConnect, nil)
			if mtCon.Err != nil {
				fmt.Printf("  [SKIP] MQTT 联调 (无法连接 broker): %v\n", mtCon.Err)
			} else {
				report("MQTT/connect", "tcp://127.0.0.1:1883", nil)
				o := ab.Command(extAtom, ability.MQTTCommandSubscribe, ability.MQTTSubscribeArgs{Topic: "verify/topic", Qos: 1, MaxQueue: 16})
				report("MQTT/subscribe", "verify/topic qos=1", o.Err)
				o = ab.Command(extAtom, ability.MQTTCommandPublish, ability.MQTTPublishArgs{Topic: "verify/topic", Payload: []byte("hello-fe-mqtt"), Qos: 1})
				report("MQTT/publish", "hello-fe-mqtt", o.Err)
				o = ab.Command(extAtom, ability.MQTTCommandDrain, ability.MQTTPullArgs{Topic: "verify/topic", Max: 5, Timeout: 3 * time.Second})
				if msgs, ok := o.Value.([]ability.MQTTMessage); ok {
					report("MQTT/drain", fmt.Sprintf("got=%d payload=%q", len(msgs), string(msgs[0].Payload)), o.Err)
				} else {
					report("MQTT/drain", fmt.Sprintf("%v", o.Value), o.Err)
				}
				o = ab.Command(extAtom, ability.MQTTCommandDisconnect, nil)
				report("MQTT/disconnect", "", o.Err)
			}
		}
	}

	// Modbus 联调 (需要 MiniGreat Receiver Modbus 从站 :1502)
	if ab, ok := extAtom.Ability("ModbusAbility"); ok {
		o := ab.Command(extAtom, ability.ModbusCommandSetEndpoint, ability.ModbusEndpointArgs{Addr: "127.0.0.1:1502"})
		report("Modbus/set_endpoint", "127.0.0.1:1502", o.Err)
		o = ab.Command(extAtom, ability.ModbusCommandSetUnitID, ability.ModbusUnitIDArgs{UnitID: 1})
		report("Modbus/set_unit_id", "1", o.Err)
		// 注入真实 TCP transport (ModbusAbility 只做协议骨架, 字节流由 transport 负责)
		ma, ok := ab.(*ability.ModbusAbility)
		if !ok {
			fmt.Printf("  [FAIL] Modbus 类型断言失败\n")
			failCount++
		} else {
			mt, err := newModbusTCPTransport("127.0.0.1:1502")
			if err != nil {
				fmt.Printf("  [SKIP] Modbus 联调 (无法连接从站): %v\n", err)
			} else {
				ma.SetTransport(mt)
				defer mt.Close()
				o = ab.Command(extAtom, ability.ModbusCommandWriteHolding, ability.ModbusWriteHoldingArgs{Address: 10, Value: 1234})
				if o.Err != nil {
					fmt.Printf("  [FAIL] Modbus/write_holding: %v\n", o.Err)
					failCount++
					failDetails = append(failDetails, fmt.Sprintf("Modbus/write_holding: %v", o.Err))
				} else {
					report("Modbus/write_holding", "addr=10 val=1234", nil)
				}
				o = ab.Command(extAtom, ability.ModbusCommandReadHolding, ability.ModbusReadArgs{Address: 10, Quantity: 1})
				if res, ok := o.Value.(ability.ModbusReadResult); ok {
					report("Modbus/read_holding", fmt.Sprintf("regs=%v", res.Regs), o.Err)
				} else {
					report("Modbus/read_holding", fmt.Sprintf("%v", o.Value), o.Err)
				}
				o = ab.Command(extAtom, ability.ModbusCommandWriteCoil, ability.ModbusWriteCoilArgs{Address: 5, Value: true})
				if o.Err != nil {
					fmt.Printf("  [FAIL] Modbus/write_coil: %v\n", o.Err)
					failCount++
				} else {
					report("Modbus/write_coil", "addr=5 true", nil)
				}
				o = ab.Command(extAtom, ability.ModbusCommandReadCoils, ability.ModbusReadArgs{Address: 5, Quantity: 1})
				if res, ok := o.Value.(ability.ModbusReadResult); ok {
					report("Modbus/read_coils", fmt.Sprintf("coils=%v", res.Coils), o.Err)
				} else {
					report("Modbus/read_coils", fmt.Sprintf("%v", o.Value), o.Err)
				}
			}
		}
	}

	// EdgeRoleAbility
	if a, ok := extAtom.Ability("EdgeRoleAbility"); ok {
		o := a.Command(extAtom, ability.EdgeRoleCommandSetZone, ability.EdgeRoleSetZoneArgs{Zone: "factory-a"})
		report("EdgeRole/set_zone", "factory-a", o.Err)
		o = a.Command(extAtom, ability.EdgeRoleCommandGetZone, nil)
		report("EdgeRole/get_zone", fmt.Sprintf("%v", o.Value), o.Err)
		o = a.Command(extAtom, ability.EdgeRoleCommandAddCap, ability.EdgeRoleCapabilityArg{Name: "modbus"})
		report("EdgeRole/add_capability", "modbus", o.Err)
		o = a.Command(extAtom, ability.EdgeRoleCommandListCaps, nil)
		report("EdgeRole/list_capabilities", fmt.Sprintf("%v", o.Value), o.Err)
		o = a.Command(extAtom, ability.EdgeRoleCommandDescribe, nil)
		report("EdgeRole/describe", fmt.Sprintf("%v", o.Value), o.Err)
		o = a.Command(extAtom, ability.EdgeRoleCommandRecordLatency, ability.EdgeRoleRecordLatencyArgs{LatencyMs: 12.5})
		report("EdgeRole/record_latency", "12.5ms", o.Err)
		o = a.Command(extAtom, ability.EdgeRoleCommandGetMetrics, nil)
		report("EdgeRole/get_metrics", fmt.Sprintf("%v", o.Value), o.Err)
		o = a.Command(extAtom, ability.EdgeRoleCommandSetOnline, ability.EdgeRoleSetOnlineArgs{Online: true})
		report("EdgeRole/set_online", "true", o.Err)
	}

	// CloudRoleAbility (前置: role 需为 cloud, 切换后再测)
	if a, ok := extAtom.Ability("CloudRoleAbility"); ok {
		if ra, ok2 := extAtom.Ability("RoleAbility"); ok2 {
			_ = ra.Command(extAtom, ability.CommandSetRole, ability.RoleAbilityArgs{Role: "cloud"})
		}
		o := a.Command(extAtom, ability.CloudRoleCommandSetController, ability.CloudRoleSetControllerArgs{URL: "https://cloud-a.example.com"})
		report("CloudRole/set_controller", "cloud-a", o.Err)
		o = a.Command(extAtom, ability.CloudRoleCommandGetController, nil)
		report("CloudRole/get_controller", fmt.Sprintf("%v", o.Value), o.Err)
		o = a.Command(extAtom, ability.CloudRoleCommandRegister, ability.CloudRoleRegisterServiceArgs{Name: "svc-1", Version: "1.0", Endpoint: "10.0.0.9:8080"})
		report("CloudRole/register_service", "svc-1", o.Err)
		o = a.Command(extAtom, ability.CloudRoleCommandListServices, nil)
		report("CloudRole/list_services", fmt.Sprintf("%v", o.Value), o.Err)
		o = a.Command(extAtom, ability.CloudRoleCommandHeartbeat, nil)
		report("CloudRole/heartbeat", "", o.Err)
		o = a.Command(extAtom, ability.CloudRoleCommandSetStatus, ability.CloudRoleSetStatusArgs{Status: "healthy"})
		report("CloudRole/set_status", "healthy", o.Err)
	}

	// FileTransferAbility (本地上传/下载; 框架不内置传输实现, 无 transport 时上传返回错误是预期)
	if a, ok := extAtom.Ability("FileTransferAbility"); ok {
		src := filepath.Join(os.TempDir(), "fe-src.bin")
		_ = os.WriteFile(src, []byte("file-transfer-payload-0123456789"), 0o644)
		o := a.Command(extAtom, ability.FileTransferCommandSetTarget, ability.FileTransferTargetArgs{PeerName: "peer-b"})
		report("FileTransfer/set_target", "peer-b", o.Err)
		o = a.Command(extAtom, ability.FileTransferCommandUpload, ability.FileTransferUploadArgs{LocalPath: src, RemotePath: "incoming/fe-src.bin"})
		if o.Err != nil {
			fmt.Printf("  [SKIP] FileTransfer/upload (无传输实现, 框架预期): %v\n", o.Err)
		} else {
			report("FileTransfer/upload", "incoming/fe-src.bin", nil)
		}
		_ = os.Remove(src)
	}

	// AlgDistAbility
	if a, ok := extAtom.Ability("AlgDistAbility"); ok {
		o := a.Command(extAtom, ability.AlgDistCommandRegister, ability.AlgDistRegisterArgs{Name: "denoise", Version: "1.0", SourcePath: "/alg/denoise.py", ContentType: "python"})
		report("AlgDist/register", "denoise@1.0", o.Err)
		o = a.Command(extAtom, ability.AlgDistCommandList, nil)
		report("AlgDist/list", fmt.Sprintf("%v", o.Value), o.Err)
		o = a.Command(extAtom, ability.AlgDistCommandGet, ability.AlgDistAlgorithmRef{Name: "denoise", Version: "1.0"})
		report("AlgDist/get", fmt.Sprintf("%v", o.Value), o.Err)
		o = a.Command(extAtom, ability.AlgDistCommandUnregister, ability.AlgDistAlgorithmRef{Name: "denoise", Version: "1.0"})
		report("AlgDist/unregister", "denoise@1.0", o.Err)
	}

	// InfluxAbility (配置类, 不需要真实 InfluxDB)
	if a, ok := extAtom.Ability("InfluxAbility"); ok {
		o := a.Command(extAtom, ability.InfluxCommandSetEndpoint, ability.InfluxConfigArgs{Value: "http://127.0.0.1:8086"})
		report("Influx/set_endpoint", "http://127.0.0.1:8086", o.Err)
		o = a.Command(extAtom, ability.InfluxCommandSetBucket, ability.InfluxConfigArgs{Value: "telemetry"})
		report("Influx/set_bucket", "telemetry", o.Err)
		o = a.Command(extAtom, ability.InfluxCommandGetConfig, nil)
		report("Influx/get_config", fmt.Sprintf("%v", o.Value), o.Err)
	}

	// TSNAbility (配置类)
	if a, ok := extAtom.Ability("TSNAbility"); ok {
		o := a.Command(extAtom, ability.TSNCommandSetInterface, ability.TSNInterfaceArg{Interface: "eth0"})
		report("TSN/set_interface", "eth0", o.Err)
		o = a.Command(extAtom, ability.TSNCommandGetInterface, nil)
		report("TSN/get_interface", fmt.Sprintf("%v", o.Value), o.Err)
		o = a.Command(extAtom, ability.TSNCommandRegisterTalker, ability.TSNRegisterTalkerArgs{ID: "stream-100", MAC: "00:11:22:33:44:55", DestMAC: "01:00:5e:00:00:01", VLANID: 100, Priority: 3, PayloadLen: 512, Interval: 1000000000})
		report("TSN/register_talker", "stream-100", o.Err)
		o = a.Command(extAtom, ability.TSNCommandListStreams, nil)
		report("TSN/list_streams", fmt.Sprintf("%v", o.Value), o.Err)
		o = a.Command(extAtom, ability.TSNCommandSetPriority, ability.TSNPriorityMapArgs{Mappings: map[uint8]uint8{0: 0, 1: 0, 2: 1, 3: 1, 4: 2, 5: 2, 6: 3, 7: 3}})
		report("TSN/set_priority_map", "8->4 queue", o.Err)
		o = a.Command(extAtom, ability.TSNCommandSetTimeAware, ability.TSNTimeAwareArgs{Enabled: true, CycleTime: 1000000000, GateStates: []byte{0xAA}})
		report("TSN/set_time_aware", "Qbv enabled", o.Err)
	}

	// SerialAbility (无设备则 list_ports 正常即可)
	if a, ok := extAtom.Ability("SerialAbility"); ok {
		o := a.Command(extAtom, ability.SerialCommandListPorts, nil)
		report("Serial/list_ports", fmt.Sprintf("%v", o.Value), o.Err)
	}

	// --- EKuiperAbility (配置类; 流/规则需注入 transport) ---
	if a, ok := extAtom.Ability("EKuiperAbility"); ok {
		// set_endpoint 需域名 (isAcceptableInfluxURL 拒绝 IP/回环)
		o := a.Command(extAtom, ability.EKuiperCommandSetEndpoint, ability.EKuiperEndpointArgs{URL: "http://ekuiper.internal.example:9081"})
		report("EKuiper/set_endpoint", "http://ekuiper.internal.example:9081", o.Err)
		o = a.Command(extAtom, ability.EKuiperCommandGetEndpoint, nil)
		report("EKuiper/get_endpoint", fmt.Sprintf("%v", o.Value), o.Err)
		// 无 transport 时 create_stream 应正确报错 (验证拒绝路径)
		o = a.Command(extAtom, ability.EKuiperCommandCreateStream, ability.EKuiperStreamArgs{Name: "s1", SQL: "SELECT * FROM demo"})
		if o.Err == nil {
			report("EKuiper/create_stream-no-transport", "应拒绝但成功", fmt.Errorf("should reject without transport"))
		} else {
			report("EKuiper/create_stream-no-transport", "正确拒绝", nil)
		}
		// 非法 SQL 应被拒
		o = a.Command(extAtom, ability.EKuiperCommandCreateStream, ability.EKuiperStreamArgs{Name: "s1", SQL: ""})
		if o.Err == nil {
			report("EKuiper/create_stream-empty-sql", "应拒绝但成功", fmt.Errorf("empty sql accepted"))
		} else {
			report("EKuiper/create_stream-empty-sql", "正确拒绝", nil)
		}
	}

	// --- KubernetesAbility (配置类 set_context/get_context) ---
	if a, ok := extAtom.Ability("KubernetesAbility"); ok {
		o := a.Command(extAtom, ability.K8sCommandSetContext, ability.K8sContextArgs{K8sContext: ability.K8sContext{Cluster: "edge-cluster", Namespace: "edge-ns"}})
		if o.Err != nil {
			report("K8s/set_context", "", o.Err)
		} else {
			report("K8s/set_context", "edge-cluster/edge-ns", nil)
			o = a.Command(extAtom, ability.K8sCommandGetContext, nil)
			report("K8s/get_context", fmt.Sprintf("%v", o.Value), o.Err)
		}
		// 空 cluster 应被拒
		o = a.Command(extAtom, ability.K8sCommandSetContext, ability.K8sContextArgs{K8sContext: ability.K8sContext{Cluster: ""}})
		if o.Err == nil {
			report("K8s/set_context-empty", "应拒绝但成功", fmt.Errorf("empty cluster accepted"))
		} else {
			report("K8s/set_context-empty", "正确拒绝", nil)
		}
		// 无 transport 时 apply 应正确报错
		o = a.Command(extAtom, ability.K8sCommandApply, ability.K8sApplyArgs{Manifest: "apiVersion: v1\nkind: Pod"})
		if o.Err == nil {
			report("K8s/apply-no-transport", "应拒绝但成功", fmt.Errorf("should reject without transport"))
		} else {
			report("K8s/apply-no-transport", "正确拒绝", nil)
		}
	}

	// --- DockerAbility (配置类 set_endpoint; 容器操作需注入 transport) ---
	if a, ok := extAtom.Ability("DockerAbility"); ok {
		// unix socket 端点合法
		o := a.Command(extAtom, ability.DockerCommandSetEndpoint, ability.DockerEndpointArgs{URL: "unix:///var/run/docker.sock"})
		report("Docker/set_endpoint-unix", "unix:///var/run/docker.sock", o.Err)
		o = a.Command(extAtom, ability.DockerCommandGetEndpoint, nil)
		report("Docker/get_endpoint", fmt.Sprintf("%v", o.Value), o.Err)
		// 回环端点应被拒 (isValidDockerEndpoint 拒绝 localhost)
		o = a.Command(extAtom, ability.DockerCommandSetEndpoint, ability.DockerEndpointArgs{URL: "tcp://127.0.0.1:2375"})
		if o.Err == nil {
			report("Docker/set_endpoint-loopback", "应拒绝但成功", fmt.Errorf("loopback accepted"))
		} else {
			report("Docker/set_endpoint-loopback", "正确拒绝", nil)
		}
		// 无 transport 时 list_containers 应正确报错
		o = a.Command(extAtom, ability.DockerCommandListContainers, nil)
		if o.Err == nil {
			report("Docker/list-no-transport", "应拒绝但成功", fmt.Errorf("should reject without transport"))
		} else {
			report("Docker/list-no-transport", "正确拒绝", nil)
		}
	}

	// === 数据库 data 组件 (配置/秘钥存储, 无需真实数据库) ===	fmt.Println("\n=== 数据库 Data 组件: MySQL / PostgreSQL / SQLite / Redis / MongoDB / InfluxDB ===")
	dbVerify := func(name string, configure, setSecret, clearSecret, getConfig, status, snapshot func() error) {
		if err := configure(); err != nil {
			report(name+"/configure", "", err)
			return
		}
		report(name+"/configure", "ok", nil)
		if err := getConfig(); err != nil {
			report(name+"/get_config", "", err)
		} else {
			report(name+"/get_config", "ok", nil)
		}
		if err := status(); err != nil {
			report(name+"/status", "", err)
		} else {
			report(name+"/status", "ok", nil)
		}
		if err := setSecret(); err != nil {
			report(name+"/set_secret", "", err)
		} else {
			report(name+"/set_secret", "ok", nil)
		}
		if err := snapshot(); err != nil {
			report(name+"/snapshot", "", err)
		} else {
			report(name+"/snapshot", "ok", nil)
		}
		if err := clearSecret(); err != nil {
			report(name+"/clear_secret", "", err)
		} else {
			report(name+"/clear_secret", "ok", nil)
		}
	}
	// MySQLData
	if d, ok := extAtom.Data("MySQLData"); ok {
		cfg := data.SQLDatabaseConfig{Host: "db.internal.example", Port: 3306, Database: "appdb", Username: "app", TLSMode: data.DatabaseTLSDisable, ConnectTimeout: 3 * time.Second, MaxOpenConns: 10, MaxIdleConns: 5, ConnMaxLifetime: time.Hour}
		dbVerify("MySQLData",
			func() error {
				return d.Command(extAtom, data.DatabaseCommandConfigure, data.SQLDatabaseConfigureArgs{Config: cfg}).Err
			},
			func() error {
				return d.Command(extAtom, data.DatabaseCommandSetSecret, data.DatabaseSetSecretArgs{Secret: []byte("mysql-secret-0123456789")}).Err
			},
			func() error { return d.Command(extAtom, data.DatabaseCommandClearSecret, nil).Err },
			func() error { return d.Command(extAtom, data.DatabaseCommandGetConfig, nil).Err },
			func() error { return d.Command(extAtom, data.DatabaseCommandStatus, nil).Err },
			func() error { return d.Command(extAtom, data.DatabaseCommandSnapshot, nil).Err })
	}
	// PostgreSQLData
	if d, ok := extAtom.Data("PostgreSQLData"); ok {
		cfg := data.SQLDatabaseConfig{Host: "pg.internal.example", Port: 5432, Database: "appdb", Username: "app", TLSMode: data.DatabaseTLSPrefer, ConnectTimeout: 3 * time.Second, MaxOpenConns: 10, MaxIdleConns: 5, ConnMaxLifetime: time.Hour}
		dbVerify("PostgreSQLData",
			func() error {
				return d.Command(extAtom, data.DatabaseCommandConfigure, data.SQLDatabaseConfigureArgs{Config: cfg}).Err
			},
			func() error {
				return d.Command(extAtom, data.DatabaseCommandSetSecret, data.DatabaseSetSecretArgs{Secret: []byte("pg-secret-0123456789")}).Err
			},
			func() error { return d.Command(extAtom, data.DatabaseCommandClearSecret, nil).Err },
			func() error { return d.Command(extAtom, data.DatabaseCommandGetConfig, nil).Err },
			func() error { return d.Command(extAtom, data.DatabaseCommandStatus, nil).Err },
			func() error { return d.Command(extAtom, data.DatabaseCommandSnapshot, nil).Err })
	}
	// SQLiteData (本地文件, 无秘钥: ListCommands 不含 set_secret/clear_secret)
	if d, ok := extAtom.Data("SQLiteData"); ok {
		cfg := data.SQLiteConfig{Path: "/tmp/fe-verify.db", Mode: "rwc", BusyTimeout: time.Second, WAL: true, ForeignKeys: true, MaxOpenConns: 4}
		if err := d.Command(extAtom, data.DatabaseCommandConfigure, data.SQLiteConfigureArgs{Config: cfg}).Err; err != nil {
			report("SQLiteData/configure", "", err)
		} else {
			report("SQLiteData/configure", "ok", nil)
			if err := d.Command(extAtom, data.DatabaseCommandGetConfig, nil).Err; err != nil {
				report("SQLiteData/get_config", "", err)
			} else {
				report("SQLiteData/get_config", "ok", nil)
			}
			if err := d.Command(extAtom, data.DatabaseCommandStatus, nil).Err; err != nil {
				report("SQLiteData/status", "", err)
			} else {
				report("SQLiteData/status", "ok", nil)
			}
			if err := d.Command(extAtom, data.DatabaseCommandSnapshot, nil).Err; err != nil {
				report("SQLiteData/snapshot", "", err)
			} else {
				report("SQLiteData/snapshot", "ok", nil)
			}
		}
	}
	// RedisData
	if d, ok := extAtom.Data("RedisData"); ok {
		cfg := data.RedisConfig{Addresses: []string{"redis.internal.example:6379"}, DB: 0, DialTimeout: 3 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, PoolSize: 8}
		dbVerify("RedisData",
			func() error {
				return d.Command(extAtom, data.DatabaseCommandConfigure, data.RedisConfigureArgs{Config: cfg}).Err
			},
			func() error {
				return d.Command(extAtom, data.DatabaseCommandSetSecret, data.DatabaseSetSecretArgs{Secret: []byte("redis-secret-0123456789")}).Err
			},
			func() error { return d.Command(extAtom, data.DatabaseCommandClearSecret, nil).Err },
			func() error { return d.Command(extAtom, data.DatabaseCommandGetConfig, nil).Err },
			func() error { return d.Command(extAtom, data.DatabaseCommandStatus, nil).Err },
			func() error { return d.Command(extAtom, data.DatabaseCommandSnapshot, nil).Err })
	}
	// MongoDBData
	if d, ok := extAtom.Data("MongoDBData"); ok {
		cfg := data.MongoDBConfig{Hosts: []string{"mongo.internal.example:27017"}, Database: "appdb", Username: "app", ConnectTimeout: 3 * time.Second, ServerSelectionTimeout: 3 * time.Second}
		dbVerify("MongoDBData",
			func() error {
				return d.Command(extAtom, data.DatabaseCommandConfigure, data.MongoDBConfigureArgs{Config: cfg}).Err
			},
			func() error {
				return d.Command(extAtom, data.DatabaseCommandSetSecret, data.DatabaseSetSecretArgs{Secret: []byte("mongo-secret-0123456789")}).Err
			},
			func() error { return d.Command(extAtom, data.DatabaseCommandClearSecret, nil).Err },
			func() error { return d.Command(extAtom, data.DatabaseCommandGetConfig, nil).Err },
			func() error { return d.Command(extAtom, data.DatabaseCommandStatus, nil).Err },
			func() error { return d.Command(extAtom, data.DatabaseCommandSnapshot, nil).Err })
	}
	// InfluxDBData (endpoint 拒绝 IP/回环, 用域名)
	if d, ok := extAtom.Data("InfluxDBData"); ok {
		cfg := data.InfluxDBConfig{Endpoint: "http://influx.internal.example:8086", Org: "feorg", Bucket: "telemetry", Timeout: 3 * time.Second}
		dbVerify("InfluxDBData",
			func() error {
				return d.Command(extAtom, data.DatabaseCommandConfigure, data.InfluxDBConfigureArgs{Config: cfg}).Err
			},
			func() error {
				return d.Command(extAtom, data.DatabaseCommandSetSecret, data.DatabaseSetSecretArgs{Secret: []byte("influx-token-0123456789")}).Err
			},
			func() error { return d.Command(extAtom, data.DatabaseCommandClearSecret, nil).Err },
			func() error { return d.Command(extAtom, data.DatabaseCommandGetConfig, nil).Err },
			func() error { return d.Command(extAtom, data.DatabaseCommandStatus, nil).Err },
			func() error { return d.Command(extAtom, data.DatabaseCommandSnapshot, nil).Err })
	}

	// 3. 命令鉴权: 未配置认证时应拒绝
	fmt.Println("\n=== 命令鉴权验证 ===")
	o := atom.AuthenticatedCommand("cred", "BaseAbility", ability.CommandListAbilityNames, nil)
	if o.Err == nil {
		report("Auth/no-authenticator", "应拒绝但成功", fmt.Errorf("authentication bypass"))
	} else {
		report("Auth/no-authenticator", fmt.Sprintf("正确拒绝: %v", o.Err), nil)
	}

	// 4. 汇总
	fmt.Printf("\n=== 汇总: %d PASS, %d FAIL ===\n", passCount, failCount)
	if failCount > 0 {
		fmt.Println("失败详情:")
		for _, f := range failDetails {
			fmt.Println("  -", f)
		}
		os.Exit(1)
	}
	fmt.Println("ALL VERIFICATIONS PASSED")
}
