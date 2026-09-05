// ─────────────────────────────────────────────────────────────
// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
// ─────────────────────────────────────────────────────────────
// Package main 最小 MQTT 3.1.1 客户端 transport 实现 (仅用于验证程序, 不入主库依赖):
// 事件驱动: readLoop 是唯一读者, PUBLISH 走回调, SUBACK/UNSUBACK/PUBACK 走响应 channel,
// 避免与同步读竞争同一个 TCP 连接。满足 ability.MQTTTransport 接口。
package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/FasterEdge/FasterEdge/ability"
)

// brokerAddr 返回验证脚本注入的 broker 地址 (FE_MQTT_BROKER), 默认回环。
func brokerAddr() string {
	if a := os.Getenv("FE_MQTT_BROKER"); a != "" {
		return a
	}
	return "127.0.0.1:1883"
}

// mqttClientTransport 是最小 MQTT 3.1.1 客户端。
type mqttClientTransport struct {
	mu        sync.Mutex
	conn      net.Conn
	clientID  string
	connected bool
	msgCb     func(ability.MQTTMessage)
	respCh    chan byte // 响应通道: 0x90 SUBACK / 0xB0 UNSUBACK / 0x40 PUBACK 到达时写入
}

func newMQTTClientTransport(clientID string, msgCb func(ability.MQTTMessage)) *mqttClientTransport {
	return &mqttClientTransport{clientID: clientID, msgCb: msgCb, respCh: make(chan byte, 8)}
}

// Connect 建立 TCP 连接并发送 CONNECT (MQTT 3.1.1, 协议名 "MQTT", 级别 4)。
func (t *mqttClientTransport) Connect() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.connected {
		return nil
	}
	conn, err := net.DialTimeout("tcp", brokerAddr(), 5*time.Second)
	if err != nil {
		return err
	}
	t.conn = conn
	// 可变头: 协议名 + 级别 + 连接标志 + keepalive
	vh := encodeMQTTString("MQTT")
	vh = append(vh, 4) // level 3.1.1
	vh = append(vh, 2) // flags: clean session
	keepalive := make([]byte, 2)
	binary.BigEndian.PutUint16(keepalive, 60)
	vh = append(vh, keepalive...)
	// payload: client id
	payload := encodeMQTTString(t.clientID)
	if err := writeMQTTPacket(conn, 0x10, append(vh, payload...)); err != nil {
		conn.Close()
		return err
	}
	// CONNACK: 固定头 0x20, 剩余长度 2
	ack := make([]byte, 4)
	if _, err := io.ReadFull(conn, ack); err != nil {
		conn.Close()
		return err
	}
	if ack[0] != 0x20 || len(ack) < 4 || ack[3] != 0 {
		conn.Close()
		return fmt.Errorf("CONNACK rejected: %x", ack)
	}
	t.connected = true
	// 启动唯一读循环, 处理 PUBLISH / 各类 ACK
	go t.readLoop()
	return nil
}

// readLoop 是 conn 的唯一读者, 把响应按类型派发:
//
//	0x30 PUBLISH   -> msgCb 回调
//	0x90 SUBACK    -> respCh (Subscribe 等待)
//	0xB0 UNSUBACK  -> respCh (Unsubscribe 等待)
//	0x40 PUBACK    -> respCh (QoS1 Publish 等待, 宽松处理)
//	0xD0 PINGRESP  -> 忽略
func (t *mqttClientTransport) readLoop() {
	for {
		hdr0 := make([]byte, 1)
		if _, err := io.ReadFull(t.conn, hdr0); err != nil {
			t.mu.Lock()
			t.connected = false
			t.mu.Unlock()
			return
		}
		kind := hdr0[0] & 0xF0
		// 剩余长度为变长编码(1-4 字节)。旧实现只读 1 字节:
		// broker 下发 ≥128 字节的包(如大 PUBLISH)时解码截断, 流永久错位。
		remLen := 0
		mult := 1
		for i := 0; i < 4; i++ {
			one := make([]byte, 1)
			if _, err := io.ReadFull(t.conn, one); err != nil {
				t.mu.Lock()
				t.connected = false
				t.mu.Unlock()
				return
			}
			digit := int(one[0])
			remLen += (digit & 0x7F) * mult
			if digit&0x80 == 0 {
				break
			}
			mult *= 128
		}
		body := make([]byte, remLen)
		if _, err := io.ReadFull(t.conn, body); err != nil {
			t.mu.Lock()
			t.connected = false
			t.mu.Unlock()
			return
		}
		switch kind {
		case 0x30: // PUBLISH
			topic, off := decodeMQTTString(body, 0)
			// QoS>0 时 topic 后还有 2 字节 packet id, 需要跳过才是 payload
			if hdr0[0]&0x06 != 0 && off+2 <= len(body) {
				off += 2
			}
			payload := body[off:]
			if msg := t.msgCb; msg != nil {
				msg(ability.MQTTMessage{Topic: topic, Payload: payload, ReceivedAt: time.Now()})
			}
		case 0x90, 0xB0, 0x40: // SUBACK / UNSUBACK / PUBACK
			select {
			case t.respCh <- kind:
			default:
			}
		case 0xD0: // PINGRESP: 忽略
		}
	}
}

// waitAck 等待指定类型的响应, 超时返回错误。
func (t *mqttClientTransport) waitAck(want byte, timeout time.Duration) error {
	deadline := time.After(timeout)
	for {
		select {
		case kind := <-t.respCh:
			if kind == want {
				return nil
			}
			// 其它类型的响应继续等
		case <-deadline:
			return fmt.Errorf("timeout waiting for 0x%02x", want)
		}
	}
}

func (t *mqttClientTransport) Disconnect() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.connected {
		return nil
	}
	if _, err := t.conn.Write([]byte{0xE0, 0x00}); err != nil {
		// 继续关闭
	}
	t.connected = false
	return t.conn.Close()
}

func (t *mqttClientTransport) IsConnected() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.connected
}

func (t *mqttClientTransport) Publish(topic string, payload []byte, qos ability.MQTTQos, retain bool) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.connected {
		return fmt.Errorf("not connected")
	}
	vh := encodeMQTTString(topic)
	fixed := byte(0x30)
	if retain {
		fixed |= 0x01
	}
	var body []byte
	if qos == 1 {
		fixed |= 0x02
		pid := make([]byte, 2)
		binary.BigEndian.PutUint16(pid, 1)
		body = append(body, vh...)
		body = append(body, pid...)
		body = append(body, payload...)
	} else {
		body = append(vh, payload...)
	}
	if err := writeMQTTPacket(t.conn, fixed, body); err != nil {
		return err
	}
	if qos == 1 {
		return t.waitAck(0x40, 3*time.Second)
	}
	return nil
}

func (t *mqttClientTransport) Subscribe(topic string, qos ability.MQTTQos) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.connected {
		return fmt.Errorf("not connected")
	}
	pid := make([]byte, 2)
	binary.BigEndian.PutUint16(pid, 2)
	body := append(pid, encodeMQTTString(topic)...)
	body = append(body, byte(qos))
	if err := writeMQTTPacket(t.conn, 0x82, body); err != nil {
		return err
	}
	return t.waitAck(0x90, 3*time.Second)
}

func (t *mqttClientTransport) Unsubscribe(topic string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.connected {
		return fmt.Errorf("not connected")
	}
	pid := make([]byte, 2)
	binary.BigEndian.PutUint16(pid, 3)
	body := append(pid, encodeMQTTString(topic)...)
	if err := writeMQTTPacket(t.conn, 0xA2, body); err != nil {
		return err
	}
	return t.waitAck(0xB0, 3*time.Second)
}

// --- MQTT 编解码辅助 ---

func encodeMQTTString(s string) []byte {
	b := make([]byte, 2+len(s))
	binary.BigEndian.PutUint16(b, uint16(len(s)))
	copy(b[2:], s)
	return b
}

func decodeMQTTString(b []byte, off int) (string, int) {
	if off+2 > len(b) {
		return "", off
	}
	l := int(binary.BigEndian.Uint16(b[off : off+2]))
	off += 2
	if off+l > len(b) {
		return "", off
	}
	return string(b[off : off+l]), off + l
}

func decodeRemainingLength(b []byte) (int, int) {
	mult := 1
	val := 0
	for i := 0; i < len(b); i++ {
		digit := int(b[i])
		val += (digit & 0x7F) * mult
		if digit&0x80 == 0 {
			return val, i + 1
		}
		mult *= 128
	}
	return val, len(b)
}

// encodeRemainingLength 按 MQTT 3.1.1 规范编码剩余长度(变长, 1-4 字节,
// 高 7 位为值、MSB 为延续位)。旧实现 writeMQTTPacket 用单字节
// byte(len(body)), ≥128 字节的包会被截断/损坏。
func encodeRemainingLength(n int) []byte {
	var out []byte
	for {
		digit := byte(n % 128)
		n /= 128
		if n > 0 {
			digit |= 0x80
		}
		out = append(out, digit)
		if n == 0 {
			return out
		}
	}
}

func writeMQTTPacket(conn net.Conn, fixed byte, body []byte) error {
	rl := encodeRemainingLength(len(body))
	out := make([]byte, 0, 1+len(rl)+len(body))
	out = append(out, fixed)
	out = append(out, rl...)
	out = append(out, body...)
	_, err := conn.Write(out)
	return err
}
