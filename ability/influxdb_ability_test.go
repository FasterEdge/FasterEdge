package ability

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/FasterEdge/FasterEdge/data"
	"github.com/FasterEdge/FasterEdge/types"
)

type fakeInfluxTransport struct {
	mu       sync.Mutex
	pingErr  error
	writeErr error
	queryErr error
	written  []InfluxPoint
	pinged   int
	queried  string
	rows     []map[string]any
}

func (f *fakeInfluxTransport) Ping() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pinged++
	return f.pingErr
}

func (f *fakeInfluxTransport) Write(points []InfluxPoint) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeErr != nil {
		return f.writeErr
	}
	f.written = append(f.written, points...)
	return nil
}

func (f *fakeInfluxTransport) Query(flux string) ([]map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queried = flux
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	return f.rows, nil
}

func newInfluxAtom(t *testing.T) *types.Atom {
	t.Helper()
	a := &types.Atom{}
	if err := a.AddData(&data.BaseData{}); err != nil {
		t.Fatal(err)
	}
	if err := a.AddData(data.NewInfluxDBData()); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestInfluxAbilityRejectsMissingDeps(t *testing.T) {
	i := NewInfluxAbility()
	if out := i.Command(&types.Atom{}, InfluxCommandGetConfig, nil); !errors.Is(out.Err, types.ErrMissingDependency) {
		t.Fatalf("missing deps error = %v", out.Err)
	}
}

func TestInfluxAbilitySetEndpoint(t *testing.T) {
	i := NewInfluxAbility()
	atom := newInfluxAtom(t)
	if out := i.Command(atom, InfluxCommandSetEndpoint, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set wrong type error = %v", out.Err)
	}
	for _, bad := range []string{"", "ftp://x", "http://localhost:8086", "http://127.0.0.1", "http://0.0.0.0"} {
		if out := i.Command(atom, InfluxCommandSetEndpoint, InfluxConfigArgs{Value: bad}); !errors.Is(out.Err, types.ErrInvalidArguments) {
			t.Errorf("bad url %q should reject, got %v", bad, out.Err)
		}
	}
	if out := i.Command(atom, InfluxCommandSetEndpoint, InfluxConfigArgs{Value: "https://influx.example.com:8086"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := i.Command(atom, InfluxCommandGetConfig, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if cfg, _ := out.Value.(InfluxConfig); cfg.Endpoint != "https://influx.example.com:8086" {
		t.Fatalf("endpoint = %q", cfg.Endpoint)
	}
}

func TestInfluxAbilitySetTokenOrgBucket(t *testing.T) {
	i := NewInfluxAbility()
	atom := newInfluxAtom(t)
	if out := i.Command(atom, InfluxCommandSetToken, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("set wrong type error = %v", out.Err)
	}
	if out := i.Command(atom, InfluxCommandSetToken, InfluxConfigArgs{Value: "short"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("short token error = %v", out.Err)
	}
	if out := i.Command(atom, InfluxCommandSetToken, InfluxConfigArgs{Value: "this-is-a-valid-token-32bytes+"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := i.Command(atom, InfluxCommandSetOrg, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("org wrong type error = %v", out.Err)
	}
	for _, bad := range []string{"", "has space", "中文"} {
		if out := i.Command(atom, InfluxCommandSetOrg, InfluxConfigArgs{Value: bad}); !errors.Is(out.Err, types.ErrInvalidArguments) {
			t.Errorf("org %q should reject, got %v", bad, out.Err)
		}
	}
	if out := i.Command(atom, InfluxCommandSetOrg, InfluxConfigArgs{Value: "my-org"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := i.Command(atom, InfluxCommandSetBucket, InfluxConfigArgs{Value: "my-bucket"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := i.Command(atom, InfluxCommandGetConfig, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("get with args error = %v", out.Err)
	}
	cfgOut := i.Command(atom, InfluxCommandGetConfig, nil)
	if cfgOut.Err != nil {
		t.Fatal(cfgOut.Err)
	}
	if cfg, _ := cfgOut.Value.(InfluxConfig); cfg.Org != "my-org" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestInfluxAbilityPing(t *testing.T) {
	i := NewInfluxAbility()
	atom := newInfluxAtom(t)
	// 无 transport
	if out := i.Command(atom, InfluxCommandPing, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("no transport ping error = %v", out.Err)
	}
	// 有了 transport 但 endpoint 空
	ft := &fakeInfluxTransport{}
	i.SetTransport(ft)
	if out := i.Command(atom, InfluxCommandPing, nil); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("empty endpoint error = %v", out.Err)
	}
	if out := i.Command(atom, InfluxCommandPing, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("ping with args error = %v", out.Err)
	}
	i.Command(atom, InfluxCommandSetEndpoint, InfluxConfigArgs{Value: "https://influx.example.com"})
	if out := i.Command(atom, InfluxCommandPing, nil); out.Err != nil {
		t.Fatal(out.Err)
	}
	ft.pingErr = errors.New("net down")
	if out := i.Command(atom, InfluxCommandPing, nil); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("ping error = %v", out.Err)
	}
}

func TestInfluxAbilityWrite(t *testing.T) {
	i := NewInfluxAbility()
	atom := newInfluxAtom(t)
	ft := &fakeInfluxTransport{}
	i.SetTransport(ft)
	// 类型错误
	if out := i.Command(atom, InfluxCommandWrite, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("write wrong type error = %v", out.Err)
	}
	// 空
	if out := i.Command(atom, InfluxCommandWrite, InfluxWriteArgs{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("empty points error = %v", out.Err)
	}
	// 缺 bucket
	if out := i.Command(atom, InfluxCommandWrite, InfluxWriteArgs{Points: []InfluxPoint{{Measurement: "m", Fields: map[string]any{"v": 1.0}}}}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("missing bucket error = %v", out.Err)
	}
	i.Command(atom, InfluxCommandSetEndpoint, InfluxConfigArgs{Value: "https://influx.example.com"})
	i.Command(atom, InfluxCommandSetBucket, InfluxConfigArgs{Value: "bkt"})
	// 无 transport
	i2 := NewInfluxAbility()
	if out := i2.Command(atom, InfluxCommandWrite, InfluxWriteArgs{Points: []InfluxPoint{{Measurement: "m", Fields: map[string]any{"v": 1.0}}}}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("no transport write error = %v", out.Err)
	}
	// 非法 measurement
	if out := i.Command(atom, InfluxCommandWrite, InfluxWriteArgs{Points: []InfluxPoint{{Measurement: "has space", Fields: map[string]any{"v": 1.0}}}}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("bad measurement error = %v", out.Err)
	}
	// 缺 fields
	if out := i.Command(atom, InfluxCommandWrite, InfluxWriteArgs{Points: []InfluxPoint{{Measurement: "m"}}}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("no fields error = %v", out.Err)
	}
	// 正常
	now := time.Now()
	if out := i.Command(atom, InfluxCommandWrite, InfluxWriteArgs{Points: []InfluxPoint{
		{Measurement: "cpu", Tags: map[string]string{"host": "h1"}, Fields: map[string]any{"usage": 0.5}, Time: now},
	}}); out.Err != nil {
		t.Fatal(out.Err)
	}
	// list series
	if out := i.Command(atom, InfluxCommandListSeries, struct{}{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("list with args error = %v", out.Err)
	}
	if out := i.Command(atom, InfluxCommandListSeries, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if list, _ := out.Value.([]string); len(list) != 1 || list[0] != "cpu" {
		t.Fatalf("series = %v", list)
	}
	// write error
	ft.writeErr = errors.New("disk full")
	if out := i.Command(atom, InfluxCommandWrite, InfluxWriteArgs{Points: []InfluxPoint{{Measurement: "m", Fields: map[string]any{"v": 1.0}}}}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("write error = %v", out.Err)
	}
}

func TestInfluxAbilityQuery(t *testing.T) {
	i := NewInfluxAbility()
	atom := newInfluxAtom(t)
	ft := &fakeInfluxTransport{rows: []map[string]any{{"_value": 1.0}}}
	i.SetTransport(ft)
	i.Command(atom, InfluxCommandSetEndpoint, InfluxConfigArgs{Value: "https://influx.example.com"})
	if out := i.Command(atom, InfluxCommandQuery, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("query wrong type error = %v", out.Err)
	}
	if out := i.Command(atom, InfluxCommandQuery, InfluxQueryArgs{Query: ""}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("empty query error = %v", out.Err)
	}
	if out := i.Command(atom, InfluxCommandQuery, InfluxQueryArgs{Query: "from(bucket:\"b\")"}); out.Err != nil {
		t.Fatal(out.Err)
	} else if rows, _ := out.Value.([]map[string]any); len(rows) != 1 {
		t.Fatalf("rows = %v", rows)
	}
	ft.queryErr = errors.New("syntax")
	if out := i.Command(atom, InfluxCommandQuery, InfluxQueryArgs{Query: "bad"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("query err = %v", out.Err)
	}
	// 无 transport
	i2 := NewInfluxAbility()
	if out := i2.Command(atom, InfluxCommandQuery, InfluxQueryArgs{Query: "x"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("no transport query error = %v", out.Err)
	}
}

func TestInfluxAbilityDeleteSeries(t *testing.T) {
	i := NewInfluxAbility()
	atom := newInfluxAtom(t)
	ft := &fakeInfluxTransport{}
	i.SetTransport(ft)
	i.Command(atom, InfluxCommandSetEndpoint, InfluxConfigArgs{Value: "https://influx.example.com"})
	i.Command(atom, InfluxCommandSetBucket, InfluxConfigArgs{Value: "bkt"})
	i.Command(atom, InfluxCommandWrite, InfluxWriteArgs{Points: []InfluxPoint{
		{Measurement: "cpu", Fields: map[string]any{"v": 1.0}},
		{Measurement: "mem", Fields: map[string]any{"v": 2.0}},
	}})
	if out := i.Command(atom, InfluxCommandDeleteSeries, "raw-string"); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("delete wrong type error = %v", out.Err)
	}
	if out := i.Command(atom, InfluxCommandDeleteSeries, InfluxSeriesArgs{}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("delete empty error = %v", out.Err)
	}
	if out := i.Command(atom, InfluxCommandDeleteSeries, InfluxSeriesArgs{Measurement: "has space"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("delete bad name error = %v", out.Err)
	}
	if out := i.Command(atom, InfluxCommandDeleteSeries, InfluxSeriesArgs{Measurement: "ghost"}); !errors.Is(out.Err, types.ErrInvalidArguments) {
		t.Fatalf("delete missing error = %v", out.Err)
	}
	if out := i.Command(atom, InfluxCommandDeleteSeries, InfluxSeriesArgs{Measurement: "cpu"}); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := i.Command(atom, InfluxCommandListSeries, nil); out.Err != nil {
		t.Fatal(out.Err)
	} else if list, _ := out.Value.([]string); len(list) != 1 || list[0] != "mem" {
		t.Fatalf("series after delete = %v", list)
	}
}

func TestInfluxAbilityUnknownCommand(t *testing.T) {
	i := NewInfluxAbility()
	atom := newInfluxAtom(t)
	if out := i.Command(atom, "nope", nil); !errors.Is(out.Err, types.ErrUnsupportedCommand) {
		t.Fatalf("unknown error = %v", out.Err)
	}
}
