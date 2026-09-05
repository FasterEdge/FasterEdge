package main

import (
	"fmt"

	"github.com/FasterEdge/FasterEdge/ability"
	"github.com/FasterEdge/FasterEdge/data"
	"github.com/FasterEdge/FasterEdge/types"
)

// verifySkeletonExtras 覆盖骨架能力(Serial/Modbus/MQTT/NetMap/Config/ConfigFile)
// 的命令盲区——第十三轮全仓命令枚举 mapping 发现这些命令从未被 verify 触及。
// 纪律与主段一致: 类型断言失败 + err==nil = FAIL; 参数校验/空状态/无 transport
// 的"正确拒绝"与值段(PASS)。
func verifySkeletonExtras(atom, extAtom *types.Atom) {
	// --- SerialAbility: open/close/read/write/is_open/set_config/get_config ---
	if a, ok := extAtom.Ability("SerialAbility"); ok {
		o := a.Command(extAtom, ability.SerialCommandOpen, "raw")
		if o.Err == nil {
			report("Serial/open-type", "应拒绝但成功", fmt.Errorf("wrong type accepted"))
		} else {
			report("Serial/open-type", "正确拒绝", nil)
		}
		o = a.Command(extAtom, ability.SerialCommandOpen, ability.SerialOpenArgs{Port: "../evil", Config: ability.SerialConfig{Baud: 9600, DataBits: 8, StopBits: 1, Parity: "N"}})
		if o.Err == nil {
			report("Serial/open-bad-port", "应拒绝但成功", fmt.Errorf("invalid port accepted"))
		} else {
			report("Serial/open-bad-port", "正确拒绝", nil)
		}
		// 合法端口 + 无 transport → 正确拒绝(open 需 transport.Open)
		o = a.Command(extAtom, ability.SerialCommandOpen, ability.SerialOpenArgs{Port: "/dev/ttyUSB0", Config: ability.SerialConfig{Baud: 9600, DataBits: 8, StopBits: 1, Parity: "N"}})
		if o.Err == nil {
			report("Serial/open-no-transport", "应拒绝但成功", fmt.Errorf("open accepted without transport"))
		} else {
			report("Serial/open-no-transport", "正确拒绝", nil)
		}
		// is_open 未打开端口 → false 值段
		o = a.Command(extAtom, ability.SerialCommandIsOpen, ability.SerialPortArg{Port: "/dev/ttyUSB0"})
		if v, ok := o.Value.(bool); ok {
			if v {
				report("Serial/is_open-unopened", "true", fmt.Errorf("unopened port reported open"))
			} else {
				report("Serial/is_open-unopened", "false", nil)
			}
		} else if o.Err != nil {
			report("Serial/is_open-unopened", fmt.Sprintf("%v", o.Value), o.Err)
		} else {
			report("Serial/is_open-unopened", fmt.Sprintf("%T", o.Value), fmt.Errorf("is_open returned %T (want bool)", o.Value))
		}
		// get_config 未打开 → 拒绝
		o = a.Command(extAtom, ability.SerialCommandGetConfig, ability.SerialPortArg{Port: "/dev/ttyUSB0"})
		if o.Err == nil {
			report("Serial/get_config-unopened", "应拒绝但成功", fmt.Errorf("get_config on unopened port accepted"))
		} else {
			report("Serial/get_config-unopened", "正确拒绝", nil)
		}
		// read 长度 0 / 超上限 → 拒绝
		o = a.Command(extAtom, ability.SerialCommandRead, ability.SerialReadArgs{Port: "/dev/ttyUSB0", Length: 0})
		if o.Err == nil {
			report("Serial/read-zero", "应拒绝但成功", fmt.Errorf("zero length accepted"))
		} else {
			report("Serial/read-zero", "正确拒绝", nil)
		}
		o = a.Command(extAtom, ability.SerialCommandRead, ability.SerialReadArgs{Port: "/dev/ttyUSB0", Length: 1 << 21})
		if o.Err == nil {
			report("Serial/read-too-large", "应拒绝但成功", fmt.Errorf("oversized length accepted"))
		} else {
			report("Serial/read-too-large", "正确拒绝", nil)
		}
		// write 空数据 → 拒绝
		o = a.Command(extAtom, ability.SerialCommandWrite, ability.SerialWriteArgs{Port: "/dev/ttyUSB0"})
		if o.Err == nil {
			report("Serial/write-empty", "应拒绝但成功", fmt.Errorf("empty write accepted"))
		} else {
			report("Serial/write-empty", "正确拒绝", nil)
		}
		// set_config 未打开 → 拒绝
		o = a.Command(extAtom, ability.SerialCommandSetConfig, ability.SerialSetConfigArgs{Port: "/dev/ttyUSB0", Config: ability.SerialConfig{Baud: 115200, DataBits: 8, StopBits: 1, Parity: "N"}})
		if o.Err == nil {
			report("Serial/set_config-unopened", "应拒绝但成功", fmt.Errorf("set_config on unopened port accepted"))
		} else {
			report("Serial/set_config-unopened", "正确拒绝", nil)
		}
		// close 未打开端口 → 拒绝
		o = a.Command(extAtom, ability.SerialCommandClose, ability.SerialPortArg{Port: "/dev/ttyUSB0"})
		if o.Err == nil {
			report("Serial/close-unopened", "应拒绝但成功", fmt.Errorf("close on unopened port accepted"))
		} else {
			report("Serial/close-unopened", "正确拒绝", nil)
		}
	}

	// --- ModbusAbility: get_endpoint/get_unit_id/read_input/read_discrete/write_multi_reg ---
	if a, ok := extAtom.Ability("ModbusAbility"); ok {
		o := a.Command(extAtom, ability.ModbusCommandSetUnitID, ability.ModbusUnitIDArgs{UnitID: 7})
		report("Modbus/set_unit_id2", "7", o.Err)
		o = a.Command(extAtom, ability.ModbusCommandGetUnitID, nil)
		if v, ok := o.Value.(uint8); ok {
			if v != 7 {
				report("Modbus/get_unit_id", fmt.Sprintf("%d", v), fmt.Errorf("get_unit_id mismatch: got %d want 7", v))
			} else {
				report("Modbus/get_unit_id", "7", nil)
			}
		} else if o.Err != nil {
			report("Modbus/get_unit_id", fmt.Sprintf("%v", o.Value), o.Err)
		} else {
			report("Modbus/get_unit_id", fmt.Sprintf("%T", o.Value), fmt.Errorf("get_unit_id returned %T (want uint8)", o.Value))
		}
		// get_endpoint 往返(现有段已 set 127.0.0.1:1502)
		o = a.Command(extAtom, ability.ModbusCommandGetEndpoint, nil)
		if v, ok := o.Value.(string); ok && v == "127.0.0.1:1502" {
			report("Modbus/get_endpoint2", v, nil)
		} else if o.Err != nil {
			report("Modbus/get_endpoint2", fmt.Sprintf("%v", o.Value), o.Err)
		} else {
			report("Modbus/get_endpoint2", fmt.Sprintf("%T %v", o.Value, o.Value), fmt.Errorf("get_endpoint returned %T (want string)", o.Value))
		}
		// read_input quantity 0 → 拒绝(参数校验, 与 transport 无关)
		o = a.Command(extAtom, ability.ModbusCommandReadInput, ability.ModbusReadArgs{Address: 0, Quantity: 0})
		if o.Err == nil {
			report("Modbus/read_input-zero", "应拒绝但成功", fmt.Errorf("zero quantity accepted"))
		} else {
			report("Modbus/read_input-zero", "正确拒绝", nil)
		}
		// read_discrete 合法参数(无从站时 transport.Send 失败 → 运行期拒绝)
		o = a.Command(extAtom, ability.ModbusCommandReadDiscrete, ability.ModbusReadArgs{Address: 0, Quantity: 8})
		if o.Err == nil {
			report("Modbus/read_discrete-unreachable", "应拒绝但成功", fmt.Errorf("read_discrete accepted without reachable slave"))
		} else {
			report("Modbus/read_discrete-unreachable", "正确拒绝", nil)
		}
		// write_multi_reg 空/超 123 → 拒绝
		o = a.Command(extAtom, ability.ModbusCommandWriteMultiReg, ability.ModbusWriteMultiArgs{Address: 0, Values: nil})
		if o.Err == nil {
			report("Modbus/write_multi-empty", "应拒绝但成功", fmt.Errorf("empty values accepted"))
		} else {
			report("Modbus/write_multi-empty", "正确拒绝", nil)
		}
		big := make([]uint16, 200)
		o = a.Command(extAtom, ability.ModbusCommandWriteMultiReg, ability.ModbusWriteMultiArgs{Address: 0, Values: big})
		if o.Err == nil {
			report("Modbus/write_multi-too-many", "应拒绝但成功", fmt.Errorf("200 values accepted"))
		} else {
			report("Modbus/write_multi-too-many", "正确拒绝", nil)
		}
	}

	// --- MQTTAbility: is_connected/get_broker/set_client_id/set_credentials/list_subs/unsubscribe ---
	if a, ok := extAtom.Ability("MQTTAbility"); ok {
		o := a.Command(extAtom, ability.MQTTCommandSetClientID, ability.MQTTClientIDArgs{ClientID: "fe-verify-skel"})
		report("MQTT/set_client_id", "fe-verify-skel", o.Err)
		// 空 client id 拒绝
		o = a.Command(extAtom, ability.MQTTCommandSetClientID, ability.MQTTClientIDArgs{ClientID: "  "})
		if o.Err == nil {
			report("MQTT/set_client_id-blank", "应拒绝但成功", fmt.Errorf("blank client id accepted"))
		} else {
			report("MQTT/set_client_id-blank", "正确拒绝", nil)
		}
		o = a.Command(extAtom, ability.MQTTCommandSetCredentials, ability.MQTTCredentialsArgs{Username: "u1", Password: "p1"})
		report("MQTT/set_credentials", "u1", o.Err)
		// is_connected 无 transport → false 值段(实现语义, 非错误)
		o = a.Command(extAtom, ability.MQTTCommandIsConnected, nil)
		if v, ok := o.Value.(bool); ok {
			report("MQTT/is_connected", fmt.Sprintf("%v", v), nil)
		} else if o.Err != nil {
			report("MQTT/is_connected", fmt.Sprintf("%v", o.Value), o.Err)
		} else {
			report("MQTT/is_connected", fmt.Sprintf("%T", o.Value), fmt.Errorf("is_connected returned %T (want bool)", o.Value))
		}
		o = a.Command(extAtom, ability.MQTTCommandGetBroker, nil)
		if v, ok := o.Value.(string); ok {
			report("MQTT/get_broker", fmt.Sprintf("%q", v), nil)
		} else if o.Err != nil {
			report("MQTT/get_broker", fmt.Sprintf("%v", o.Value), o.Err)
		} else {
			report("MQTT/get_broker", fmt.Sprintf("%T", o.Value), fmt.Errorf("get_broker returned %T (want string)", o.Value))
		}
		o = a.Command(extAtom, ability.MQTTCommandListSubs, nil)
		if v, ok := o.Value.([]string); ok {
			report("MQTT/list_subs", fmt.Sprintf("count=%d", len(v)), nil)
		} else if o.Err != nil {
			report("MQTT/list_subs", fmt.Sprintf("%v", o.Value), o.Err)
		} else {
			report("MQTT/list_subs", fmt.Sprintf("%T", o.Value), fmt.Errorf("list_subs returned %T (want []string)", o.Value))
		}
		// unsubscribe 未订阅 → 拒绝
		o = a.Command(extAtom, ability.MQTTCommandUnsubscribe, ability.MQTTTopicArg{Topic: "never/subscribed"})
		if o.Err == nil {
			report("MQTT/unsubscribe-not-subscribed", "应拒绝但成功", fmt.Errorf("unsubscribe on non-subscribed topic accepted"))
		} else {
			report("MQTT/unsubscribe-not-subscribed", "正确拒绝", nil)
		}
	}

	// --- NetMapAbility: lookup_peer/update_peer/get_topology ---
	if a, ok := extAtom.Ability("NetMapAbility"); ok {
		// lookup_peer 已注册 peer-b(现有段已注册) → 值段
		o := a.Command(extAtom, ability.NetMapCommandLookupPeer, ability.NetMapLookupPeerArgs{Name: "peer-b"})
		if v, ok := o.Value.(ability.NetMapPeer); ok && v.Name == "peer-b" {
			report("NetMap/lookup_peer", "peer-b", nil)
		} else if o.Err != nil {
			report("NetMap/lookup_peer", fmt.Sprintf("%v", o.Value), o.Err)
		} else {
			report("NetMap/lookup_peer", fmt.Sprintf("%T %v", o.Value, o.Value), fmt.Errorf("lookup_peer returned %T (want ability.NetMapPeer)", o.Value))
		}
		// update_peer 存在 → 值段
		o = a.Command(extAtom, ability.NetMapCommandUpdatePeer, ability.NetMapUpdatePeerArgs{Name: "peer-b", NewAddress: "10.0.0.6:7000", NewRole: "edge"})
		if o.Err != nil {
			report("NetMap/update_peer", fmt.Sprintf("%v", o.Err), o.Err)
		} else {
			report("NetMap/update_peer", "peer-b", nil)
		}
		// update 后 lookup 地址变化断言
		o = a.Command(extAtom, ability.NetMapCommandLookupPeer, ability.NetMapLookupPeerArgs{Name: "peer-b"})
		if v, ok := o.Value.(ability.NetMapPeer); ok {
			if v.Address != "10.0.0.6:7000" {
				report("NetMap/update_peer-lookup", fmt.Sprintf("%+v", v), fmt.Errorf("update_peer not reflected: %+v", v))
			} else {
				report("NetMap/update_peer-lookup", v.Address, nil)
			}
		} else if o.Err != nil {
			report("NetMap/update_peer-lookup", fmt.Sprintf("%v", o.Value), o.Err)
		} else {
			report("NetMap/update_peer-lookup", fmt.Sprintf("%T", o.Value), fmt.Errorf("lookup returned %T (want ability.NetMapPeer)", o.Value))
		}
		// get_topology 值段(NetMapTopology 快照)
		o = a.Command(extAtom, ability.NetMapCommandGetTopology, nil)
		if v, ok := o.Value.(ability.NetMapTopology); ok {
			report("NetMap/get_topology", fmt.Sprintf("peers=%d", len(v.Peers)), nil)
		} else if o.Err != nil {
			report("NetMap/get_topology", fmt.Sprintf("%v", o.Value), o.Err)
		} else {
			report("NetMap/get_topology", fmt.Sprintf("%T", o.Value), fmt.Errorf("get_topology returned %T (want ability.NetMapTopology)", o.Value))
		}
	}

	// --- ConfigData: delete/snapshot(组件在 atom 上) ---
	if d, ok := atom.Data("ConfigData"); ok {
		cd := d.(*data.ConfigData)
		o := cd.Command(atom, data.ConfigCommandSet, data.ConfigSetArgs{Key: "fe.verify.del", Value: "x"})
		report("Config/set-del-key", "fe.verify.del", o.Err)
		o = cd.Command(atom, data.ConfigCommandDelete, data.ConfigDeleteArgs{Key: "fe.verify.del"})
		if o.Err != nil {
			report("Config/delete", fmt.Sprintf("%v", o.Err), o.Err)
		} else {
			report("Config/delete", "fe.verify.del", nil)
			o = cd.Command(atom, data.ConfigCommandGet, data.ConfigGetArgs{Key: "fe.verify.del"})
			if o.Err == nil {
				report("Config/delete-gone", "应失败但仍在", fmt.Errorf("deleted key still readable"))
			} else {
				report("Config/delete-gone", "已删除", nil)
			}
		}
		o = cd.Command(atom, data.ConfigCommandDelete, data.ConfigDeleteArgs{Key: "fe.never.exists"})
		if o.Err == nil {
			report("Config/delete-missing", "应拒绝但成功", fmt.Errorf("missing key delete accepted"))
		} else {
			report("Config/delete-missing", "正确拒绝", nil)
		}
		o = cd.Command(atom, data.ConfigCommandSnapshot, nil)
		if v, ok := o.Value.(map[string]string); ok {
			report("Config/snapshot", fmt.Sprintf("keys=%d", len(v)), nil)
		} else if o.Err != nil {
			report("Config/snapshot", fmt.Sprintf("%v", o.Value), o.Err)
		} else {
			report("Config/snapshot", fmt.Sprintf("%T", o.Value), fmt.Errorf("snapshot returned %T (want map[string]string)", o.Value))
		}
	}

	// --- ConfigFileAbility: exists/set_path/get_path ---
	if a, ok := extAtom.Ability("ConfigFileAbility"); ok {
		// 临时 root 下不存在 → false
		o := a.Command(extAtom, ability.ConfigFileCommandExists, ability.ConfigFilePathArg{Path: "no-such.json"})
		if v, ok := o.Value.(bool); ok {
			if v {
				report("ConfigFile/exists-missing", "true", fmt.Errorf("missing file reported exists"))
			} else {
				report("ConfigFile/exists-missing", "false", nil)
			}
		} else if o.Err != nil {
			report("ConfigFile/exists-missing", fmt.Sprintf("%v", o.Value), o.Err)
		} else {
			report("ConfigFile/exists-missing", fmt.Sprintf("%T", o.Value), fmt.Errorf("exists returned %T (want bool)", o.Value))
		}
		// set_path 合法往返(confine 后)
		o = a.Command(extAtom, ability.ConfigFileCommandSetPath, ability.ConfigFilePathArg{Path: "sub/fe-skel.json"})
		if o.Err != nil {
			report("ConfigFile/set_path", fmt.Sprintf("%v", o.Err), o.Err)
		} else {
			report("ConfigFile/set_path", fmt.Sprintf("%v", o.Value), nil)
			o = a.Command(extAtom, ability.ConfigFileCommandGetPath, nil)
			if v, ok := o.Value.(string); ok && v != "" {
				report("ConfigFile/get_path", v, nil)
			} else if o.Err != nil {
				report("ConfigFile/get_path", fmt.Sprintf("%v", o.Value), o.Err)
			} else {
				report("ConfigFile/get_path", fmt.Sprintf("%T %v", o.Value, o.Value), fmt.Errorf("get_path returned %T (want non-empty string)", o.Value))
			}
		}
		// 逃逸路径拒绝
		o = a.Command(extAtom, ability.ConfigFileCommandSetPath, ability.ConfigFilePathArg{Path: "../../etc/passwd"})
		if o.Err == nil {
			report("ConfigFile/set_path-escape", "应拒绝但成功", fmt.Errorf("escape path accepted"))
		} else {
			report("ConfigFile/set_path-escape", "正确拒绝", nil)
		}
	}
}
