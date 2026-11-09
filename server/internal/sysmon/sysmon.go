// Package sysmon reads what a machine is (OS, hardware) and how busy it is (CPU, memory, network, disk), for the
// machine pages and their 24-hour history (docs/server/machines.md). Linux reads /proc and /sys; macOS reads sysctl and two
// long-lived processes (iostat, vm_stat); Windows calls Win32 (windows.go): a sample starts no process. The parsers are
// plain functions over the files' text or the bytes the system returns (winparse.go), tested with fixtures on any OS.
package sysmon

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/proto"
)

// Collector samples the machine; rates (CPU, network) are measured between two calls.
type Collector interface {
	Sample(now time.Time) proto.MetricSample
	Close()
}

// cpuTimes are a CPU's cumulative jiffies (Linux) or tick counts: busy and all.
type cpuTimes struct{ busy, total uint64 }

// cpuPercent is the busy share between two readings; nil when there is nothing to compare (the first reading,
// a counter that went backwards). Busy grows by more than total when idle + iowait went backwards (Linux iowait
// may decrease, proc(5)): that is also a counter going backwards, so one sample is lost rather than a CPU above
// 100% drawn.
func cpuPercent(prev, cur cpuTimes) *float64 {
	if prev.total == 0 || cur.total <= prev.total || cur.busy < prev.busy || cur.busy-prev.busy > cur.total-prev.total {
		return nil
	}
	v := round1(100 * float64(cur.busy-prev.busy) / float64(cur.total-prev.total))
	return &v
}

// netCounters are the bytes physical interfaces received and sent since boot.
type netCounters struct {
	rx, tx uint64
	at     time.Time
}

// netRates are bytes per second between two readings; nil for the first, or when a counter went backwards (an
// interface went away or was reset: one sample is lost rather than a huge spike drawn).
func netRates(prev, cur netCounters) (rx, tx *float64) {
	dt := cur.at.Sub(prev.at).Seconds()
	if prev.at.IsZero() || dt < 0.5 || cur.rx < prev.rx || cur.tx < prev.tx {
		return nil, nil
	}
	r, t := round1(float64(cur.rx-prev.rx)/dt), round1(float64(cur.tx-prev.tx)/dt)
	return &r, &t
}

// listEvery: how often a collector checks again which network interfaces (and on Windows, drives) count; one plugged
// in or renamed shows within a minute.
const listEvery = time.Minute

func round1(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }

func ptr(v float64) *float64 { return &v }

// virtualName: an interface whose traffic is not the machine's own on the wire — loopback, containers, bridges,
// VPN / proxy tunnels (Tailscale, WireGuard, TUN proxies), VM taps and Apple's peer-to-peer links. Their bytes are
// either local or also counted on the physical interface underneath. Used where the OS cannot say (see physical in
// linux.go, darwin.go).
func virtualName(name string) bool {
	for _, p := range []string{"lo", "docker", "br-", "veth", "virbr", "vnet", "tailscale", "tun", "tap", "utun", "wg",
		"zt", "cni", "flannel", "cali", "vxlan", "kube", "dummy", "ifb", "bridge", "awdl", "llw", "anpi", "gif", "stf",
		"ap", "ipsec", "ppp", "vmenet", "podman", "lxc", "lxd", "incus", "nordlynx", "mullvad"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// parseOSRelease reads /etc/os-release (KEY=value, values maybe quoted).
func parseOSRelease(b []byte) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}
		if uq, err := strconv.Unquote(v); err == nil {
			v = uq
		} else {
			v = strings.Trim(v, `"'`)
		}
		out[k] = v
	}
	return out
}

// linuxSystem fills the OS part of System from os-release.
func linuxSystem(osr map[string]string) proto.System {
	s := proto.System{OS: "linux", Distro: osr["ID"], Like: osr["ID_LIKE"], Name: osr["PRETTY_NAME"], Version: osr["VERSION_ID"]}
	if s.Distro == "" {
		s.Distro = "linux"
	}
	if s.Name == "" {
		s.Name = strings.TrimSpace(osr["NAME"] + " " + osr["VERSION_ID"])
	}
	if s.Name == "" {
		s.Name = "Linux"
	}
	return s
}

// parseProcStat reads /proc/stat: all CPUs, each core, and the boot time. Other lines are skipped before they are
// split: intr and softirq hold a counter per interrupt, thousands on a big host.
func parseProcStat(b []byte) (all cpuTimes, cores []cpuTimes, boot int64) {
	cores = make([]cpuTimes, 0, bytes.Count(b, []byte("\ncpu")))
	for len(b) > 0 {
		var line []byte
		line, b, _ = bytes.Cut(b, []byte("\n"))
		line = bytes.TrimLeftFunc(line, unicode.IsSpace)
		if !bytes.HasPrefix(line, []byte("cpu")) && !bytes.HasPrefix(line, []byte("btime")) {
			continue
		}
		f := strings.Fields(string(line))
		if len(f) < 2 {
			continue
		}
		switch {
		case f[0] == "cpu":
			all = jiffies(f[1:])
		case strings.HasPrefix(f[0], "cpu"):
			cores = append(cores, jiffies(f[1:]))
		case f[0] == "btime":
			boot, _ = strconv.ParseInt(f[1], 10, 64)
		}
	}
	return
}

// jiffies: user nice system idle iowait irq softirq steal [guest guest_nice]; guest time is already in user,
// so it is not added again. Waiting for I/O counts as idle (like top's "id + wa").
func jiffies(f []string) cpuTimes {
	var v [8]uint64
	for i := 0; i < len(v) && i < len(f); i++ {
		v[i], _ = strconv.ParseUint(f[i], 10, 64)
	}
	var t cpuTimes
	for _, x := range v {
		t.total += x
	}
	t.busy = t.total - v[3] - v[4]
	return t
}

// parseMeminfo reads /proc/meminfo into bytes.
func parseMeminfo(b []byte) map[string]int64 {
	out := map[string]int64{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		f := strings.Fields(v)
		if len(f) == 0 {
			continue
		}
		n, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			continue
		}
		if len(f) > 1 && f[1] == "kB" {
			n *= 1024
		}
		out[k] = n
	}
	return out
}

// linuxMemory: used is what `free` would need to give back to have it all available (MemTotal − MemAvailable), so
// the page cache counts as free; kernels before 3.14 have no MemAvailable: free + buffers + cache instead.
func linuxMemory(mi map[string]int64) (used, total, swapUsed, swapTotal int64) {
	total = mi["MemTotal"]
	avail, ok := mi["MemAvailable"]
	if !ok {
		avail = mi["MemFree"] + mi["Buffers"] + mi["Cached"]
	}
	used = max(total-avail, 0)
	swapTotal = mi["SwapTotal"]
	swapUsed = max(swapTotal-mi["SwapFree"], 0)
	return
}

// parseNetDev reads /proc/net/dev: each interface's received and sent bytes.
func parseNetDev(b []byte) map[string][2]uint64 {
	out := map[string][2]uint64{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		rx, err1 := strconv.ParseUint(f[0], 10, 64)
		tx, err2 := strconv.ParseUint(f[8], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		out[strings.TrimSpace(name)] = [2]uint64{rx, tx}
	}
	return out
}

// parseLoadavg reads the 1-minute load from /proc/loadavg ("0.52 0.58 0.59 1/389 12345").
func parseLoadavg(b []byte) *float64 {
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return nil
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return nil
	}
	return &v
}

// cpuModel is the first "model name" in /proc/cpuinfo (x86), else what ARM boards name ("Model", "Hardware").
func cpuModel(b []byte) string {
	var fallback string
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.Join(strings.Fields(v), " ")
		switch k {
		case "model name":
			return v
		case "Model", "Hardware":
			if fallback == "" {
				fallback = v
			}
		}
	}
	return fallback
}

// dmiModel cleans a DMI product name: placeholders firmware leaves in are dropped.
func dmiModel(vendor, product string) string {
	clean := func(s string) string {
		s = strings.TrimSpace(s)
		switch strings.ToLower(s) {
		case "", "to be filled by o.e.m.", "system product name", "default string", "not specified", "none", "o.e.m.", "system manufacturer":
			return ""
		}
		return s
	}
	vendor, product = clean(vendor), clean(product)
	if product == "" {
		return ""
	}
	if vendor != "" && !strings.Contains(strings.ToLower(product), strings.ToLower(strings.Fields(vendor)[0])) {
		return vendor + " " + product
	}
	return product
}

// parseSwVers reads `sw_vers` (ProductName / ProductVersion / BuildVersion).
func parseSwVers(b []byte) (name, version, build string) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "ProductName":
			name = v
		case "ProductVersion":
			version = v
		case "BuildVersion":
			build = v
		}
	}
	return
}

// profilerModel is the "Model Name" line of `system_profiler SPHardwareDataType` (nothing else of it is read: the
// same output has the serial number).
func profilerModel(b []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), ":"); ok && strings.TrimSpace(k) == "Model Name" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// vmStat is one row of `vm_stat <interval>` in pages (the gauges; the counters after them are not used).
type vmStat struct {
	free, active, speculative, inactive, wired, purgeable, fileBacked, anonymous, compressor uint64
}

// parseVMStatHeader reads the page size from vm_stat's first line ("… (page size of 16384 bytes)").
func parseVMStatHeader(line string) (uint64, bool) {
	_, rest, ok := strings.Cut(line, "page size of ")
	if !ok {
		return 0, false
	}
	f := strings.Fields(rest)
	if len(f) == 0 {
		return 0, false
	}
	n, err := strconv.ParseUint(f[0], 10, 64)
	return n, err == nil && n > 0
}

// parseVMStatRow reads a data row: free active specul inactive throttle wired prgable faults copy 0fill reactive
// purged file-backed anonymous cmprssed cmprssor … (column headers and other lines are not rows).
func parseVMStatRow(line string) (vmStat, bool) {
	f := strings.Fields(line)
	if len(f) < 16 {
		return vmStat{}, false
	}
	var v [16]uint64
	for i := range v {
		n, ok := abbreviated(f[i])
		if !ok {
			return vmStat{}, false
		}
		v[i] = n
	}
	return vmStat{free: v[0], active: v[1], speculative: v[2], inactive: v[3], wired: v[5], purgeable: v[6],
		fileBacked: v[12], anonymous: v[13], compressor: v[15]}, true
}

// abbreviated parses vm_stat's numbers, which it shortens to fit the column ("12456M", "278381K").
func abbreviated(s string) (uint64, bool) {
	mult := uint64(1)
	switch {
	case strings.HasSuffix(s, "K"):
		mult, s = 1<<10, s[:len(s)-1]
	case strings.HasSuffix(s, "M"):
		mult, s = 1<<20, s[:len(s)-1]
	case strings.HasSuffix(s, "G"):
		mult, s = 1<<30, s[:len(s)-1]
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return n * mult, err == nil
}

// darwinMemoryUsed is Activity Monitor's  "Memory Used": app memory (anonymous pages that are not purgeable) + wired +
// what the compressor occupies. File cache and purgeable memory count as free, as on Linux.
func darwinMemoryUsed(v vmStat, page uint64) int64 {
	app := v.anonymous - min(v.purgeable, v.anonymous)
	return int64((app + v.wired + v.compressor) * page)
}

// parseIostatRow reads a row of `iostat -n 0 -w <interval>`: "us sy id 1m 5m 15m" (headers are not rows).
func parseIostatRow(line string) (busy float64, load float64, ok bool) {
	f := strings.Fields(line)
	if len(f) < 4 {
		return 0, 0, false
	}
	var v [4]float64
	for i := range v {
		n, err := strconv.ParseFloat(f[i], 64)
		if err != nil {
			return 0, 0, false
		}
		v[i] = n
	}
	return min(max(100-v[2], 0), 100), v[3], true
}

// diskUsage is like df: used = blocks − free, total = used + what an unprivileged user can still write.
func diskUsage(blocks, bfree, bavail, bsize uint64) (used, total int64) {
	used = int64((blocks - min(bfree, blocks)) * bsize)
	return used, used + int64(bavail*bsize)
}

// diskMounts picks, from /proc/self/mounts, one mount point per local block device: a box with its data disk on
// /data00 has most of its space there, so counting only / would show a 118 GB disk on a 5 TB machine. Loop devices
// (snaps, images) and read-only image filesystems are not storage anyone fills; bind mounts repeat a device.
// Used when /proc/self/mountinfo cannot be read; diskMountinfo also drops bind mounts of a directory.
func diskMounts(b []byte) []string {
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || !diskDevice(f[0], f[2]) || seen[f[0]] {
			continue
		}
		seen[f[0]] = true
		out = append(out, unescapeMount(f[1]))
	}
	return out
}

// diskMountinfo is diskMounts over /proc/self/mountinfo, which also says which directory of the filesystem a mount
// shows (its root). In a container, Kubernetes and Docker bind single directories of the host's disks in: an
// emptyDir on /tmp, /etc/hosts, /etc/hostname, log directories. That disk is shared by every pod on the host, so
// counting it showed a 2 TB disk at 28% on a box whose own / was 83% full. A mount whose root is not / is such a
// directory and is skipped, except / itself and btrfs subvolumes (Ubuntu mounts / and /home as /@ and /@home; each
// reports the whole filesystem).
func diskMountinfo(b []byte) []string {
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		pre, post, ok := strings.Cut(line, " - ")
		f, g := strings.Fields(pre), strings.Fields(post)
		if !ok || len(f) < 5 || len(g) < 2 {
			continue
		}
		dev, root, point, fstype, source := f[2], unescapeMount(f[3]), unescapeMount(f[4]), g[0], g[1]
		if !diskDevice(source, fstype) || seen[dev] || (root != "/" && point != "/" && fstype != "btrfs") {
			continue
		}
		seen[dev] = true
		out = append(out, point)
	}
	return out
}

// diskDevice: a local block device someone stores files on (not a loop device or a read-only image filesystem).
func diskDevice(source, fstype string) bool {
	if !strings.HasPrefix(source, "/dev/") || strings.HasPrefix(source, "/dev/loop") {
		return false
	}
	switch fstype {
	case "squashfs", "iso9660", "erofs", "udf":
		return false
	}
	return true
}

// cgroupMemory reads the memory limit and use of the cgroup mounted at root (/sys/fs/cgroup), v2 or v1. In a
// container that is the container's own cgroup, and /proc/meminfo is the host's (unless lxcfs is mounted over it):
// a pod limited to 32 GiB on a 512 GiB host showed 31% used while it was close to being OOM-killed. On a machine
// that is not in a container it is the root cgroup, which has no limit (v2 has no memory.max there, v1 reports
// about 2^63), so ok is false. Used is like docker stats and kubectl top: usage minus the inactive page cache,
// which the kernel reclaims before it kills anything.
func cgroupMemory(root string) (limit, used int64, ok bool) {
	read := func(name string) []byte { b, _ := os.ReadFile(filepath.Join(root, name)); return b }
	if b := read("memory.max"); b != nil {
		if limit, ok = cgroupLimit(b); ok {
			used = cgroupUsed(read("memory.current"), read("memory.stat"), "inactive_file")
		}
		return
	}
	if b := read("memory/memory.limit_in_bytes"); b != nil {
		if limit, ok = cgroupLimit(b); ok {
			used = cgroupUsed(read("memory/memory.usage_in_bytes"), read("memory/memory.stat"), "total_inactive_file", "inactive_file")
		}
	}
	return
}

// noCgroupLimit: v1 writes "no limit" as the largest page-aligned int64; anything at 2^60 or above is no limit.
const noCgroupLimit = 1 << 60

func cgroupLimit(b []byte) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64) // v2 writes "max"
	if err != nil || n <= 0 || n >= noCgroupLimit {
		return 0, false
	}
	return n, true
}

// cgroupUsed is usage minus the first of keys found in memory.stat.
func cgroupUsed(usage, stat []byte, keys ...string) int64 {
	u, _ := strconv.ParseInt(strings.TrimSpace(string(usage)), 10, 64)
	vals := map[string]int64{}
	for _, line := range strings.Split(string(stat), "\n") {
		if k, v, ok := strings.Cut(line, " "); ok {
			if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				vals[k] = n
			}
		}
	}
	for _, k := range keys {
		if n, ok := vals[k]; ok {
			return max(u-n, 0)
		}
	}
	return max(u, 0)
}

// unescapeMount undoes the kernel's octal escapes in mount points (\040 for a space).
func unescapeMount(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
