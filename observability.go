// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package FasterEdge

import (
	"fmt"

	"github.com/FasterEdge/FasterEdge/types"
)

// ---------------------------------------------------------------------------
// 可观测性装配入口
//
// 埋点(types.LifecycleEvent)在 lifecycle.go / command.go 的各相位已经发出,
// 缺的是"装配"——使用方此前必须自己 import types、自己组合 sink。这里的
// WithEventSink / WithMetrics 把这一步收拢成一行, 让"把指标接上"不需要读
// types 内部。
//
// 设计约束: 只做装配, 不起 goroutine、不起 HTTP server、不写文件。FasterEdge
// 是库不是进程, 导出通道(scrape 端口 / 日志 / 推送)由使用方决定。
// ---------------------------------------------------------------------------

// EventSinkOption 把一个 EventSink 装配到 Atom 上。返回错误以保持与
// InitAtom/PreRunAtom 相同的"错误上浮"风格, 并便于将来扩展更多装配动作。
type EventSinkOption func(*types.Atom) error

// WithEventSink 把任意 EventSink 挂到 Atom。传 nil sink 是 no-op(与
// types.Atom.AddEventSink 语义一致), 便于调用方按开关拼装。
//
// 多次调用会叠加: 第一次 SetEventSink, 之后经 MultiEventSink 扇出, 因此
// "日志 recorder + 指标 collector"可以并存而不会互相覆盖。
func WithEventSink(sink types.EventSink) EventSinkOption {
	return func(atom *types.Atom) error {
		if atom == nil {
			return types.ErrNilAtom
		}
		if sink == nil {
			return nil
		}
		return atom.AddEventSink(sink)
	}
}

// WithMetrics 装配一个 MetricsCollector 并把返回值交给调用方, 使"装配"与
// "持有导出器"在一次调用里完成:
//
//	metrics, err := fasteredge.WithMetrics(atom)
//	if err != nil { return err }
//	defer func() { fmt.Print(metrics.WritePrometheus()) }()
//
// 返回的 *types.MetricsCollector 实现 types.EventSink, 因此也可以直接再喂给
// WithEventSink 之外的扇出链。
func WithMetrics(atom *types.Atom) (*types.MetricsCollector, error) {
	if atom == nil {
		return nil, types.ErrNilAtom
	}
	metrics := types.NewMetricsCollector()
	if err := atom.AddEventSink(metrics); err != nil {
		return nil, fmt.Errorf("attach metrics collector: %w", err)
	}
	return metrics, nil
}

// WithEventSinkAndMetrics 是上面两者的组合: 同一个调用里挂上额外 sink 与
// MetricsCollector, 省去调用方两次错误处理。
func WithEventSinkAndMetrics(sink types.EventSink, atom *types.Atom) (*types.MetricsCollector, error) {
	metrics, err := WithMetrics(atom)
	if err != nil {
		return nil, err
	}
	if err := WithEventSink(sink)(atom); err != nil {
		return nil, err
	}
	return metrics, nil
}

// WithOptions 按顺序应用装配项, 任一失败即中止并返回该错误(不部分装配:
// 中途失败时已挂上的 sink 不回滚——卸载 sink 会引入"装配失败却改变了可观测
// 行为"的更差语义, 由调用方决定重试或丢弃整个 Atom)。
func WithOptions(atom *types.Atom, opts ...EventSinkOption) error {
	if atom == nil {
		return types.ErrNilAtom
	}
	for _, opt := range opts {
		if opt == nil {
			return fmt.Errorf("nil EventSinkOption: %w", types.ErrInvalidArguments)
		}
		if err := opt(atom); err != nil {
			return err
		}
	}
	return nil
}
