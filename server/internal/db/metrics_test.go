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
// and the charts read the same as from one InsertMetric per sample. A sample Postgres rejects costs only itself.
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
		if rejected, err := d.InsertMetrics(ctx, samples); err != nil || rejected != nil {
			t.Fatal(rejected, err)
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

	// One machine's clock far off (out of timestamp range) between two good samples: the others are stored.
	late := base.Add(time.Hour)
	mixed := []MachineSample{{Machine: "m0", Sample: proto.MetricSample{T: late.Unix(), MemTotal: 1}},
		{Machine: "m1", Sample: proto.MetricSample{T: 1 << 50, MemTotal: 1}},
		{Machine: "m2", Sample: proto.MetricSample{T: late.Unix(), MemTotal: 1}}}
	rejected, err := d.InsertMetrics(ctx, mixed)
	if err != nil || len(rejected) != 3 || rejected[0] != nil || rejected[1] == nil || rejected[2] != nil {
		t.Fatalf("rejected %v, %v", rejected, err)
	}
	if s, _ := d.MetricSeries(ctx, nil, late, late.Add(time.Minute), RawMetricStep); len(s) != 2 || len(s["m0"]) != 1 || len(s["m2"]) != 1 {
		t.Fatalf("stored after a rejected sample: %+v", s)
	}
}
