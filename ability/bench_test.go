package ability

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FasterEdge/FasterEdge/data"
	"github.com/FasterEdge/FasterEdge/types"
)

// BenchmarkRoleAbilitySetGet 并发写读 RoleAbility,模拟典型 "node 状态查询" 路径。
func BenchmarkRoleAbilitySetGet(b *testing.B) {
	r := &RoleAbility{}
	// 预热:设一次角色
	r.Command(nil, CommandSetRole, RoleAbilityArgs{Role: "edge"})

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			r.Command(nil, CommandSetRole, RoleAbilityArgs{Role: "edge"})
			r.Command(nil, CommandGetRole, nil)
		}
	})
}

// BenchmarkKeyringDataIssueAndVerify 模拟高频签发 + 校验令牌的热路径。
func BenchmarkKeyringDataIssueAndVerify(b *testing.B) {
	k := data.NewKeyringData()
	k.Mount(nil)
	// 预热
	k.IssueToken("warmup", time.Hour)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		subject := "subject-" + fmt.Sprintf("%d", i)
		tok, err := k.IssueToken(subject, time.Hour)
		if err != nil {
			b.Fatal(err)
		}
		k.Verify(subject, tok.IssuedAt, tok.ExpiresAt, k.Sign(tok))
	}
}

// BenchmarkNetMapAbilityRegisterLookup 模拟对等节点注册 + 频繁查找。
func BenchmarkNetMapAbilityRegisterLookup(b *testing.B) {
	a := NewNetMapAbility()
	atom := &types.Atom{}
	atom.AddData(&data.BaseData{})
	atom.AddData(data.NewNetMapData())
	// 预注册 100 个对端
	for i := 0; i < 100; i++ {
		a.Command(atom, NetMapCommandRegisterPeer, NetMapRegisterPeerArgs{
			Name:    "peer-" + fmt.Sprintf("%d", i),
			Address: "10.0.0.1:7000",
		})
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			name := "peer-" + fmt.Sprintf("%d", i%100)
			a.Command(atom, NetMapCommandLookupPeer, NetMapLookupPeerArgs{Name: name})
			i++
		}
	})
}

// BenchmarkConfigDataGetSet 模拟 KV 读写的典型路径。
func BenchmarkConfigDataGetSet(b *testing.B) {
	c := data.NewConfigData()
	c.Set("server.port", "8080")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Set("server.port", "8080")
		c.Get("server.port")
	}
}

// BenchmarkModbusReadHolding 单次 Modbus 寄存器读取(注入 fake transport)。
func BenchmarkModbusReadHolding(b *testing.B) {
	m := NewModbusAbility()
	atom := &types.Atom{}
	atom.AddData(&data.BaseData{})
	resp := []byte{0x03, 0x06, 0x00, 0x01, 0x00, 0x02, 0x00, 0x03}
	ft := &fakeModbusTransport{resp: resp}
	m.SetTransport(ft)
	args := ModbusReadArgs{Address: 100, Quantity: 3}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Command(atom, ModbusCommandReadHolding, args)
	}
}

// BenchmarkMQTTPublishDrain 模拟发布 + 收消息的端到端路径。
func BenchmarkMQTTPublishDrain(b *testing.B) {
	m := NewMQTTAbility()
	atom := &types.Atom{}
	atom.AddData(&data.BaseData{})
	ft := &fakeMQTTTransport{}
	m.SetTransport(ft)
	m.Command(atom, MQTTCommandSubscribe, MQTTSubscribeArgs{Topic: "bench/topic", Qos: 0, MaxQueue: 1024})
	b.ResetTimer()
	var ctr uint64
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			i := atomic.AddUint64(&ctr, 1)
			m.PushMessage(MQTTMessage{
				Topic:      "bench/topic",
				Payload:    []byte("payload"),
				Qos:        0,
				ReceivedAt: time.Now(),
				// 用 i 作为唯一后缀避免完全相同的消息
			})
			m.Command(atom, MQTTCommandDrain, MQTTPullArgs{Topic: "bench/topic", Max: 100})
			_ = i
		}
	})
}

// BenchmarkFullAtomInitStandardAtom 模拟从零启动整个框架的开销。
func BenchmarkFullAtomInitStandardAtom(b *testing.B) {
	for i := 0; i < b.N; i++ {
		atom := newStandardAtomForBench()
		_ = atom
	}
}

// BenchmarkAtomMountAndUnmount 模拟 mount 全套组件的开销。
func BenchmarkAtomMountAndUnmount(b *testing.B) {
	for i := 0; i < b.N; i++ {
		atom := newStandardAtomForBench()
		atom.PreRun()
	}
}

func newStandardAtomForBench() *types.Atom {
	atom := &types.Atom{}
	atom.AddData(&data.BaseData{})
	atom.AddData(data.NewNetMapData())
	atom.AddData(data.NewKeyringData())
	atom.AddData(data.NewConfigData())
	atom.AddAbility(&BaseAbility{})
	atom.AddAbility(&RoleAbility{})
	atom.AddAbility(NewNetMapAbility())
	atom.AddAbility(NewOneKeyAbility())
	atom.AddAbility(NewCmdAbility())
	atom.AddAbility(NewShAbility())
	atom.AddAbility(NewConfigFileAbility())
	return atom
}
