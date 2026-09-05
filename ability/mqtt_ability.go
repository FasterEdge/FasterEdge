// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package ability

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/FasterEdge/FasterEdge/types"
)

const (
	// mqttMaxPayloadBytes 是单条消息负载的上限(1 MiB),防恶意 broker/调用方
	// 通过无限大 payload 造成内存耗尽(队列长度有界但字节数原本无界)。
	mqttMaxPayloadBytes = 1 << 20
)

const (
	MQTTCommandSetBroker      = "set_broker"
	MQTTCommandGetBroker      = "get_broker"
	MQTTCommandSetCredentials = "set_credentials"
	MQTTCommandConnect        = "connect"
	MQTTCommandDisconnect     = "disconnect"
	MQTTCommandIsConnected    = "is_connected"
	MQTTCommandPublish        = "publish"
	MQTTCommandSubscribe      = "subscribe"
	MQTTCommandUnsubscribe    = "unsubscribe"
	MQTTCommandListSubs       = "list_subscriptions"
	MQTTCommandDrain          = "drain"
	MQTTCommandSetClientID    = "set_client_id"
)

// MQTTQos 是 MQTT 服务质量等级。
type MQTTQos uint8

const (
	MQTTQos0 MQTTQos = 0
	MQTTQos1 MQTTQos = 1
	MQTTQos2 MQTTQos = 2
)

// MQTTBrokerArgs 是 set_broker 的参数。
type MQTTBrokerArgs struct {
	URL string // tcp://host:1883 或 ssl://...
}

// MQTTCredentialsArgs 是 set_credentials 的参数。
type MQTTCredentialsArgs struct {
	Username string
	Password string
}

// MQTTClientIDArgs 是 set_client_id 的参数。
type MQTTClientIDArgs struct {
	ClientID string
}

// MQTTPublishArgs 是 publish 的参数。
type MQTTPublishArgs struct {
	Topic   string
	Payload []byte
	Qos     MQTTQos
	Retain  bool
}

// MQTTSubscribeArgs 是 subscribe 的参数。
type MQTTSubscribeArgs struct {
	Topic    string
	Qos      MQTTQos
	MaxQueue int // 消息队列容量,0 表示默认 256
}

// MQTTTopicArg 是 unsubscribe / drain 的参数。
type MQTTTopicArg struct {
	Topic string
}

// MQTTPullArgs 是 drain 的参数(限制最大条数与超时)。
type MQTTPullArgs struct {
	Topic   string
	Max     int
	Timeout time.Duration
}

// MQTTMessage 是 publish 回调/订阅消息的统一结构。
type MQTTMessage struct {
	Topic      string
	Payload    []byte
	Qos        MQTTQos
	Retain     bool
	ReceivedAt time.Time
}

// MQTTTransport 抽象出真实 MQTT 客户端。Publish 应在未连接时返回 error。
type MQTTTransport interface {
	Connect() error
	Disconnect() error
	IsConnected() bool
	Publish(topic string, payload []byte, qos MQTTQos, retain bool) error
	Subscribe(topic string, qos MQTTQos) error
	Unsubscribe(topic string) error
}

// mqttClosingError 表示能力已被 Unmount 关闭后再次接收新命令。
var mqttClosingError = errors.New("MQTTAbility is closing")

// MQTTAbility 在 Transport 之上提供 MQTT 客户端命令,并维护每个订阅的本地消息队列。
type MQTTAbility struct {
	mu        sync.RWMutex
	closing   bool
	broker    string
	clientID  string
	username  string
	password  string
	queues    map[string]*mqttQueue
	transport MQTTTransport
}

type mqttQueue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    []MQTTMessage
	max    int
	closed bool
}

func newMQTTQueue(max int) *mqttQueue {
	if max <= 0 {
		max = 256
	}
	q := &mqttQueue{max: max}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *mqttQueue) push(m MQTTMessage) bool {
	if len(m.Payload) > mqttMaxPayloadBytes {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return false
	}
	if len(q.buf) >= q.max {
		// 队列满,丢弃最早
		q.buf = q.buf[1:]
	}
	q.buf = append(q.buf, m)
	q.cond.Signal()
	return true
}

func (q *mqttQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.cond.Broadcast()
	q.mu.Unlock()
}

func (q *mqttQueue) drain(max int, timeout time.Duration) []MQTTMessage {
	if max < 0 {
		// 防御: 负值会在 q.buf[:max] 处 slice panic
		max = 0
	}
	deadline := time.Now().Add(timeout)
	q.mu.Lock()
	defer q.mu.Unlock()
	if timeout == 0 {
		if max > len(q.buf) {
			max = len(q.buf)
		}
		out := append([]MQTTMessage(nil), q.buf[:max]...)
		q.buf = q.buf[max:]
		return out
	}
	for len(q.buf) == 0 && !q.closed && time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		wait := remaining
		if wait > 50*time.Millisecond {
			wait = 50 * time.Millisecond
		}
		// cond.L 与 q.mu 是同一把锁 (sync.NewCond(&q.mu)), 调用方已持有:
		// 直接 Wait 会在内部释放并重新获取锁, 切勿再次 Lock, 否则同锁不可重入而死锁。
		// 用定时广播模拟带超时的条件等待。
		timer := time.AfterFunc(wait, func() { q.cond.Broadcast() })
		q.cond.Wait()
		timer.Stop()
	}
	if max <= 0 || max > len(q.buf) {
		max = len(q.buf)
	}
	out := append([]MQTTMessage(nil), q.buf[:max]...)
	q.buf = q.buf[max:]
	return out
}

func NewMQTTAbility() *MQTTAbility {
	return &MQTTAbility{
		queues:   make(map[string]*mqttQueue),
		clientID: "FasterEdge-" + fmt.Sprintf("%d", time.Now().UnixNano()),
	}
}

func (m *MQTTAbility) SetTransport(t MQTTTransport) {
	m.mu.Lock()
	m.transport = t
	m.mu.Unlock()
}

// PushMessage 供外部 Transport 收到消息后回调,把消息推入对应队列。
// 这是 MQTTAbility 对外暴露的关键方法,Transport 收到订阅消息后必须调用它。
func (m *MQTTAbility) PushMessage(msg MQTTMessage) bool {
	m.mu.RLock()
	q, ok := m.queues[msg.Topic]
	closing := m.closing
	m.mu.RUnlock()
	if closing {
		return false
	}
	if !ok {
		return false
	}
	return q.push(msg)
}

func (m *MQTTAbility) GetName() string { return "MQTTAbility" }

func (m *MQTTAbility) Describe() string {
	return "MQTTAbility提供 MQTT 客户端能力:连接/断开/发布/订阅,每个订阅维护本地消息队列,通过外部 Transport 桥接真实 broker。"
}

func (m *MQTTAbility) Check(atom *types.Atom) error {
	if atom == nil {
		return types.ErrMissingDependency
	}
	if _, ok := atom.Data("BaseData"); !ok {
		return types.ErrMissingDependency
	}
	return nil
}

func (m *MQTTAbility) Mount(atom *types.Atom) error { return m.Check(atom) }

// beginShutdown 标记为 closing。返回是否成功。
func (m *MQTTAbility) beginShutdown() bool {
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return false
	}
	m.closing = true
	m.mu.Unlock()
	return true
}

// Unmount tears down all subscriptions and disconnects the transport under a
// bounded context. Subscriptions are detached before transport disconnect so
// that no new command can recreate queues after Unmount has run.
func (m *MQTTAbility) Unmount(ctx context.Context, _ *types.Atom) error {
	if ctx == nil {
		return types.ErrNilContext
	}
	if !m.beginShutdown() {
		return nil
	}
	m.mu.Lock()
	transport := m.transport
	queues := m.queues
	m.queues = make(map[string]*mqttQueue)
	m.mu.Unlock()
	for _, q := range queues {
		q.close()
	}
	if transport == nil {
		return nil
	}
	result := make(chan error, 1)
	go func() { result <- transport.Disconnect() }()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		// Disconnect 阻塞时等待一个有限窗口让 goroutine 退出, 避免每次
		// Unmount 泄漏一个 goroutine + 底层连接。Go 无法强制终止 goroutine,
		// 但窗口期给了正常慢连接退出机会。
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-result:
		case <-timer.C:
		}
		return ctx.Err()
	}
}

// rejectIfClosing 用于拒绝 closing 之后到达的新命令。
func (m *MQTTAbility) rejectIfClosing(act string) *types.CommandOutput {
	m.mu.RLock()
	closing := m.closing
	m.mu.RUnlock()
	if closing {
		out := types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, mqttClosingError)}
		return &out
	}
	return nil
}

func (m *MQTTAbility) Command(atom *types.Atom, act string, args any) types.CommandOutput {
	if err := m.Check(atom); err != nil {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, err)}
	}
	switch act {
	case MQTTCommandSetBroker:
		typed, ok := args.(MQTTBrokerArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		url := strings.TrimSpace(typed.URL)
		if !isAcceptableBrokerURL(url) {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: invalid broker url: %w", act, types.ErrInvalidArguments)}
		}
		m.mu.Lock()
		m.broker = url
		m.mu.Unlock()
		return types.CommandOutput{Name: act, Value: url}
	case MQTTCommandGetBroker:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		m.mu.RLock()
		defer m.mu.RUnlock()
		return types.CommandOutput{Name: act, Value: m.broker}
	case MQTTCommandSetCredentials:
		typed, ok := args.(MQTTCredentialsArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		m.mu.Lock()
		m.username = strings.TrimSpace(typed.Username)
		m.password = typed.Password
		m.mu.Unlock()
		return types.CommandOutput{Name: act}
	case MQTTCommandSetClientID:
		typed, ok := args.(MQTTClientIDArgs)
		if !ok || strings.TrimSpace(typed.ClientID) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		m.mu.Lock()
		m.clientID = strings.TrimSpace(typed.ClientID)
		m.mu.Unlock()
		return types.CommandOutput{Name: act, Value: m.clientID}
	case MQTTCommandConnect:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		if out := m.rejectIfClosing(act); out != nil {
			return *out
		}
		m.mu.RLock()
		transport := m.transport
		m.mu.RUnlock()
		if transport == nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no transport: %w", act, types.ErrInvalidArguments)}
		}
		if err := transport.Connect(); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %w", act, types.ErrOperationFailed, err)}
		}
		// 断线重连(clean session)后 broker 端订阅清空: 恢复本地队列对应的
		// 全部订阅, 否则消息静默永久丢失。恢复统一用 QoS0(能力层不记录
		// 订阅时用的 QoS; 需要 QoS1/2 的场景应由 transport 层做持久订阅)。
		m.mu.RLock()
		topics := make([]string, 0, len(m.queues))
		for t := range m.queues {
			topics = append(topics, t)
		}
		m.mu.RUnlock()
		for _, t := range topics {
			// 逐个重订阅前锁内复查队列仍在: 并发 Unsubscribe 已删队列并完成
			// wire 退订时, 对同一 topic 重新 Subscribe 会在 broker 端产生
			// "幽灵订阅"(消息持续投递但本地无队列、也无法再退订)——与
			// subscribe 命令的占位+复查二次校验同型, 队列已删则跳过。
			m.mu.RLock()
			_, stillThere := m.queues[t]
			m.mu.RUnlock()
			if !stillThere {
				continue
			}
			_ = transport.Subscribe(t, 0)
		}
		return types.CommandOutput{Name: act, Value: true}
	case MQTTCommandDisconnect:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		if out := m.rejectIfClosing(act); out != nil {
			return *out
		}
		m.mu.Lock()
		transport := m.transport
		queues := m.queues
		m.queues = make(map[string]*mqttQueue)
		m.mu.Unlock()
		if transport != nil {
			_ = transport.Disconnect()
		}
		for _, q := range queues {
			q.close()
		}
		return types.CommandOutput{Name: act, Value: true}
	case MQTTCommandIsConnected:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		m.mu.RLock()
		transport := m.transport
		m.mu.RUnlock()
		if transport == nil {
			return types.CommandOutput{Name: act, Value: false}
		}
		return types.CommandOutput{Name: act, Value: transport.IsConnected()}
	case MQTTCommandPublish:
		typed, ok := args.(MQTTPublishArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		if out := m.rejectIfClosing(act); out != nil {
			return *out
		}
		topic := strings.TrimSpace(typed.Topic)
		if topic == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: empty topic: %w", act, types.ErrInvalidArguments)}
		}
		if len(topic) > 65535 {
			// MQTT UTF-8 字符串上限: 超长 topic 会被参考 transport 的
			// uint16 长度字段截断成畸形包。
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: topic too long: %w", act, types.ErrInvalidArguments)}
		}
		if strings.ContainsAny(topic, "+#") {
			// 发布不允许通配符(MQTT 规范): 向 broker 发布含 +/# 的 topic
			// 会污染订阅匹配面。
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: publish topic must not contain wildcards: %w", act, types.ErrInvalidArguments)}
		}
		if typed.Qos > MQTTQos2 {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: invalid qos: %w", act, types.ErrInvalidArguments)}
		}
		if len(typed.Payload) > mqttMaxPayloadBytes {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: payload exceeds %d bytes: %w", act, mqttMaxPayloadBytes, types.ErrInvalidArguments)}
		}
		m.mu.RLock()
		transport := m.transport
		m.mu.RUnlock()
		if transport == nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no transport: %w", act, types.ErrInvalidArguments)}
		}
		if err := transport.Publish(topic, typed.Payload, typed.Qos, typed.Retain); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %w", act, types.ErrOperationFailed, err)}
		}
		return types.CommandOutput{Name: act, Value: topic}
	case MQTTCommandSubscribe:
		typed, ok := args.(MQTTSubscribeArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		topic := strings.TrimSpace(typed.Topic)
		if topic == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: empty topic: %w", act, types.ErrInvalidArguments)}
		}
		if len(topic) > 65535 {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: topic too long: %w", act, types.ErrInvalidArguments)}
		}
		if typed.Qos > MQTTQos2 {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: invalid qos: %w", act, types.ErrInvalidArguments)}
		}
		// 锁内做状态检查与队列占位, transport.Subscribe(网络往返)在锁外执行:
		// 旧实现整段持有写锁, 若 transport 回调路径调用 PushMessage(RLock)
		// 且恰有消息到达, 会形成"回调等锁 -> SUBACK 无人处理 -> Subscribe 永不返回"
		// 的死锁, 慢 broker 也会让所有 MQTT 操作全局串行停顿。
		m.mu.Lock()
		if m.closing {
			m.mu.Unlock()
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, mqttClosingError)}
		}
		if _, exists := m.queues[topic]; exists {
			m.mu.Unlock()
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: already subscribed: %w", act, types.ErrInvalidArguments)}
		}
		transport := m.transport
		if transport == nil {
			m.mu.Unlock()
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no transport: %w", act, types.ErrInvalidArguments)}
		}
		m.queues[topic] = newMQTTQueue(typed.MaxQueue)
		m.mu.Unlock()
		if err := transport.Subscribe(topic, typed.Qos); err != nil {
			m.mu.Lock()
			if q, ok := m.queues[topic]; ok {
				q.close()
				delete(m.queues, topic)
			}
			m.mu.Unlock()
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %w", act, types.ErrOperationFailed, err)}
		}
		// Subscribe 成功后确保队列存在: 占位队列可能被并发 Unsubscribe
		// 删除(其 transport.Unsubscribe 与本次 Subscribe 竞争)。若已被删除,
		// 本次 wire 订阅即成"无主订阅"(broker 已订阅但无人收消息)——撤销
		// wire 订阅并明确失败, 避免幽灵订阅/静默丢消息。
		m.mu.Lock()
		if _, ok := m.queues[topic]; !ok {
			m.mu.Unlock()
			if transport != nil {
				_ = transport.Unsubscribe(topic)
			}
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: subscription cancelled concurrently: %w", act, types.ErrInvalidArguments)}
		}
		m.mu.Unlock()
		return types.CommandOutput{Name: act, Value: topic}
	case MQTTCommandUnsubscribe:
		typed, ok := args.(MQTTTopicArg)
		if !ok || strings.TrimSpace(typed.Topic) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		topic := strings.TrimSpace(typed.Topic)
		m.mu.Lock()
		q, ok := m.queues[topic]
		if ok {
			delete(m.queues, topic)
		}
		transport := m.transport
		m.mu.Unlock()
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: not subscribed: %w", act, types.ErrInvalidArguments)}
		}
		q.close()
		if transport != nil {
			_ = transport.Unsubscribe(topic)
		}
		return types.CommandOutput{Name: act, Value: topic}
	case MQTTCommandListSubs:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		m.mu.RLock()
		out := make([]string, 0, len(m.queues))
		for t := range m.queues {
			out = append(out, t)
		}
		m.mu.RUnlock()
		return types.CommandOutput{Name: act, Value: out}
	case MQTTCommandDrain:
		typed, ok := args.(MQTTPullArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		topic := strings.TrimSpace(typed.Topic)
		if topic == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: empty topic: %w", act, types.ErrInvalidArguments)}
		}
		if typed.Timeout < 0 {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: timeout must be non-negative: %w", act, types.ErrInvalidArguments)}
		}
		if typed.Max < 0 {
			// 负值 Max 会让 drain 的 q.buf[:max] 触发 slice panic(旧实现只查 Timeout)
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: max must be non-negative: %w", act, types.ErrInvalidArguments)}
		}
		m.mu.RLock()
		q, ok := m.queues[topic]
		m.mu.RUnlock()
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: not subscribed: %w", act, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: q.drain(typed.Max, typed.Timeout)}
	}
	return types.CommandOutput{Name: act, Err: fmt.Errorf("command %s: %w", act, types.ErrUnsupportedCommand)}
}

// isAcceptableBrokerURL 限制 MQTT URL 必须是 tcp/tls/ssl/ws/wss 协议,且 host 不为本地回环。
// 这与 TimeAbility / CloudRoleAbility 的网络策略一致(私网 broker 允许, 回环拒绝)。
// host 提取使用标准 URL 解析取 Hostname(): 旧实现按最后一个冒号切分会把
// userinfo 手法(tcp://alice@127.0.0.1:1883)提取成 "alice@127.0.0.1" 绕过回环名单;
// IP 字面量经 netip 规范化(IPv4-mapped IPv6)后再判回环。
func isAcceptableBrokerURL(u string) bool {
	if u == "" {
		return false
	}
	lower := strings.ToLower(u)
	for _, prefix := range []string{"tcp://", "tls://", "ssl://", "ws://", "wss://"} {
		if strings.HasPrefix(lower, prefix) {
			rest := u[len(prefix):]
			host := rest
			if i := strings.IndexAny(rest, "/?#"); i >= 0 {
				host = rest[:i]
			}
			if i := strings.LastIndex(host, ":"); i >= 0 {
				host = host[:i]
			}
			// IPv6 字面量带方括号, 先剥掉再比较回环 (如 tcp://[::1]:1883)
			if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") && len(host) >= 2 {
				host = host[1 : len(host)-1]
			}
			if host == "" {
				return false
			}
			// userinfo 防护 + 严格解析: 旧实现只有在 url.Parse 成功时才用
			// parsed.Hostname(), 失败时(畸形 host/空白/截断方括号/空端口)
			// fall through 到主机名分支返回 true——"tcp://["、"tcp://host:"、
			// "tcp://a b:1883" 全部放行, 晚到 dial 才失败。
			parsed, perr := url.Parse(u)
			if perr != nil || parsed.Hostname() == "" || parsed.Port() == "" {
				return false
			}
			if parsed.User != nil {
				return false
			}
			host = parsed.Hostname()
			// IPv6 字面量 Hostname() 带方括号([::1]), 剥掉再判回环
			if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") && len(host) >= 2 {
				host = host[1 : len(host)-1]
			}
			if host == "" {
				return false
			}
			// IP 字面量: netip 规范化后判回环/未指定
			if addr, aerr := netip.ParseAddr(host); aerr == nil {
				addr = addr.Unmap()
				if addr.IsLoopback() || addr.IsUnspecified() {
					return false
				}
				return true
			}
			// 主机名: 去掉尾点 FQDN 后比较回环别名
			if strings.TrimSuffix(host, ".") == "localhost" {
				return false
			}
			return true
		}
	}
	return false
}

// Compile-time guarantee that MQTTAbility satisfies Unmounter.
var _ types.Unmounter = (*MQTTAbility)(nil)
