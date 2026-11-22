// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package FasterEdge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/FasterEdge/FasterEdge/types"
)

// timeoutCtx 是给卸载路径用的上下文。用 Background 派生一个 5 秒预算的
// 上下文, 而不是已取消的上下文——后者会改变被测行为。
func timeoutCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestWithMetricsExportsRealLifecycleData 是"埋点真的被接上了"的端到端证据:
// 用 InitStandardAtom 跑一次真实的 mount/run/unmount, 再检查导出文本里出现
// 对应相位的样本。只 import 不调用是接不上的——这一步把装配入口与真实
// 生命周期数据绑在一起。
func TestWithMetricsExportsRealLifecycleData(t *testing.T) {
	atom := InitStandardAtom()

	metrics, err := WithMetrics(atom)
	if err != nil {
		t.Fatal(err)
	}

	if err := PreRunAtom(atom); err != nil {
		t.Fatalf("PreRun: %v", err)
	}
	// 走一次 Atom 的分派路径(atom.Command 而非 component.Command), 让
	// command 相位也有样本——只有经过 Atom.dispatchContext 才会发埋点。
	if out := atom.Command("TimeAbility", "get_time", nil); out.Err != nil {
		t.Fatalf("command: %v", out.Err)
	}
	if err := UnmountAtom(timeoutCtx(t), atom); err != nil {
		t.Fatalf("UnmountAtom: %v", err)
	}

	snap := metrics.Snapshot()
	if snap.Counts["check:success"] == 0 || snap.Counts["mount:success"] == 0 {
		t.Fatalf("expected check/mount counts, got %+v", snap.Counts)
	}
	if snap.Counts["unmount:success"] == 0 {
		t.Fatalf("expected unmount counts, got %+v", snap.Counts)
	}

	prom := metrics.WritePrometheus()
	for _, want := range []string{
		`fasteredge_lifecycle_events_total{phase="mount",status="success"}`,
		`fasteredge_lifecycle_events_total{phase="command",status="success"}`,
		`fasteredge_phase_duration_seconds_count{phase="unmount"}`,
	} {
		if !strings.Contains(prom, want) {
			t.Fatalf("prometheus output missing %q\n%s", want, prom)
		}
	}

	raw, err := metrics.WriteJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"phase": "mount"`) {
		t.Fatalf("json output missing mount phase\n%s", raw)
	}
}

func TestWithEventSinkComposesWithMetrics(t *testing.T) {
	atom := InitStandardAtom()
	rec := &types.EventRecorder{}

	metrics, err := WithEventSinkAndMetrics(rec, atom)
	if err != nil {
		t.Fatal(err)
	}
	if metrics == nil {
		t.Fatal("WithEventSinkAndMetrics returned nil collector")
	}

	if err := PreRunAtom(atom); err != nil {
		t.Fatal(err)
	}
	if len(rec.Events()) == 0 {
		t.Fatal("recorder saw no events")
	}
	if metrics.Snapshot().Counts["mount:success"] == 0 {
		t.Fatal("metrics collector saw no mount events")
	}
}

func TestWithOptionsAppliesInOrder(t *testing.T) {
	atom := InitStandardAtom()
	if err := WithOptions(atom,
		WithEventSink(&types.EventRecorder{}),
		WithEventSink(&types.EventRecorder{}),
		WithEventSink(nil), // no-op, 不得报错
	); err != nil {
		t.Fatal(err)
	}
	if err := PreRunAtom(atom); err != nil {
		t.Fatal(err)
	}

	// nil option 必须被拒绝, 否则调用方拼错装配项会静默丢失可观测性。
	if err := WithOptions(atom, nil); !errors.Is(err, types.ErrInvalidArguments) {
		t.Fatalf("nil option err=%v", err)
	}
	// nil atom 一律返回 ErrNilAtom, 不 panic。
	if err := WithOptions(nil); !errors.Is(err, types.ErrNilAtom) {
		t.Fatalf("nil atom err=%v", err)
	}
	if _, err := WithMetrics(nil); !errors.Is(err, types.ErrNilAtom) {
		t.Fatalf("WithMetrics(nil) err=%v", err)
	}
	if err := WithEventSink(nil)(nil); !errors.Is(err, types.ErrNilAtom) {
		t.Fatalf("WithEventSink(nil)(nil) err=%v", err)
	}
}

func TestWithMetricsIsIsolatedPerAtom(t *testing.T) {
	// 两个 atom 各自装配, 指标不得串味。
	atom1, atom2 := InitStandardAtom(), InitStandardAtom()
	m1, err := WithMetrics(atom1)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := WithMetrics(atom2)
	if err != nil {
		t.Fatal(err)
	}
	if err := PreRunAtom(atom1); err != nil {
		t.Fatal(err)
	}
	// atom1 挂载过, atom2 没有: 两个 collector 必须各自只反映自己的 atom。
	if m1.Snapshot().Counts["mount:success"] == 0 {
		t.Fatal("atom1 collector saw no mount events")
	}
	if _, ok := m2.Snapshot().Counts["mount:success"]; ok {
		t.Fatal("atom2 collector must not see atom1's events")
	}
}
