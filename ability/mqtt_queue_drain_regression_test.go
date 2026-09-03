package ability

// 回归测试: drain 空订阅队列 + 超时必须返回, 不能死锁。
// 背景: mqttQueue 用 sync.NewCond(&q.mu) 创建 (cond.L == &q.mu),
// 而 drain 等待分支里先持有 q.mu 又对 q.cond.L 二次 Lock() —— 同一把互斥锁不可重入,
// 一旦进入空队列等待即永久死锁。此测试确保超时路径能正常返回。

import (
	"testing"
	"time"
)

func TestMQTTQueueDrainEmptyWithTimeoutDoesNotDeadlock(t *testing.T) {
	q := newMQTTQueue(16)

	done := make(chan struct{})
	go func() {
		// 空队列 + 200ms 超时: 修复前会永久阻塞
		out := q.drain(10, 200*time.Millisecond)
		if len(out) != 0 {
			t.Errorf("expected empty drain, got %v", out)
		}
		close(done)
	}()

	select {
	case <-done:
		// 正常返回
	case <-time.After(2 * time.Second):
		t.Fatal("drain on empty queue with timeout deadlocked (double lock on q.cond.L == q.mu)")
	}
}

func TestMQTTQueueDrainEmptyWithTimeoutThenPush(t *testing.T) {
	q := newMQTTQueue(16)

	go func() {
		// 先进入等待, 100ms 后推入消息, 应被 cond.Broadcast 唤醒
		time.Sleep(100 * time.Millisecond)
		q.push(MQTTMessage{Topic: "t", Payload: []byte("late")})
	}()

	out := q.drain(10, 5*time.Second)
	if len(out) != 1 || string(out[0].Payload) != "late" {
		t.Fatalf("expected 1 late message, got %v", out)
	}
}