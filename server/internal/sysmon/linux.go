//go:build linux

package sysmon

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// ReadSystem reads /etc/os-release, the kernel release, DMI and /proc/cpuinfo.
func ReadSystem(ctx context.Context) proto.System {
	osr, err := os.ReadFile("/etc/os-release")
	if err != nil {
		osr, _ = os.ReadFile("/usr/lib/os-release")
	}
	s := linuxSystem(parseOSRelease(osr))
	s.Arch, s.Cores = runtime.GOARCH, runtime.NumCPU()
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		s.Kernel = strings.TrimSpace(string(b))
	}
	vendor, _ := os.ReadFile("/sys/devices/virtual/dmi/id/sys_vendor")
	product, _ := os.ReadFile("/sys/devices/virtual/dmi/id/product_name")
	s.Model = dmiModel(string(vendor), string(product))
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		s.CPU = cpuModel(b)
	}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		s.Memory = parseMeminfo(b)["MemTotal"]
	}
	if limit, _, ok := cgroupMemory(cgroupRoot); ok && limit < s.Memory {
		s.Memory = limit
	}
	if b, err := os.ReadFile("/proc/stat"); err == nil {
		_, _, s.BootTime = parseProcStat(b)
	}
	return s
}

// cgroupRoot is where the cgroup hierarchy is mounted (cgroupMemory).
const cgroupRoot = "/sys/fs/cgroup"

type linuxCollector struct {
	cpu   cpuTimes
	cores []cpuTimes
	net   netCounters
	buf   bytes.Buffer // what read read last, reused between samples
	// Whether each interface is physical, forgotten every listEvery: resolving its /sys link takes a few syscalls,
	// and a host running containers has a veth per container.
	nics   map[string]bool
	nicsAt time.Time
	mounts mountCache
}

// NewCollector starts sampling (nothing runs between samples on Linux).
func NewCollector(ctx context.Context) Collector { return &linuxCollector{} }

func (c *linuxCollector) Close() {}

func (c *linuxCollector) Sample(now time.Time) proto.MetricSample {
	s := proto.MetricSample{T: now.Unix()}
	if b, err := c.read("/proc/stat"); err == nil {
		all, cores, _ := parseProcStat(b)
		s.CPU = cpuPercent(c.cpu, all)
		if len(cores) == len(c.cores) {
			for i := range cores {
				if v := cpuPercent(c.cores[i], cores[i]); v != nil {
					s.Cores = append(s.Cores, *v)
				}
			}
			if len(s.Cores) != len(cores) {
				s.Cores = nil
			}
		}
		c.cpu, c.cores = all, cores
	}
	if b, err := c.read("/proc/loadavg"); err == nil {
		s.Load = parseLoadavg(b)
	}
	if b, err := c.read("/proc/meminfo"); err == nil {
		s.MemUsed, s.MemTotal, s.SwapUsed, s.SwapTotal = linuxMemory(parseMeminfo(b))
	}
	// In a container the limit is what it can use, not the host's memory (cgroupMemory).
	if limit, used, ok := cgroupMemory(cgroupRoot); ok && limit < s.MemTotal {
		s.MemTotal, s.MemUsed = limit, min(used, limit)
	}
	if b, err := c.read("/proc/net/dev"); err == nil {
		cur := netCounters{at: now}
		for name, v := range parseNetDev(b) {
			if c.isPhysical(name, now) {
				cur.rx += v[0]
				cur.tx += v[1]
			}
		}
		s.NetRx, s.NetTx = netRates(c.net, cur)
		c.net = cur
	}
	mounts := []string{"/"}
	if b, err := c.read("/proc/self/mountinfo"); err == nil {
		if m := c.mounts.of(b); len(m) > 0 {
			mounts = m
		}
	} else if b, err := c.read("/proc/self/mounts"); err == nil {
		if m := diskMounts(b); len(m) > 0 {
			mounts = m
		}
	}
	for _, m := range mounts {
		var st syscall.Statfs_t
		if syscall.Statfs(m, &st) == nil {
			used, total := diskUsage(st.Blocks, st.Bfree, st.Bavail, uint64(st.Bsize))
			s.DiskUsed += used
			s.DiskTotal += total
		}
	}
	return s
}

// read reads a /proc file into c.buf: the bytes are good until the next read.
func (c *linuxCollector) read(name string) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	c.buf.Reset()
	if _, err := c.buf.ReadFrom(f); err != nil {
		return nil, err
	}
	return c.buf.Bytes(), nil
}

// isPhysical is physical(name) remembered for up to listEvery, so an interface renamed or moved shows within a minute,
// as on Windows; a name not seen before is looked up at once.
func (c *linuxCollector) isPhysical(name string, now time.Time) bool {
	if c.nics == nil || now.Sub(c.nicsAt) >= listEvery || now.Before(c.nicsAt) {
		c.nics, c.nicsAt = map[string]bool{}, now
	}
	p, ok := c.nics[name]
	if !ok {
		p = physical(name)
		c.nics[name] = p
	}
	return p
}

// mountCache keeps the last mountinfo and what diskMountinfo picked from it: the file is the same on almost every
// sample, and has thousands of lines on a host running containers.
type mountCache struct {
	raw    []byte
	points []string
	parsed bool
}

// of is diskMountinfo(b), parsed again only when b differs from the last call's. The result must not be changed.
func (m *mountCache) of(b []byte) []string {
	if !m.parsed || !bytes.Equal(b, m.raw) {
		m.raw, m.points, m.parsed = append(m.raw[:0], b...), diskMountinfo(b), true
	}
	return m.points
}

// physical: the kernel puts every software interface (lo, docker0, veth*, br-*, tun*, tailscale0, wg0, bonds and
// VLANs on top of real ports) under /sys/devices/virtual/net; real NICs, virtio ones in a VM included, sit under
// their bus. Without /sys the name decides.
func physical(name string) bool {
	if target, err := filepath.EvalSymlinks(filepath.Join("/sys/class/net", name)); err == nil {
		return !strings.Contains(target, "/devices/virtual/")
	}
	return !virtualName(name)
}

func loadAverageOS() *float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return nil
	}
	return parseLoadavg(b)
}
