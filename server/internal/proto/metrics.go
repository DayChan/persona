package proto

import "time"

// MetricSample is one reading of a machine, every MetricEvery (sysmon; docs/server/machines.md "What is measured"). A nil field
// was not measured (the first sample has no CPU or network rate: those need two readings).
type MetricSample struct {
	T         int64     `json:"t"`               // Unix seconds
	CPU       *float64  `json:"cpu,omitempty"`   // busy %, all cores (0–100)
	Cores     []float64 `json:"cores,omitempty"` // busy % per core (Linux, Windows; the latest sample only, not kept)
	Load      *float64  `json:"load,omitempty"`  // 1-minute load average
	MemUsed   int64     `json:"mem_used"`        // bytes
	MemTotal  int64     `json:"mem_total"`
	SwapUsed  int64     `json:"swap_used"`
	SwapTotal int64     `json:"swap_total"`
	NetRx     *float64  `json:"net_rx,omitempty"` // bytes/s received on physical interfaces
	NetTx     *float64  `json:"net_tx,omitempty"` // bytes/s sent
	DiskUsed  int64     `json:"disk_used"`        // the system disk (/, or the macOS data volume), like df
	DiskTotal int64     `json:"disk_total"`       // used + available
}

// MetricPoint is one step of a machine's history (GET /api/v1/machines/{id}/metrics): the average over the step,
// or a break (Gap) where the machine sent nothing, which charts must not bridge. Memory, swap and disk are bytes used.
type MetricPoint struct {
	T    int64    `json:"t"` // Unix seconds, the step's start
	Gap  bool     `json:"gap,omitempty"`
	CPU  *float64 `json:"cpu,omitempty"`
	Load *float64 `json:"load,omitempty"`
	Mem  *float64 `json:"mem,omitempty"`
	Swap *float64 `json:"swap,omitempty"`
	Rx   *float64 `json:"rx,omitempty"`
	Tx   *float64 `json:"tx,omitempty"`
	Disk *float64 `json:"disk,omitempty"`
}

// MetricEvery is how often a node samples (and the finest step of the history).
const MetricEvery = 10 * time.Second
