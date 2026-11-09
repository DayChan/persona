package sysmon

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestProcStatCPU(t *testing.T) {
	all0, cores0, boot := parseProcStat(fixture(t, "proc_stat"))
	all1, cores1, _ := parseProcStat(fixture(t, "proc_stat_later"))
	if boot != 1790788452 || len(cores0) != 2 || len(cores1) != 2 {
		t.Fatalf("boot %d, cores %d %d", boot, len(cores0), len(cores1))
	}
	// 2000 jiffies passed, 1200 of them busy (iowait counts as idle).
	if v := cpuPercent(all0, all1); v == nil || *v != 60 {
		t.Fatalf("cpu = %v", v)
	}
	if v := cpuPercent(cores0[0], cores1[0]); v == nil || *v != 66.7 {
		t.Fatalf("cpu0 = %v", v)
	}
	if v := cpuPercent(cpuTimes{}, all1); v != nil {
		t.Fatalf("first reading has a rate: %v", *v)
	}
	if v := cpuPercent(all1, all0); v != nil {
		t.Fatalf("counter went back but got %v", *v)
	}
}

// Busy is total - idle - iowait and iowait may decrease (proc(5)), so busy can grow by more than total: that is a
// counter going backwards (no reading), not a CPU above 100%.
func TestCPUPercentCounterBackwards(t *testing.T) {
	_, cores0, _ := parseProcStat([]byte("cpu  200 0 100 600 100 0 0 0\ncpu0 100 0 50 300 50 0 0 0\ncpu1 100 0 50 300 50 0 0 0\n"))
	_, cores1, _ := parseProcStat([]byte("cpu  300 0 110 630 50 0 0 0\ncpu0 120 0 60 320 50 0 0 0\ncpu1 180 0 50 310 0 0 0 0\n"))
	if len(cores0) != 2 || len(cores1) != 2 {
		t.Fatalf("cores %d %d", len(cores0), len(cores1))
	}
	show := func(v *float64) string {
		if v == nil {
			return "nil"
		}
		return fmt.Sprint(*v)
	}
	for _, tc := range []struct {
		name      string
		prev, cur cpuTimes
		want      *float64
	}{
		{"idle and iowait went back", jiffies(strings.Fields("10 0 10 50 30 0 0 0")), jiffies(strings.Fields("40 0 20 60 0 0 0 0")), nil},
		{"one core's iowait went back", cores0[1], cores1[1], nil},
		{"the other core", cores0[0], cores1[0], ptr(60)},
		{"normal delta", cpuTimes{busy: 10, total: 100}, cpuTimes{busy: 35, total: 200}, ptr(25)},
		{"fully busy", cpuTimes{busy: 10, total: 100}, cpuTimes{busy: 110, total: 200}, ptr(100)},
		{"fully idle", cpuTimes{busy: 10, total: 100}, cpuTimes{busy: 10, total: 200}, ptr(0)},
	} {
		if got := cpuPercent(tc.prev, tc.cur); show(got) != show(tc.want) {
			t.Errorf("%s: cpuPercent(%+v, %+v) = %s, want %s", tc.name, tc.prev, tc.cur, show(got), show(tc.want))
		}
	}
}

func TestMeminfo(t *testing.T) {
	used, total, swapUsed, swapTotal := linuxMemory(parseMeminfo(fixture(t, "meminfo")))
	if total != 16303428*1024 || used != (16303428-9861756)*1024 {
		t.Fatalf("mem %d / %d", used, total)
	}
	if swapTotal != 4194300*1024 || swapUsed != (4194300-4093948)*1024 {
		t.Fatalf("swap %d / %d", swapUsed, swapTotal)
	}
	// No MemAvailable (old kernels): free + buffers + cache.
	used, _, _, _ = linuxMemory(map[string]int64{"MemTotal": 100, "MemFree": 10, "Buffers": 5, "Cached": 25})
	if used != 60 {
		t.Fatalf("old kernel used = %d", used)
	}
}

func TestNetDevAndVirtualInterfaces(t *testing.T) {
	dev := parseNetDev(fixture(t, "net_dev"))
	if len(dev) != 5 || dev["eth0"] != [2]uint64{5000000000, 1000000000} || dev["docker0"][1] != 654321 {
		t.Fatalf("net dev = %v", dev)
	}
	var kept []string
	for name := range dev {
		if !virtualName(name) {
			kept = append(kept, name)
		}
	}
	if len(kept) != 1 || kept[0] != "eth0" {
		t.Fatalf("physical by name = %v", kept)
	}
	for _, name := range []string{"en0", "enp3s0", "eno1", "wlan0", "wlp2s0", "ens5"} {
		if virtualName(name) {
			t.Errorf("%s taken for virtual", name)
		}
	}
	for _, name := range []string{"utun3", "wg0", "tun0", "br-1a2b", "virbr0", "awdl0", "llw0", "bridge0", "zt5u4y"} {
		if !virtualName(name) {
			t.Errorf("%s taken for physical", name)
		}
	}
}

func TestNetRates(t *testing.T) {
	t0 := time.Unix(1000, 0)
	prev := netCounters{rx: 1000, tx: 500, at: t0}
	rx, tx := netRates(prev, netCounters{rx: 21000, tx: 1500, at: t0.Add(10 * time.Second)})
	if rx == nil || *rx != 2000 || *tx != 100 {
		t.Fatalf("rates %v %v", rx, tx)
	}
	if rx, _ := netRates(netCounters{}, prev); rx != nil {
		t.Fatal("first reading has a rate")
	}
	// An interface went away: the sum dropped. No negative or wrapped spike.
	if rx, _ := netRates(prev, netCounters{rx: 10, tx: 600, at: t0.Add(10 * time.Second)}); rx != nil {
		t.Fatal("rate across a counter reset")
	}
}

func TestOSRelease(t *testing.T) {
	s := linuxSystem(parseOSRelease(fixture(t, "os-release-ubuntu")))
	if s.Distro != "ubuntu" || s.Like != "debian" || s.Name != "Ubuntu 24.04.1 LTS" || s.Version != "24.04" {
		t.Fatalf("ubuntu = %+v", s)
	}
	s = linuxSystem(parseOSRelease(fixture(t, "os-release-debian")))
	if s.Distro != "debian" || s.Name != "Debian GNU/Linux 12 (bookworm)" || s.Version != "12" {
		t.Fatalf("debian = %+v", s)
	}
	if s := linuxSystem(nil); s.Distro != "linux" || s.Name != "Linux" {
		t.Fatalf("no os-release = %+v", s)
	}
}

func TestHardwareNames(t *testing.T) {
	if m := cpuModel(fixture(t, "cpuinfo")); m != "AMD Ryzen 9 7950X 16-Core Processor" {
		t.Fatalf("x86 cpu = %q", m)
	}
	if m := cpuModel(fixture(t, "cpuinfo_arm")); m != "BCM2835" {
		t.Fatalf("arm cpu = %q", m)
	}
	for _, c := range [][3]string{
		{"Dell Inc.\n", "OptiPlex 7090\n", "Dell Inc. OptiPlex 7090"},
		{"LENOVO", "ThinkCentre LENOVO M75q", "ThinkCentre LENOVO M75q"},
		{"System manufacturer", "System Product Name", ""},
		{"QEMU", "Standard PC (Q35 + ICH9, 2009)", "QEMU Standard PC (Q35 + ICH9, 2009)"},
		{"", "", ""},
	} {
		if got := dmiModel(c[0], c[1]); got != c[2] {
			t.Errorf("dmi %q %q = %q, want %q", c[0], c[1], got, c[2])
		}
	}
	name, version, build := parseSwVers(fixture(t, "sw_vers"))
	if name != "macOS" || version != "26.6.2" || build != "25G83" {
		t.Fatalf("sw_vers = %q %q %q", name, version, build)
	}
	if m := profilerModel(fixture(t, "system_profiler")); m != "Mac Studio" {
		t.Fatalf("model = %q", m)
	}
}

func TestVMStat(t *testing.T) {
	var page uint64
	var rows []vmStat
	sc := bufio.NewScanner(strings.NewReader(string(fixture(t, "vm_stat"))))
	for sc.Scan() {
		if p, ok := parseVMStatHeader(sc.Text()); ok {
			page = p
		} else if v, ok := parseVMStatRow(sc.Text()); ok {
			rows = append(rows, v)
		}
	}
	if page != 16384 || len(rows) != 2 {
		t.Fatalf("page %d, %d rows (the column header is not a row)", page, len(rows))
	}
	v := rows[1]
	if v.anonymous != 1003369 || v.purgeable != 4150 || v.wired != 700618 || v.compressor != 1921486 || v.free != 8142 {
		t.Fatalf("row = %+v", v)
	}
	if used := darwinMemoryUsed(v, page); used != (1003369-4150+700618+1921486)*16384 {
		t.Fatalf("used = %d", used)
	}
	if n, ok := abbreviated("12456M"); !ok || n != 12456<<20 {
		t.Fatalf("abbreviated = %d", n)
	}
	// A header cut off after "page size of " is not a header (it used to index an empty field list and panic).
	for _, line := range []string{"Mach Virtual Memory Statistics: (page size of ", "page size of    "} {
		if n, ok := parseVMStatHeader(line); ok || n != 0 {
			t.Fatalf("parseVMStatHeader(%q) = %d, %v", line, n, ok)
		}
	}
}

func TestIostat(t *testing.T) {
	var busy, load []float64
	for _, line := range strings.Split(string(fixture(t, "iostat")), "\n") {
		if b, l, ok := parseIostatRow(line); ok {
			busy, load = append(busy, b), append(load, l)
		}
	}
	if len(busy) != 2 || busy[0] != 66 || busy[1] != 37 || load[0] != 18.76 {
		t.Fatalf("iostat busy %v load %v", busy, load)
	}
}

func TestDiskUsage(t *testing.T) {
	// df: 1K-blocks 971350180, available 614658756, used 333036572 → capacity 36%.
	used, total := diskUsage(971350180, 971350180-333036572, 614658756, 1024)
	if used != 333036572*1024 || total != (333036572+614658756)*1024 {
		t.Fatalf("disk %d / %d", used, total)
	}
}

func TestDiskMounts(t *testing.T) {
	mounts := "/dev/vda1 / ext4 rw,relatime 0 0\n" +
		"proc /proc proc rw 0 0\n" +
		"/dev/vdb /data00 ext4 rw,relatime 0 0\n" +
		"/dev/vdb /home ext4 rw,relatime 0 0\n" + // bind mount of the same disk
		"/dev/loop3 /snap/core/1 squashfs ro 0 0\n" +
		"/dev/sr0 /media/cd iso9660 ro 0 0\n" +
		"/dev/sdc1 /mnt/my\\040disk xfs rw 0 0\n" +
		"tmpfs /run tmpfs rw 0 0\n"
	got := strings.Join(diskMounts([]byte(mounts)), "|")
	if got != "/|/data00|/mnt/my disk" {
		t.Fatalf("mounts %q", got)
	}
}

func TestSampleHere(t *testing.T) {
	c := NewCollector(context.Background())
	defer c.Close()
	now := time.Now()
	_ = c.Sample(now)
	s := c.Sample(now.Add(time.Second))
	if s.MemTotal == 0 || s.DiskTotal == 0 {
		t.Fatalf("sample on this machine = %+v", s)
	}
	if sys := ReadSystem(context.Background()); sys.Cores == 0 || sys.Kernel == "" || sys.Name == "" {
		t.Fatalf("system = %+v", sys)
	}
}

func TestDiskMountinfo(t *testing.T) {
	// A Kubernetes pod: / is its own disk; the host's data disk comes in as directories (emptyDir on /tmp,
	// /etc/hosts, a log directory) and the root disk again under /host. Only / counts.
	pod := "2049 1801 8:1 / / rw,relatime master:1 - ext4 /dev/sda1 rw\n" +
		"2050 2049 0:310 / /proc rw - proc proc rw\n" +
		"2061 2049 8:17 /k8s/pods/0f/volumes/kubernetes.io~empty-dir/tmp /tmp rw - ext4 /dev/sdb1 rw\n" +
		"2062 2049 8:1 / /host rw - ext4 /dev/sda1 rw\n" +
		"2063 2049 8:17 /k8s/pods/0f/etc-hosts /etc/hosts rw - ext4 /dev/sdb1 rw\n" +
		"2064 2049 8:17 /var/log/pods/0f /var/log/app rw - ext4 /dev/sdb1 rw\n" +
		"2065 2049 0:52 / /dev/shm rw - tmpfs shm rw\n"
	if got := strings.Join(diskMountinfo([]byte(pod)), "|"); got != "/" {
		t.Fatalf("pod mounts %q", got)
	}
	// A plain machine: whole disks on / and /data00 count, a bind of a directory of /data00 does not repeat it,
	// loop and image filesystems are skipped, escapes are undone.
	box := "22 1 252:1 / / rw - ext4 /dev/vda1 rw\n" +
		"30 22 252:16 / /data00 rw - ext4 /dev/vdb rw\n" +
		"31 22 252:16 /home /home rw - ext4 /dev/vdb rw\n" +
		"32 22 7:3 / /snap/core/1 ro - squashfs /dev/loop3 ro\n" +
		"33 22 11:0 / /media/cd ro - iso9660 /dev/sr0 ro\n" +
		"34 22 8:33 / /mnt/my\\040disk rw - xfs /dev/sdc1 rw\n"
	if got := strings.Join(diskMountinfo([]byte(box)), "|"); got != "/|/data00|/mnt/my disk" {
		t.Fatalf("box mounts %q", got)
	}
	// btrfs mounts subvolumes (Ubuntu's /@ on /, /@home on /home): each reports the whole filesystem, and both are
	// one device, so it counts once, whichever comes first.
	btrfs := "25 1 0:25 /@ / rw - btrfs /dev/nvme0n1p2 rw\n" +
		"26 25 0:25 /@home /home rw - btrfs /dev/nvme0n1p2 rw\n" +
		"27 25 0:26 /@data /srv rw - btrfs /dev/sdb1 rw\n"
	if got := strings.Join(diskMountinfo([]byte(btrfs)), "|"); got != "/|/srv" {
		t.Fatalf("btrfs mounts %q", got)
	}
}

func TestCgroupMemory(t *testing.T) {
	write := func(t *testing.T, files map[string]string) string {
		dir := t.TempDir()
		for name, body := range files {
			p := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	cases := []struct {
		name        string
		files       map[string]string
		limit, used int64
		ok          bool
	}{
		{"v2 limited", map[string]string{
			"memory.max": "34359738368\n", "memory.current": "31321325568\n",
			"memory.stat": "anon 3451019264\nfile 24192638976\ninactive_file 15724765184\nactive_file 8461402112\n",
		}, 34359738368, 31321325568 - 15724765184, true},
		{"v2 unlimited", map[string]string{"memory.max": "max\n", "memory.current": "1000\n"}, 0, 0, false},
		{"v1 limited", map[string]string{
			"memory/memory.limit_in_bytes": "34359738368\n", "memory/memory.usage_in_bytes": "31321325568\n",
			"memory/memory.stat": "cache 24192638976\nrss 3451019264\ninactive_file 1\ntotal_inactive_file 15724765184\n",
		}, 34359738368, 31321325568 - 15724765184, true},
		{"v1 unlimited", map[string]string{
			"memory/memory.limit_in_bytes": "9223372036854771712\n", "memory/memory.usage_in_bytes": "115230494720\n",
		}, 0, 0, false},
		{"v1 no stat", map[string]string{
			"memory/memory.limit_in_bytes": "1073741824\n", "memory/memory.usage_in_bytes": "536870912\n",
		}, 1073741824, 536870912, true},
		{"no cgroup", map[string]string{}, 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			limit, used, ok := cgroupMemory(write(t, c.files))
			if limit != c.limit || used != c.used || ok != c.ok {
				t.Fatalf("got %d / %d %v, want %d / %d %v", used, limit, ok, c.used, c.limit, c.ok)
			}
		})
	}
}

// LoadAverage reads the kernel value (no locale-dependent CLI). On darwin and linux it must be a finite non-negative number.
func TestLoadAverage(t *testing.T) {
	v := LoadAverage()
	if v < 0 || v != v { // NaN
		t.Fatalf("LoadAverage = %v", v)
	}
	switch runtime.GOOS {
	case "darwin", "linux":
		// A live machine's 1-minute load is almost never exactly 0 for long; 0 usually meant the old text parse failed.
		if v == 0 {
			t.Log("LoadAverage is 0; unusual on a busy CI host but legal")
		}
	default:
		if v != 0 {
			t.Fatalf("LoadAverage = %v on %s, want 0", v, runtime.GOOS)
		}
	}
}
