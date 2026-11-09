package db

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A tick's samples of every machine go in one transaction (one xmin for all its rows: one commit and one WAL flush),
// and the charts read the same as from one InsertMetric per sample. A rejected sample fails the whole batch and
// stores nothing of it, which is what lets the caller write them one at a time instead (devices/metrics.go).
func TestInsertMetricsOneTransaction(t *testing.T) {
	d, ctx := open(t), context.Background()
	base := time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)
	f := func(v float64) *float64 { return &v }
	var ticks [][]MachineSample
	for tick := range 12 {
		var samples []MachineSample
		for m := range 6 {
			s := proto.MetricSample{T: base.Add(time.Duration(tick) * proto.MetricEvery).Unix(), CPU: f(float64(10*m + tick)),
				Load: f(0.5), MemUsed: int64(100 * (m + 1)), MemTotal: 1000, NetRx: f(float64(tick)), DiskUsed: 5, DiskTotal: 50}
			if m == 2 && tick == 0 {
				s.CPU, s.NetRx = nil, nil // a node's first sample
			}
			samples = append(samples, MachineSample{Machine: fmt.Sprintf("m%d", m), Sample: s})
		}
		ticks = append(ticks, samples)
	}
	series := func() map[string][]proto.MetricPoint {
		t.Helper()
		out, err := d.MetricSeries(ctx, nil, base, base.Add(time.Hour), RawMetricStep)
		if err != nil || len(out) != 6 || len(out["m0"]) != 12 {
			t.Fatalf("series = %d machines, %v", len(out), err)
		}
		return out
	}
	for _, samples := range ticks {
		for _, s := range samples {
			if err := d.InsertMetric(ctx, s.Machine, s.Sample); err != nil {
				t.Fatal(err)
			}
		}
	}
	each := series()
	if _, err := d.pool.Exec(ctx, `TRUNCATE machine_metrics`); err != nil {
		t.Fatal(err)
	}
	for _, samples := range ticks {
		if err := d.InsertMetrics(ctx, samples); err != nil {
			t.Fatal(err)
		}
	}
	if batched := series(); !reflect.DeepEqual(batched, each) {
		t.Fatalf("batched series differ:\n%+v\n%+v", batched, each)
	}
	var txs, perTick int
	if err := d.pool.QueryRow(ctx, `SELECT count(DISTINCT xmin::text), max(n) FROM machine_metrics,
			LATERAL (SELECT count(DISTINCT xmin::text) AS n FROM machine_metrics m WHERE m.ts = machine_metrics.ts) per`).Scan(&txs, &perTick); err != nil {
		t.Fatal(err)
	}
	if txs != len(ticks) || perTick != 1 {
		t.Fatalf("%d transactions for %d ticks, up to %d per tick", txs, len(ticks), perTick)
	}

	late := base.Add(time.Hour)
	bad := []MachineSample{{Machine: "m0", Sample: proto.MetricSample{T: late.Unix(), MemTotal: 1}},
		{Machine: "m1", Sample: proto.MetricSample{T: 1 << 50, MemTotal: 1}}} // a clock far off: out of timestamp range
	if err := d.InsertMetrics(ctx, bad); err == nil {
		t.Fatal("a sample out of range was accepted")
	}
	if s, _ := d.MetricSeries(ctx, []string{"m0"}, late, late.Add(time.Minute), RawMetricStep); len(s) != 0 {
		t.Fatalf("a failed batch stored %+v", s)
	}
}
