package types

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ringCapacity is the maximum number of duration samples retained per phase
// inside a MetricsCollector. Older samples are overwritten in FIFO order.
const ringCapacity = 1024

// metricsNamespace 是导出指标的 Prometheus 命名空间前缀。FasterEdge 是库而非
// 进程, 指标名与使用方自己的命名空间分离, 便于挂上 job / instance 标签。
const metricsNamespace = "fasteredge"

// MetricsCollector is an EventSink that aggregates lifecycle activity into
// per-phase counts and a bounded ring buffer of duration samples. It is
// safe for concurrent use.
type MetricsCollector struct {
	mu        sync.Mutex
	startedAt time.Time
	counts    map[string]uint64          // "<phase>:<status>" -> count
	durations map[string][]time.Duration // phase -> ring buffer of durations
	ringPos   map[string]int             // phase -> next write index
	ringFull  map[string]bool            // phase -> ring buffer filled at least once
}

func NewMetricsCollector() *MetricsCollector {
	return &MetricsCollector{
		startedAt: time.Now(),
		counts:    map[string]uint64{},
		durations: map[string][]time.Duration{},
		ringPos:   map[string]int{},
		ringFull:  map[string]bool{},
	}
}

// ObserveLifecycle counts terminal events and records their duration into
// the per-phase ring buffer. EventStart events are intentionally ignored
// because they have no terminal counterpart and would double-count.
func (m *MetricsCollector) ObserveLifecycle(e LifecycleEvent) {
	if m == nil {
		return
	}
	if e.Status == EventStart {
		return
	}
	key := string(e.Phase) + ":" + string(e.Status)
	m.mu.Lock()
	m.counts[key]++
	phase := string(e.Phase)
	buf := m.durations[phase]
	if len(buf) == 0 {
		buf = make([]time.Duration, ringCapacity)
		m.durations[phase] = buf
	}
	pos := m.ringPos[phase]
	if pos >= ringCapacity {
		pos = 0
		m.ringFull[phase] = true
	}
	buf[pos] = e.Duration
	m.ringPos[phase] = pos + 1
	m.mu.Unlock()
}

// MetricsSnapshot is a defensive copy of a MetricsCollector's state.
type MetricsSnapshot struct {
	StartedAt time.Time
	Counts    map[string]uint64
	Durations map[string][]time.Duration
}

// Snapshot returns a defensive copy of the current counts and durations.
// The durations slices are also copied so callers may sort or analyse them
// without affecting the collector.
func (m *MetricsCollector) Snapshot() MetricsSnapshot {
	if m == nil {
		return MetricsSnapshot{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	counts := make(map[string]uint64, len(m.counts))
	for k, v := range m.counts {
		counts[k] = v
	}
	durations := make(map[string][]time.Duration, len(m.durations))
	for phase, buf := range m.durations {
		var length int
		if m.ringFull[phase] {
			length = ringCapacity
		} else {
			length = m.ringPos[phase]
		}
		cp := make([]time.Duration, length)
		copy(cp, buf[:length])
		durations[phase] = cp
	}
	return MetricsSnapshot{StartedAt: m.startedAt, Counts: counts, Durations: durations}
}

// Compile-time assertion.
var _ EventSink = (*MetricsCollector)(nil)

// ---------------------------------------------------------------------------
// 导出渲染: Prometheus 文本格式 + 简单 JSON
//
// 渲染只读 Snapshot() 的防御性副本, 不触碰收集器内部状态, 因此可以在任意
// goroutine 并发调用, 也可以与正在发生的 ObserveLifecycle 同时进行。
// ---------------------------------------------------------------------------

// PhaseCount 是单一 phase 在单一 EventStatus 下的观测次数。Prometheus 与 JSON
// 两种渲染共用它, 保证同一份快照在两种格式里给出同一组数字。
type PhaseCount struct {
	Phase  LifecyclePhase
	Status EventStatus
	Count  uint64
}

// PhaseStats 是单一 phase 的时长统计。Count 为 0 表示该 phase 从未产生终态
// 事件, 此时所有时长字段为 0(不会凭空造出样本)。
type PhaseStats struct {
	Phase LifecyclePhase
	Count int // 参与统计的样本数(0 = 无数据)
	Min   time.Duration
	Max   time.Duration
	Sum   time.Duration
	Mean  time.Duration
	P50   time.Duration // 中位数
	P95   time.Duration
	P99   time.Duration
}

// MetricsReport 是 MetricsCollector 的可导出视图: 按 phase/status 的计数, 加
// 每相位的时长分位数。它只含值不持引用, 可安全跨 goroutine 传递。
type MetricsReport struct {
	StartedAt time.Time
	Counts    []PhaseCount
	Phases    []PhaseStats
}

// Report 把当前快照整理成可导出的视图。Counts 与 Phases 均按名称排序, 渲染
// 输出因此是确定性的(同快照 → 同字节), 便于测试断言与 diff。
func (m *MetricsCollector) Report() MetricsReport {
	snap := m.Snapshot()
	report := MetricsReport{StartedAt: snap.StartedAt}

	keys := make([]string, 0, len(snap.Counts))
	for k := range snap.Counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	report.Counts = make([]PhaseCount, 0, len(keys))
	for _, k := range keys {
		phase, status := splitCountKey(k)
		report.Counts = append(report.Counts, PhaseCount{Phase: LifecyclePhase(phase), Status: EventStatus(status), Count: snap.Counts[k]})
	}

	phases := make([]string, 0, len(snap.Durations))
	for p := range snap.Durations {
		phases = append(phases, p)
	}
	sort.Strings(phases)
	report.Phases = make([]PhaseStats, 0, len(phases))
	for _, p := range phases {
		report.Phases = append(report.Phases, phaseStats(LifecyclePhase(p), snap.Durations[p]))
	}
	return report
}

// splitCountKey 把 MetricsCollector 的 "<phase>:<status>" 计数键拆回原始字段。
// 这是该键格式的唯一解析点: 拼接发生在 ObserveLifecycle, 拆分发生在这里,
// 两处必须同步演进。
func splitCountKey(key string) (phase, status string) {
	if i := strings.IndexByte(key, ':'); i >= 0 {
		return key[:i], key[i+1:]
	}
	return key, ""
}

func phaseStats(phase LifecyclePhase, samples []time.Duration) PhaseStats {
	st := PhaseStats{Phase: phase, Count: len(samples)}
	if len(samples) == 0 {
		return st
	}
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	st.Min = sorted[0]
	st.Max = sorted[len(sorted)-1]
	st.P50 = percentile(sorted, 0.50)
	st.P95 = percentile(sorted, 0.95)
	st.P99 = percentile(sorted, 0.99)

	var sum time.Duration
	for _, d := range sorted {
		sum += d
	}
	st.Sum = sum
	st.Mean = sum / time.Duration(len(sorted))
	return st
}

// percentile 对已升序排序的样本取最近秩分位数(不做插值)。
// rank = ceil(q*n) 是 Prometheus 与常见监控系统的口径, 保证单样本时任意分
// 位数都等于该样本本身, 不会凭空造出 0。注意必须向上取整: 用截断的
// int(q*n) 会让 n=5 时的 p50 落到第 2 个样本(10ms)而不是第 3 个(20ms)。
func percentile(sorted []time.Duration, q float64) time.Duration {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	rank := int(math.Ceil(q * float64(n)))
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return sorted[rank-1]
}

// durationSeconds 把 Duration 渲染成 Prometheus/JSON 共用的秒字符串。
// 'g' 格式在纳秒级精度下不会补无意义的尾零。
func durationSeconds(d time.Duration) string {
	return strconv.FormatFloat(float64(d)/float64(time.Second), 'g', -1, 64)
}

func durationMillis(d time.Duration) int64 {
	return int64(d / time.Millisecond)
}

// prometheusEscape 转义 label 值中的反斜杠、双引号与换行。组件名由用户注册,
// 框架不做字符假设——不转义会产出无法被 Prometheus 解析的指标行。
func prometheusEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return r.Replace(s)
}

// WritePrometheus 以 Prometheus 文本格式写出当前快照。指标族:
//
//	fasteredge_lifecycle_events_total{phase,status}   counter
//	fasteredge_phase_duration_seconds{phase,quantile} summary
//	fasteredge_phase_duration_seconds_count{phase}
//	fasteredge_phase_duration_seconds_sum{phase}
//
// 输出可直接作为 `text/plain; version=0.0.4` 响应体。从未产生终态事件的
// phase 不出现在任何指标里——"没发生过"与"发生 0 次"在 Prometheus 中同义,
// 查询方用 absent() 区分。
func (m *MetricsCollector) WritePrometheus() string {
	if m == nil {
		return ""
	}
	return renderPrometheus(m.Report())
}

// prometheusLabel 渲染一个已转义的 label 值为带引号的形式。刻意不用 %q:
// %q 会对反斜杠再做一层 Go 转义, 与 prometheusEscape 叠加后输出
// `\\` -> `\\\\`(双重转义), Prometheus 解析出的字符串因此与原值不符。
func prometheusLabel(s string) string {
	return `"` + prometheusEscape(s) + `"`
}

func renderPrometheus(report MetricsReport) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# HELP fasteredge_lifecycle_events_total Lifecycle events observed by phase and status.\n")
	fmt.Fprintf(&b, "# TYPE fasteredge_lifecycle_events_total counter\n")
	for _, c := range report.Counts {
		fmt.Fprintf(&b, "%s_lifecycle_events_total{phase=%s,status=%s} %d\n",
			metricsNamespace, prometheusLabel(string(c.Phase)), prometheusLabel(string(c.Status)), c.Count)
	}

	fmt.Fprintf(&b, "# HELP fasteredge_phase_duration_seconds Lifecycle phase duration quantiles in seconds.\n")
	fmt.Fprintf(&b, "# TYPE fasteredge_phase_duration_seconds summary\n")
	for _, st := range report.Phases {
		phase := prometheusLabel(string(st.Phase))
		// summary 族: 同名 metric 下带 quantile 标签的 φ 分位数, 配 _count /
		// _sum 两个无 quantile 标签的后缀样本。
		fmt.Fprintf(&b, "%s_phase_duration_seconds_count{phase=%s} %d\n", metricsNamespace, phase, st.Count)
		fmt.Fprintf(&b, "%s_phase_duration_seconds_sum{phase=%s} %s\n", metricsNamespace, phase, durationSeconds(st.Sum))
		for _, q := range []struct {
			name string
			d    time.Duration
		}{{"0.5", st.P50}, {"0.95", st.P95}, {"0.99", st.P99}} {
			fmt.Fprintf(&b, "%s_phase_duration_seconds{phase=%s,quantile=%q} %s\n",
				metricsNamespace, phase, q.name, durationSeconds(q.d))
		}
	}
	return b.String()
}

// WriteJSON 以简单 JSON 写出当前快照。时长同时给出毫秒整数与秒浮点: 毫秒供
// 断言比对, 秒供 Grafana 直接消费, 省去调用方自行换算。字段经 json.Marshal
// 转义, 组件名不做字符假设。
func (m *MetricsCollector) WriteJSON() (string, error) {
	if m == nil {
		return "null", nil
	}
	return renderJSON(m.Report())
}

type jsonPhaseCount struct {
	Phase  string `json:"phase"`
	Status string `json:"status"`
	Count  uint64 `json:"count"`
}

type jsonPhaseStats struct {
	Phase    string `json:"phase"`
	Count    int    `json:"count"`
	MinMS    int64  `json:"min_ms"`
	MaxMS    int64  `json:"max_ms"`
	MeanMS   int64  `json:"mean_ms"`
	P50MS    int64  `json:"p50_ms"`
	P95MS    int64  `json:"p95_ms"`
	P99MS    int64  `json:"p99_ms"`
	MeanSecs string `json:"mean_seconds"`
	P99Secs  string `json:"p99_seconds"`
}

type jsonReport struct {
	StartedAt time.Time        `json:"started_at"`
	Counts    []jsonPhaseCount `json:"counts"`
	Phases    []jsonPhaseStats `json:"phases"`
}

func renderJSON(report MetricsReport) (string, error) {
	out := jsonReport{
		StartedAt: report.StartedAt,
		Counts:    make([]jsonPhaseCount, 0, len(report.Counts)),
		Phases:    make([]jsonPhaseStats, 0, len(report.Phases)),
	}
	for _, c := range report.Counts {
		out.Counts = append(out.Counts, jsonPhaseCount{Phase: string(c.Phase), Status: string(c.Status), Count: c.Count})
	}
	for _, st := range report.Phases {
		out.Phases = append(out.Phases, jsonPhaseStats{
			Phase:    string(st.Phase),
			Count:    st.Count,
			MinMS:    durationMillis(st.Min),
			MaxMS:    durationMillis(st.Max),
			MeanMS:   durationMillis(st.Mean),
			P50MS:    durationMillis(st.P50),
			P95MS:    durationMillis(st.P95),
			P99MS:    durationMillis(st.P99),
			MeanSecs: durationSeconds(st.Mean),
			P99Secs:  durationSeconds(st.P99),
		})
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}
