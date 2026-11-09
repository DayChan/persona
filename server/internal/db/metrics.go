package db

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nwverse/persona/server/internal/proto"
)

// ---- machine monitoring (docs/server/machines.md) ----

func (d *DB) PutMachineSystem(ctx context.Context, machine string, s proto.System) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = d.pool.Exec(ctx, `INSERT INTO machine_systems (machine_id, system, updated_at) VALUES ($1, $2, now())
		ON CONFLICT (machine_id) DO UPDATE SET system = $2, updated_at = now()`, machine, b)
	return err
}

func (d *DB) MachineSystems(ctx context.Context) (map[string]proto.System, error) {
	rows, err := d.pool.Query(ctx, `SELECT machine_id, system FROM machine_systems`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]proto.System{}
	for rows.Next() {
		var id string
		var b []byte
		if err := rows.Scan(&id, &b); err != nil {
			return nil, err
		}
		var s proto.System
		if json.Unmarshal(b, &s) == nil {
			out[id] = s
		}
	}
	return out, rows.Err()
}

// RawMetricStep is the step of a row straight from a node; RolledMetricStep of one minute rolled up.
const (
	RawMetricStep    = 10
	RolledMetricStep = 60
)

const insertMetricSQL = `INSERT INTO machine_metrics (machine_id, ts, step, cpu, load, mem_used, mem_total,
		swap_used, swap_total, net_rx, net_tx, disk_used, disk_total)
	VALUES ($1, to_timestamp($2), $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	ON CONFLICT (machine_id, ts, step) DO UPDATE SET cpu = $4, load = $5, mem_used = $6, mem_total = $7,
		swap_used = $8, swap_total = $9, net_rx = $10, net_tx = $11, disk_used = $12, disk_total = $13`

func insertMetricArgs(machine string, s proto.MetricSample) []any {
	return []any{machine, s.T, RawMetricStep, s.CPU, s.Load, s.MemUsed, s.MemTotal, s.SwapUsed, s.SwapTotal, s.NetRx, s.NetTx,
		s.DiskUsed, s.DiskTotal}
}

// InsertMetric stores a node's sample as a raw row (a repeat of the same second replaces it).
func (d *DB) InsertMetric(ctx context.Context, machine string, s proto.MetricSample) error {
	_, err := d.pool.Exec(ctx, insertMetricSQL, insertMetricArgs(machine, s)...)
	return err
}

// MachineSample is one machine's sample, for InsertMetrics.
type MachineSample struct {
	Machine string
	Sample  proto.MetricSample
}

// InsertMetrics stores samples of several machines (the nodes sample on the same tick) as InsertMetric does, in one
// transaction: one commit, so one WAL flush, instead of one a machine. One machine's bad sample does not cost the
// others theirs: when the batch fails, the samples are written again in one transaction, each under its own
// savepoint, and rejected[i] is the error samples[i] got (rejected is nil when all were stored). err is a failure of
// the whole write (the connection, the commit, ctx); then none was stored.
func (d *DB) InsertMetrics(ctx context.Context, samples []MachineSample) (rejected []error, err error) {
	if len(samples) == 0 {
		return nil, nil
	}
	err = pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		batch := &pgx.Batch{}
		for _, s := range samples {
			batch.Queue(insertMetricSQL, insertMetricArgs(s.Machine, s.Sample)...)
		}
		return tx.SendBatch(ctx, batch).Close()
	})
	if err == nil || ctx.Err() != nil {
		return nil, err
	}
	// A batch stops at its first error, and the transaction with it: find which samples Postgres takes.
	rejected = make([]error, len(samples))
	err = pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		for i, s := range samples {
			rejected[i] = pgx.BeginFunc(ctx, tx, func(sp pgx.Tx) error { // a savepoint
				_, err := sp.Exec(ctx, insertMetricSQL, insertMetricArgs(s.Machine, s.Sample)...)
				return err
			})
		}
		return nil
	})
	if err != nil || !slices.ContainsFunc(rejected, func(err error) bool { return err != nil }) {
		return nil, err
	}
	return rejected, nil
}

// rollupMetricsSQL moves the raw rows before $1 (Unix seconds) into one row a machine and minute of step $2. The raw
// rows are found through the partial index machine_metrics_raw (migration 0415) instead of a scan of the whole table.
// The raw step is written out (RawMetricStep, 10) rather than passed: a cached statement may get a generic plan, and
// the planner can only match a partial index's WHERE against a constant.
const rollupMetricsSQL = `WITH moved AS (
		DELETE FROM machine_metrics WHERE step = 10 AND ts < to_timestamp($1) RETURNING *
	)
	INSERT INTO machine_metrics (machine_id, ts, step, cpu, load, mem_used, mem_total, swap_used, swap_total,
		net_rx, net_tx, disk_used, disk_total)
	SELECT machine_id, date_bin('1 minute', ts, timestamptz 'epoch'), $2, avg(cpu), avg(load), avg(mem_used)::bigint,
		max(mem_total), avg(swap_used)::bigint, max(swap_total), avg(net_rx), avg(net_tx), avg(disk_used)::bigint,
		max(disk_total)
	FROM moved GROUP BY 1, 2
	ON CONFLICT (machine_id, ts, step) DO NOTHING`

// RollupMetrics turns raw rows older than before (rounded down to a minute) into one row per machine and minute:
// averages, except the totals (largest). Moving and averaging is one statement, so a minute is rolled up once.
func (d *DB) RollupMetrics(ctx context.Context, before time.Time) (int64, error) {
	tag, err := d.pool.Exec(ctx, rollupMetricsSQL, before.Truncate(time.Minute).Unix(), RolledMetricStep)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// PruneMetrics deletes every row older than before.
func (d *DB) PruneMetrics(ctx context.Context, before time.Time) (int64, error) {
	tag, err := d.pool.Exec(ctx, `DELETE FROM machine_metrics WHERE ts < to_timestamp($1)`, before.Unix())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// The statements of MetricSeries: all machines, or the listed ones. The single statement they replace matched nil
// machines with an OR ("$4 IS NULL OR machine_id = ANY($4)"). Once Postgres switched that cached statement to a
// generic plan (after five runs) it could not fold the test away, so it read every machine's rows in the range and
// filtered them. TestMetricSeriesIndexed checks the plans.
const (
	metricSeriesSelect = `SELECT machine_id,
			extract(epoch FROM date_bin(make_interval(secs => $3), ts, timestamptz 'epoch'))::bigint,
			sum(cpu::float8 * step) / nullif(sum(step) FILTER (WHERE cpu IS NOT NULL), 0),
			sum(load::float8 * step) / nullif(sum(step) FILTER (WHERE load IS NOT NULL), 0),
			sum(mem_used::float8 * step) / sum(step),
			sum(swap_used::float8 * step) / sum(step),
			sum(net_rx::float8 * step) / nullif(sum(step) FILTER (WHERE net_rx IS NOT NULL), 0),
			sum(net_tx::float8 * step) / nullif(sum(step) FILTER (WHERE net_tx IS NOT NULL), 0),
			sum(disk_used::float8 * step) / sum(step)
		FROM machine_metrics
		WHERE ts >= to_timestamp($1) AND ts < to_timestamp($2)`
	metricSeriesSQL = metricSeriesSelect + `
		GROUP BY 1, 2 ORDER BY 1, 2`
	metricSeriesMachinesSQL = metricSeriesSelect + ` AND machine_id = ANY($4::text[])
		GROUP BY 1, 2 ORDER BY 1, 2`
)

// MetricSeries averages each machine's rows from from (inclusive) to to over steps of step seconds (aligned to the
// Unix epoch), weighting raw and rolled-up rows by the time they cover. Only steps with data are returned, in
// order; machines nil means all. Each case has its own statement, so a cached generic plan keeps the machine filter
// on machine_metrics_pkey.
func (d *DB) MetricSeries(ctx context.Context, machines []string, from, to time.Time, step int) (map[string][]proto.MetricPoint, error) {
	q, args := metricSeriesMachinesSQL, []any{from.Unix(), to.Unix(), step, machines}
	if machines == nil {
		q, args = metricSeriesSQL, args[:3]
	}
	rows, err := d.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]proto.MetricPoint{}
	for rows.Next() {
		var id string
		var p proto.MetricPoint
		if err := rows.Scan(&id, &p.T, &p.CPU, &p.Load, &p.Mem, &p.Swap, &p.Rx, &p.Tx, &p.Disk); err != nil {
			return nil, err
		}
		out[id] = append(out[id], p)
	}
	return out, rows.Err()
}
