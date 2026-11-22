package types

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// buildMetrics 用一个真实生命周期喂收集器, 使渲染测试面对"由埋点产生的数据"
// 而不是手工构造的事件——渲染口径与埋点口径必须同时被验证。
func buildMetrics(t *testing.T) *MetricsCollector {
	t.Helper()
	a := &Atom{}
	m := NewMetricsCollector()
	if err := a.SetEventSink(m); err != nil {
		t.Fatal(err)
	}
	if err := a.AddAbility(&recordingAbility{name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := a.MountAll(); err != nil {
		t.Fatal(err)
	}
	if err := a.UnmountAll(context.Background(), time.Second); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMetricsReportAggregatesCountsAndQuantiles(t *testing.T) {
	m := NewMetricsCollector()
	for _, d := range []time.Duration{10 * time.Millisecond, 30 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond} {
		m.ObserveLifecycle(LifecycleEvent{Phase: PhaseMount, Status: EventSuccess, Duration: d})
	}
	m.ObserveLifecycle(LifecycleEvent{Phase: PhaseMount, Status: EventFailure, Duration: time.Millisecond})

	report := m.Report()
	if len(report.Counts) != 2 {
		t.Fatalf("counts=%+v want 2 entries", report.Counts)
	}
	// Counts 按 "<phase>:<status>" 排序, 因此 failure 在前。
	if report.Counts[0].Phase != PhaseMount || report.Counts[0].Status != EventFailure || report.Counts[0].Count != 1 {
		t.Fatalf("counts[0]=%+v", report.Counts[0])
	}
	if report.Counts[1].Status != EventSuccess || report.Counts[1].Count != 4 {
		t.Fatalf("counts[1]=%+v", report.Counts[1])
	}

	var stats PhaseStats
	for _, st := range report.Phases {
		if st.Phase == PhaseMount {
			stats = st
		}
	}
	if stats.Count != 5 {
		t.Fatalf("mount samples=%d want 5", stats.Count)
	}
	if stats.Min != time.Millisecond || stats.Max != 40*time.Millisecond {
		t.Fatalf("min=%s max=%s", stats.Min, stats.Max)
	}
	if stats.Sum != 101*time.Millisecond {
		t.Fatalf("sum=%s want 101ms", stats.Sum)
	}
	if stats.Mean != 101*time.Millisecond/5 {
		t.Fatalf("mean=%s", stats.Mean)
	}
	// 升序 [1,10,20,30,40]ms: 最近秩口径下 p50=rank3=20ms, p95/p99=rank5=40ms。
	if stats.P50 != 20*time.Millisecond {
		t.Fatalf("p50=%s want 20ms", stats.P50)
	}
	if stats.P95 != 40*time.Millisecond || stats.P99 != 40*time.Millisecond {
		t.Fatalf("p95=%s p99=%s", stats.P95, stats.P99)
	}
}

func TestMetricsReportPercentileOfSingleSampleIsSampleItself(t *testing.T) {
	// 单样本时最近秩分位数必须等于该样本, 不能插值成 0(fake 的成功路径)。
	if got := percentile([]time.Duration{7 * time.Millisecond}, 0.50); got != 7*time.Millisecond {
		t.Fatalf("p50=%s", got)
	}
	if got := percentile([]time.Duration{7 * time.Millisecond}, 0.99); got != 7*time.Millisecond {
		t.Fatalf("p99=%s", got)
	}
	if got := percentile(nil, 0.99); got != 0 {
		t.Fatalf("empty p99=%s", got)
	}
}

func TestWritePrometheusRendersParseableText(t *testing.T) {
	m := buildMetrics(t)
	text := m.WritePrometheus()

	for _, want := range []string{
		"# TYPE fasteredge_lifecycle_events_total counter",
		`fasteredge_lifecycle_events_total{phase="check",status="success"} 1`,
		`fasteredge_lifecycle_events_total{phase="mount",status="success"} 1`,
		`fasteredge_lifecycle_events_total{phase="unmount",status="success"} 1`,
		`fasteredge_phase_duration_seconds_count{phase="mount"} 1`,
		`fasteredge_phase_duration_seconds{phase="mount",quantile="0.99"}`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("prometheus output missing %q\n---\n%s", want, text)
		}
	}

	// 每行必须是 "# ..." 注释或 "metric{labels} value" —— 否则 Prometheus
	// 会整段抓取失败, 而"能渲染出字符串"却通不过真实 scrape。
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.Contains(line, "} ") {
			t.Fatalf("malformed metric line: %q", line)
		}
		if strings.Count(line, "{") != strings.Count(line, "}") {
			t.Fatalf("unbalanced braces: %q", line)
		}
	}
}

func TestWritePrometheusEscapesLabelValues(t *testing.T) {
	m := NewMetricsCollector()
	m.ObserveLifecycle(LifecycleEvent{Phase: LifecyclePhase(`we"ird\phase`), Status: EventSuccess, Duration: time.Millisecond})
	text := m.WritePrometheus()
	want := `phase="we\"ird\\phase"`
	if !strings.Contains(text, want) {
		t.Fatalf("label not escaped, want %s\n---\n%s", want, text)
	}
}

func TestWriteJSONRoundTrips(t *testing.T) {
	m := buildMetrics(t)
	raw, err := m.WriteJSON()
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		StartedAt time.Time `json:"started_at"`
		Counts    []struct {
			Phase  string `json:"phase"`
			Status string `json:"status"`
			Count  uint64 `json:"count"`
		} `json:"counts"`
		Phases []struct {
			Phase string `json:"phase"`
			Count int    `json:"count"`
			P99MS int64  `json:"p99_ms"`
			P99S  string `json:"p99_seconds"`
		} `json:"phases"`
	}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, raw)
	}
	if decoded.StartedAt.IsZero() {
		t.Fatal("started_at missing")
	}
	counts := map[string]uint64{}
	for _, c := range decoded.Counts {
		counts[c.Phase+":"+c.Status] = c.Count
	}
	if counts["mount:success"] != 1 || counts["check:success"] != 1 {
		t.Fatalf("counts=%v", counts)
	}
	if len(decoded.Phases) == 0 {
		t.Fatal("phases missing")
	}
	for _, p := range decoded.Phases {
		if p.P99S == "" {
			t.Fatalf("phase %s missing p99_seconds", p.Phase)
		}
	}
}

// nil 接收者被视为"没有装配指标", 渲染空串——库代码不假设调用方已初始化
// 收集器, 也不因为缺少埋点而 panic。
func TestMetricsExportOnEmptyCollectorIsSafe(t *testing.T) {
	m := NewMetricsCollector()
	if text := m.WritePrometheus(); strings.Contains(text, "fasteredge_lifecycle_events_total{") {
		t.Fatalf("empty collector rendered samples:\n%s", text)
	}
	raw, err := m.WriteJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"counts": []`) {
		t.Fatalf("empty JSON=%s", raw)
	}
	var nilMetrics *MetricsCollector
	if text := nilMetrics.WritePrometheus(); text != "" {
		t.Fatalf("nil collector text=%q", text)
	}
	if raw, err := nilMetrics.WriteJSON(); err != nil || raw != "null" {
		t.Fatalf("nil collector json=%q err=%v", raw, err)
	}
}
