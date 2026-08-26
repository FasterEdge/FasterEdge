package ability

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/FasterEdge/FasterEdge/types"
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

// MQTTAbility 在 Transport 之上提供 MQTT 客户端命令,并维护每个订阅的本地消息队列。
type MQTTAbility struct {
	mu        sync.RWMutex
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
		q.cond.L.Lock()
		q.mu.Unlock()
		time.AfterFunc(wait, func() { q.cond.Broadcast() })
		q.cond.Wait()
		q.mu.Lock()
		q.cond.L.Unlock()
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
	m.mu.RUnlock()
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
		m.mu.RLock()
		transport := m.transport
		m.mu.RUnlock()
		if transport == nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no transport: %w", act, types.ErrInvalidArguments)}
		}
		if err := transport.Connect(); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %v", act, types.ErrInvalidArguments, err)}
		}
		return types.CommandOutput{Name: act, Value: true}
	case MQTTCommandDisconnect:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		m.mu.RLock()
		transport := m.transport
		queues := m.queues
		m.mu.RUnlock()
		if transport != nil {
			_ = transport.Disconnect()
		}
		for _, q := range queues {
			q.close()
		}
		m.mu.Lock()
		m.queues = make(map[string]*mqttQueue)
		m.mu.Unlock()
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
		topic := strings.TrimSpace(typed.Topic)
		if topic == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: empty topic: %w", act, types.ErrInvalidArguments)}
		}
		if typed.Qos > MQTTQos2 {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: invalid qos: %w", act, types.ErrInvalidArguments)}
		}
		m.mu.RLock()
		transport := m.transport
		m.mu.RUnlock()
		if transport == nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no transport: %w", act, types.ErrInvalidArguments)}
		}
		if err := transport.Publish(topic, typed.Payload, typed.Qos, typed.Retain); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %v", act, types.ErrInvalidArguments, err)}
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
		if typed.Qos > MQTTQos2 {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: invalid qos: %w", act, types.ErrInvalidArguments)}
		}
		m.mu.RLock()
		transport := m.transport
		_, exists := m.queues[topic]
		m.mu.RUnlock()
		if exists {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: already subscribed: %w", act, types.ErrInvalidArguments)}
		}
		if transport == nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: no transport: %w", act, types.ErrInvalidArguments)}
		}
		if err := transport.Subscribe(topic, typed.Qos); err != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w: %v", act, types.ErrInvalidArguments, err)}
		}
		m.mu.Lock()
		m.queues[topic] = newMQTTQueue(typed.MaxQueue)
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

// isAcceptableBrokerURL 限制 MQTT URL 必须是 tcp/tls/ssl/ws/wss 协议,且 host 不为本地回环/私网。
// 这与 TimeAbility / CloudRoleAbility 的网络策略一致。
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
			if host == "" || host == "localhost" || host == "127.0.0.1" || host == "0.0.0.0" || host == "::1" {
				return false
			}
			return true
		}
	}
	return false
}
