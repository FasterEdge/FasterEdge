package ability

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/FasterEdge/FasterEdge/data"
	"github.com/FasterEdge/FasterEdge/types"
)

type fakeMQTTTransport struct {
	mu           sync.Mutex
	connected    bool
	published    []MQTTMessage
	subscribed   []string
	connectErr   error
	publishErr   error
	subscribeErr error
}

func (f *fakeMQTTTransport) Connect() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.connectErr != nil {
		return f.connectErr
	}
	f.connected = true
	return nil
}

func (f *fakeMQTTTransport) Disconnect() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connected = false
	return nil
}

func (f *fakeMQTTTransport) IsConnected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connected
}

func (f *fakeMQTTTransport) Publish(topic string, payload []byte, qos MQTTQos, retain bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.publishErr != nil {
		return f.publishErr
	}
	f.published = append(f.published, MQTTMessage{Topic: topic, Payload: payload, Qos: qos, Retain: retain})
	return nil
}

func (f *fakeMQTTTransport) Subscribe(topic string, qos MQTTQos) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.subscribeErr != nil {
		return f.subscribeErr
	}
	f.subscribed = append(f.subscribed, topic)
	if qos > MQTTQos0 {
		// 占位
	}
	return nil
}

func (f *fakeMQTTTransport) Unsubscribe(topic string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return nil
}

func newMQTTAtom(t *testing.T) *types.Atom {
	t.Helper()
	a := &types.Atom{}
	if err := a.AddData(&data.BaseData{}); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestMQTTAbilityRejectsMissingDeps(t *testing.T) {
	m := NewMQTTAbility()
	if out := m.Command(&types.Atom{}, MQTTCommandGetBroker, nil); !errors.Is(out.Err, types.ErrMissingDependency) {
		t.Fatalf("missing deps error = %v", out.Err)
	}
}

func TestMQTTAbilitySetGetBroker(t *testing.T) {
	m := NewMQTTAbility()
	atom := newMQTTAtom(t)
	// 类型错误
	if out := m.Command(atom, MQTTCommandSetBroker, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set wrong type error = %v", out.Err)
	}
	for _, bad := range []string{
		"", "http://x", "tcp://localhost:1883", "tcp://127.0.0.1:1883", "tcp://0.0.0.0:1883",
		"tcp://", "tcp://[::1]:1883", "tcp://::1:1883",
		// 安全回归: IPv4-mapped IPv6 回环与 userinfo 手法必须拒绝
		"tcp://[::ffff:127.0.0.1]:1883", "tcp://alice@127.0.0.1:1883", "tcp://alice@[::ffff:127.0.0.1]:1883",
	} {
		if out := m.Command(atom, MQTTCommandSetBroker, MQTTBrokerArgs{URL: bad}); !errors.Is(out.Err, types.ErrInvalidArguments) {
			t.Errorf("bad url %q should reject, got %v", bad, out.Err)
		}
	}
	if out := m.Command(atom, MQTTCommandSetBroker, MQTTBrokerArgs{URL: "tcp://broker.example.com:1883"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := m.Command(atom, MQTTCommandGetBroker, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get with args error = %v", out.Err)
	}
	if out := m.Command(atom, MQTTCommandGetBroker, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if out.Value != "tcp://broker.example.com:1883" {
		t.Fatalf("broker = %q", out.Value)
	}
}

func TestMQTTAbilitySetClientAndCreds(t *testing.T) {
	m := NewMQTTAbility()
	atom := newMQTTAtom(t)
	if out := m.Command(atom, MQTTCommandSetClientID, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set client wrong type error = %v", out.Err)
	}
	if out := m.Command(atom, MQTTCommandSetClientID, MQTTClientIDArgs{ClientID: ""}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set client empty error = %v", out.Err)
	}
	if out := m.Command(atom, MQTTCommandSetClientID, MQTTClientIDArgs{ClientID: "client-1"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := m.Command(atom, MQTTCommandSetCredentials, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("creds wrong type error = %v", out.Err)
	}
	if out := m.Command(atom, MQTTCommandSetCredentials, MQTTCredentialsArgs{Username: "u", Password: "p"}); out.Err != nil {
		t.Fatal(out.Err)
	}
}

func TestMQTTAbilityConnectDisconnect(t *testing.T) {
	m := NewMQTTAbility()
	atom := newMQTTAtom(t)
	ft := &fakeMQTTTransport{}
	m.SetTransport(ft)
	if out := m.Command(atom, MQTTCommandConnect, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("connect with args error = %v", out.Err)
	}
	if out := m.Command(atom, MQTTCommandConnect, nil); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := m.Command(atom, MQTTCommandIsConnected, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("is_connected with args error = %v", out.Err)
	}
	if out := m.Command(atom, MQTTCommandIsConnected, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if v, _ := out.Value.(bool); !v {
		t.Fatal("expected connected = true")
	}
	if out := m.Command(atom, MQTTCommandDisconnect, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("disconnect with args error = %v", out.Err)
	}
	if out := m.Command(atom, MQTTCommandDisconnect, nil); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := m.Command(atom, MQTTCommandIsConnected, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if v, _ := out.Value.(bool); v {
		t.Fatal("expected connected = false after disconnect")
	}
}

func TestMQTTAbilityNoTransport(t *testing.T) {
	m := NewMQTTAbility()
	atom := newMQTTAtom(t)
	if out := m.Command(atom, MQTTCommandConnect, nil); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("connect no transport error = %v", out.Err)
	}
	if out := m.Command(atom, MQTTCommandPublish, MQTTPublishArgs{Topic: "t", Payload: []byte("x")}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("publish no transport error = %v", out.Err)
	}
	if out := m.Command(atom, MQTTCommandIsConnected, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if v, _ := out.Value.(bool); v {
		t.Fatal("is_connected should be false without transport")
	}
}

func TestMQTTAbilityPublish(t *testing.T) {
	m := NewMQTTAbility()
	atom := newMQTTAtom(t)
	ft := &fakeMQTTTransport{}
	m.SetTransport(ft)
	if out := m.Command(atom, MQTTCommandPublish, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("publish wrong type error = %v", out.Err)
	}
	if out := m.Command(atom, MQTTCommandPublish, MQTTPublishArgs{Topic: "", Payload: []byte("x")}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("publish empty topic error = %v", out.Err)
	}
	if out := m.Command(atom, MQTTCommandPublish, MQTTPublishArgs{Topic: "t", Payload: []byte("x"), Qos: 5}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("publish bad qos error = %v", out.Err)
	}
	if out := m.Command(atom, MQTTCommandPublish, MQTTPublishArgs{Topic: "t", Payload: []byte("hello"), Qos: 1, Retain: true}); out.Err != nil {
		t.Fatal(out.Err)
	}
	ft.mu.Lock()
	if len(ft.published) != 1 || string(ft.published[0].Payload) != "hello" || !ft.published[0].Retain {
		t.Fatalf("published = %+v", ft.published)
	}
	ft.mu.Unlock()
}

func TestMQTTAbilitySubscribeDrain(t *testing.T) {
	m := NewMQTTAbility()
	atom := newMQTTAtom(t)
	ft := &fakeMQTTTransport{}
	m.SetTransport(ft)
	// 类型错误
	if out := m.Command(atom, MQTTCommandSubscribe, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("subscribe wrong type error = %v", out.Err)
	}
	if out := m.Command(atom, MQTTCommandSubscribe, MQTTSubscribeArgs{Topic: "", Qos: 1}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("subscribe empty topic error = %v", out.Err)
	}
	if out := m.Command(atom, MQTTCommandSubscribe, MQTTSubscribeArgs{Topic: "t", Qos: 5}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("subscribe bad qos error = %v", out.Err)
	}
	// 正常
	if out := m.Command(atom, MQTTCommandSubscribe, MQTTSubscribeArgs{Topic: "sensors/temp", Qos: 1, MaxQueue: 4}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// 重复
	if out := m.Command(atom, MQTTCommandSubscribe, MQTTSubscribeArgs{Topic: "sensors/temp", Qos: 1}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("subscribe duplicate error = %v", out.Err)
	}
	// list
	if out := m.Command(atom, MQTTCommandListSubs, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("list with args error = %v", out.Err)
	}
	if out := m.Command(atom, MQTTCommandListSubs, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if subs, _ := out.Value.([]string); len(subs) != 1 || subs[0] != "sensors/temp" {
		t.Fatalf("subs = %v", subs)
	}
	// 推入消息
	m.PushMessage(MQTTMessage{Topic: "sensors/temp", Payload: []byte("v1"), Qos: 1, ReceivedAt: time.Now()})
	m.PushMessage(MQTTMessage{Topic: "sensors/temp", Payload: []byte("v2"), Qos: 1, ReceivedAt: time.Now()})
	// drain
	if out := m.Command(atom, MQTTCommandDrain, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("drain wrong type error = %v", out.Err)
	}
	if out := m.Command(atom, MQTTCommandDrain, MQTTPullArgs{Topic: "", Max: 10}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("drain empty topic error = %v", out.Err)
	}
	if out := m.Command(atom, MQTTCommandDrain, MQTTPullArgs{Topic: "sensors/temp", Max: 10, Timeout: -1}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("drain negative timeout error = %v", out.Err)
	}
	if out := m.Command(atom, MQTTCommandDrain, MQTTPullArgs{Topic: "missing", Max: 10}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("drain not subscribed error = %v", out.Err)
	}
	if out := m.Command(atom, MQTTCommandDrain, MQTTPullArgs{Topic: "sensors/temp", Max: 10, Timeout: 100 * time.Millisecond}); out.Err != nil {
		t.Fatal(out.Err)
	} else if msgs, _ := out.Value.([]MQTTMessage); len(msgs) != 2 {
		t.Fatalf("drain = %#v", msgs)
	}
	// unsubscribe
	if out := m.Command(atom, MQTTCommandUnsubscribe, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("unsub wrong type error = %v", out.Err)
	}
	if out := m.Command(atom, MQTTCommandUnsubscribe, MQTTTopicArg{Topic: ""}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("unsub empty error = %v", out.Err)
	}
	if out := m.Command(atom, MQTTCommandUnsubscribe, MQTTTopicArg{Topic: "sensors/temp"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := m.Command(atom, MQTTCommandUnsubscribe, MQTTTopicArg{Topic: "sensors/temp"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("unsub missing error = %v", out.Err)
	}
}

func TestMQTTAbilityQueueOverflow(t *testing.T) {
	m := NewMQTTAbility()
	atom := newMQTTAtom(t)
	ft := &fakeMQTTTransport{}
	m.SetTransport(ft)
	m.Command(atom, MQTTCommandSubscribe, MQTTSubscribeArgs{Topic: "t", Qos: 0, MaxQueue: 2})
	for i := 0; i < 5; i++ {
		m.PushMessage(MQTTMessage{Topic: "t", Payload: []byte{byte(i)}, Qos: 0, ReceivedAt: time.Now()})
	}
	out := m.Command(atom, MQTTCommandDrain, MQTTPullArgs{Topic: "t", Max: 10})
	msgs, _ := out.Value.([]MQTTMessage)
	if len(msgs) != 2 {
		t.Fatalf("expected 2, got %d", len(msgs))
	}
	// 应保留最新的 2 条
	if msgs[0].Payload[0] != 3 || msgs[1].Payload[0] != 4 {
		t.Fatalf("queue order wrong: %v", msgs)
	}
}

func TestMQTTAbilityConnectError(t *testing.T) {
	m := NewMQTTAbility()
	atom := newMQTTAtom(t)
	ft := &fakeMQTTTransport{connectErr: errors.New("refused")}
	m.SetTransport(ft)
	if out := m.Command(atom, MQTTCommandConnect, nil); !errors.Is(out.Err, types.ErrOperationFailed) {
		t.Fatalf("connect err = %v", out.Err)
	}
}

// TestMQTTAbilityPublishSubscribeErrors 补 fake 定义了却从未注入的 publishErr/
// subscribeErr 分支: 发布/订阅的 transport 失败路径原零测试(只测过 connectErr)。
func TestMQTTAbilityPublishSubscribeErrors(t *testing.T) {
	m := NewMQTTAbility()
	atom := newMQTTAtom(t)
	ft := &fakeMQTTTransport{}
	m.SetTransport(ft)
	ft.publishErr = errors.New("broker down")
	if out := m.Command(atom, MQTTCommandPublish, MQTTPublishArgs{Topic: "t", Payload: []byte("x")}); !errors.Is(out.Err, types.ErrOperationFailed) {
		t.Fatalf("publish transport err = %v", out.Err)
	}
	ft.publishErr = nil
	ft.subscribeErr = errors.New("no suback")
	if out := m.Command(atom, MQTTCommandSubscribe, MQTTSubscribeArgs{Topic: "sensors/temp", Qos: 1}); !errors.Is(out.Err, types.ErrOperationFailed) {
		t.Fatalf("subscribe transport err = %v", out.Err)
	}
	// 订阅失败必须回滚队列占位: 重试订阅应成功(而非报 already subscribed)
	ft.subscribeErr = nil
	if out := m.Command(atom, MQTTCommandSubscribe, MQTTSubscribeArgs{Topic: "sensors/temp", Qos: 1}); out.Err != nil {
		t.Fatalf("resubscribe after failure: %v", out.Err)
	}
}

func TestMQTTAbilityUnknownCommand(t *testing.T) {
	m := NewMQTTAbility()
	atom := newMQTTAtom(t)
	if out := m.Command(atom, "nope", nil); !errors.Is(out.Err, types.ErrUnsupportedCommand) {
		t.Fatalf("unknown error = %v", out.Err)
	}
}
