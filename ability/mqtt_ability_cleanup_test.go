package ability

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FasterEdge/FasterEdge/types"
)

// slowDisconnectTransport delays Disconnect so we can exercise the bounded
// wait semantics.
type slowDisconnectTransport struct {
	*fakeMQTTTransport
	disconnectDelay time.Duration
	started         chan struct{}
}

func (s *slowDisconnectTransport) Disconnect() error {
	if s.started != nil {
		select {
		case <-s.started:
		default:
			close(s.started)
		}
	}
	if s.disconnectDelay > 0 {
		time.Sleep(s.disconnectDelay)
	}
	return s.fakeMQTTTransport.Disconnect()
}

// TestMQTTUnmountClosesQueuesAndTransport verifies Unmount detaches every
// subscription queue and disconnects the transport.
func TestMQTTUnmountClosesQueuesAndTransport(t *testing.T) {
	ft := &fakeMQTTTransport{}
	m := NewMQTTAbility()
	atom := newMQTTAtom(t)
	m.SetTransport(ft)
	if out := m.Command(atom, MQTTCommandConnect, nil); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := m.Command(atom, MQTTCommandSubscribe, MQTTSubscribeArgs{Topic: "t1", Qos: 0}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if err := m.Unmount(context.Background(), atom); err != nil {
		t.Fatal(err)
	}
	if ft.IsConnected() {
		t.Fatal("transport not disconnected")
	}
	if out := m.Command(atom, MQTTCommandListSubs, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if subs, _ := out.Value.([]string); len(subs) != 0 {
		t.Fatalf("expected empty subs after unmount, got %v", subs)
	}
}

// TestMQTTUnmountTimeoutOnDisconnect ensures Unmount returns ctx.Err()
// when the transport refuses to disconnect quickly.
func TestMQTTUnmountTimeoutOnDisconnect(t *testing.T) {
	inner := &fakeMQTTTransport{}
	slow := &slowDisconnectTransport{
		fakeMQTTTransport: inner,
		disconnectDelay:   200 * time.Millisecond,
		started:           make(chan struct{}),
	}
	m := NewMQTTAbility()
	atom := newMQTTAtom(t)
	m.SetTransport(slow)
	if out := m.Command(atom, MQTTCommandConnect, nil); out.Err != nil {
		t.Fatal(out.Err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := m.Unmount(ctx, atom)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	<-slow.started
}

// TestMQTTRejectsSubscribeAfterUnmount ensures the closing gate blocks new
// subscriptions after Unmount.
func TestMQTTRejectsSubscribeAfterUnmount(t *testing.T) {
	ft := &fakeMQTTTransport{}
	m := NewMQTTAbility()
	atom := newMQTTAtom(t)
	m.SetTransport(ft)
	if err := m.Unmount(context.Background(), atom); err != nil {
		t.Fatal(err)
	}
	if out := m.Command(atom, MQTTCommandSubscribe, MQTTSubscribeArgs{Topic: "t", Qos: 0}); out.Err == nil {
		t.Fatal("expected subscribe to be rejected after Unmount")
	}
	if out := m.Command(atom, MQTTCommandConnect, nil); out.Err == nil {
		t.Fatal("expected connect to be rejected after Unmount")
	}
	if out := m.Command(atom, MQTTCommandPublish, MQTTPublishArgs{Topic: "t", Payload: []byte("x")}); out.Err == nil {
		t.Fatal("expected publish to be rejected after Unmount")
	}
}

// TestMQTTPushMessageAfterUnmount ensures PushMessage is rejected.
func TestMQTTPushMessageAfterUnmount(t *testing.T) {
	ft := &fakeMQTTTransport{}
	m := NewMQTTAbility()
	atom := newMQTTAtom(t)
	m.SetTransport(ft)
	if out := m.Command(atom, MQTTCommandConnect, nil); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := m.Command(atom, MQTTCommandSubscribe, MQTTSubscribeArgs{Topic: "t", Qos: 0}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if err := m.Unmount(context.Background(), atom); err != nil {
		t.Fatal(err)
	}
	if m.PushMessage(MQTTMessage{Topic: "t", Payload: []byte("x"), Qos: 0, ReceivedAt: time.Now()}) {
		t.Fatal("expected push to be rejected after Unmount")
	}
}

// TestMQTTConcurrentSubscribeVsUnmount ensures no panics or double-creates.
func TestMQTTConcurrentSubscribeVsUnmount(t *testing.T) {
	ft := &fakeMQTTTransport{}
	m := NewMQTTAbility()
	atom := newMQTTAtom(t)
	m.SetTransport(ft)
	var wg sync.WaitGroup
	var ok atomic.Int32
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out := m.Command(atom, MQTTCommandSubscribe, MQTTSubscribeArgs{Topic: "topic-" + string(rune('a'+i%5)), Qos: 0})
			if out.Err == nil {
				ok.Add(1)
			}
		}(i)
	}
	go func() {
		_ = m.Unmount(context.Background(), atom)
	}()
	wg.Wait()
	// Some subscriptions might have happened before unmount; we just need no
	// data races or panics.
	_ = ok.Load()
}

// TestMQTTUnmountIdempotent validates safe repeated calls.
func TestMQTTUnmountIdempotent(t *testing.T) {
	m := NewMQTTAbility()
	atom := newMQTTAtom(t)
	if err := m.Unmount(context.Background(), atom); err != nil {
		t.Fatal(err)
	}
	if err := m.Unmount(context.Background(), atom); err != nil {
		t.Fatal(err)
	}
}

// TestMQTTUnmountNilContext covers the input validation path.
func TestMQTTUnmountNilContext(t *testing.T) {
	m := NewMQTTAbility()
	if err := m.Unmount(nil, nil); !errors.Is(err, types.ErrNilContext) {
		t.Fatalf("err=%v", err)
	}
}

// Compile-time assertion that MQTTAbility satisfies Unmounter.
var _ types.Unmounter = (*MQTTAbility)(nil)
