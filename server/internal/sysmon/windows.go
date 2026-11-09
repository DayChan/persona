//go:build windows

package sysmon

import (
	"context"
	"fmt"
	"net"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/nwverse/persona/server/internal/proto"
)

// Windows: Win32 calls only (the build is CGO_ENABLED=0, and a sample starts no process). The parsers are in
// winparse.go.

var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGetSystemTimes       = kernel32.NewProc("GetSystemTimes")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
)

// ReadSystem reads the edition, version and build from the registry (what winver shows), the kernel version from
// RtlGetVersion, the BIOS's vendor and model, the CPU's name, the memory and the boot time.
func ReadSystem(ctx context.Context) proto.System {
	s := proto.System{OS: runtime.GOOS, Distro: "windows", Name: "Windows", Arch: runtime.GOARCH, Cores: runtime.NumCPU()}
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE); err == nil {
		product, _, _ := k.GetStringValue("ProductName")
		build, _, _ := k.GetStringValue("CurrentBuild")
		ubr, _, _ := k.GetIntegerValue("UBR")
		if s.Version, _, _ = k.GetStringValue("DisplayVersion"); s.Version == "" {
			s.Version, _, _ = k.GetStringValue("ReleaseId")
		}
		k.Close()
		n, _ := strconv.Atoi(build)
		s.Name, s.Build = strings.TrimSpace(windowsName(product, n)+" "+s.Version), windowsBuild(build, ubr)
	}
	if v := windows.RtlGetVersion(); v != nil {
		s.Kernel = fmt.Sprintf("NT %d.%d.%d", v.MajorVersion, v.MinorVersion, v.BuildNumber)
	}
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\BIOS`, registry.QUERY_VALUE); err == nil {
		vendor, _, _ := k.GetStringValue("SystemManufacturer")
		product, _, _ := k.GetStringValue("SystemProductName")
		k.Close()
		s.Model = dmiModel(vendor, product)
	}
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.QUERY_VALUE); err == nil {
		name, _, _ := k.GetStringValue("ProcessorNameString")
		k.Close()
		s.CPU = strings.Join(strings.Fields(name), " ")
	}
	if m, ok := memoryStatus(); ok {
		s.Memory = int64(m.TotalPhys)
	}
	s.BootTime = time.Now().Add(-windows.DurationSinceBoot()).Unix()
	return s
}

type windowsCollector struct {
	cpu   cpuTimes
	cores []cpuTimes
	net   netCounters
	buf   []byte // NtQuerySystemInformation's answers, kept between samples
	// Which network interfaces and drives count, looked up every listEvery: listing the adapters takes ~7 ms, reading
	// their counters ~0.2 ms.
	nics     []uint32 // interface indexes of the physical NICs
	drives   []string // roots of the drives on a disk
	listedAt time.Time
}

// NewCollector starts sampling (nothing runs between samples on Windows).
func NewCollector(ctx context.Context) Collector { return &windowsCollector{} }

func (c *windowsCollector) Close() {}

func (c *windowsCollector) Sample(now time.Time) proto.MetricSample {
	s := proto.MetricSample{T: now.Unix()} // no load average on Windows
	var idle, kernel, user windows.Filetime
	if r, _, _ := procGetSystemTimes.Call(uintptr(unsafe.Pointer(&idle)), uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user))); r != 0 {
		all := systemCPU(filetime(idle), filetime(kernel), filetime(user))
		s.CPU = cpuPercent(c.cpu, all)
		c.cpu = all
	}
	// Per core: one processor group's worth (up to 64), so only when that is all of them. The class wants a buffer of
	// exactly one entry per logical CPU (a bigger one is refused as a length mismatch).
	if n := runtime.NumCPU(); n <= 64 {
		if b, err := c.queryExact(windows.SystemProcessorPerformanceInformation, n*processorTimesSize); err == nil {
			c.sampleCores(&s, parseProcessorTimes(b))
		}
	}
	// Used = total − available: the standby list (file cache) is available, as MemAvailable is on Linux.
	if m, ok := memoryStatus(); ok {
		s.MemTotal, s.MemUsed = int64(m.TotalPhys), int64(m.TotalPhys-min(m.AvailPhys, m.TotalPhys))
	}
	if b, err := c.query(windows.SystemPageFileInformation); err == nil {
		s.SwapUsed, s.SwapTotal = parsePagefiles(b, uint64(os.Getpagesize()))
	}
	if c.listedAt.IsZero() || now.Sub(c.listedAt) >= listEvery || now.Before(c.listedAt) {
		nics := physicalNICs()
		if !slices.Equal(nics, c.nics) {
			c.net = netCounters{} // another set of NICs: their sum jumps, so no rate this time
		}
		c.nics, c.drives, c.listedAt = nics, diskDrives(), now
	}
	cur, ok := netCounters{at: now}, true
	for _, i := range c.nics {
		row := windows.MibIfRow2{InterfaceIndex: i}
		if windows.GetIfEntry2Ex(windows.MibIfEntryNormal, &row) != nil {
			ok = false // gone: list them again next time
			break
		}
		cur.rx += row.InOctets
		cur.tx += row.OutOctets
	}
	if ok {
		s.NetRx, s.NetTx = netRates(c.net, cur)
		c.net = cur
	} else {
		c.net, c.listedAt = netCounters{}, time.Time{}
	}
	// Every local drive on a disk, as Linux adds up every local block device: used = total − free, total = used +
	// available to the user (like df).
	for _, root := range c.drives {
		p, err := windows.UTF16PtrFromString(root)
		if err != nil {
			continue
		}
		var avail, total, free uint64
		if windows.GetDiskFreeSpaceEx(p, &avail, &total, &free) == nil {
			used := total - min(free, total)
			s.DiskUsed += int64(used)
			s.DiskTotal += int64(used + avail)
		}
	}
	return s
}

// sampleCores puts each core's busy share in s (all of them or none), against the last reading.
func (c *windowsCollector) sampleCores(s *proto.MetricSample, cores []cpuTimes) {
	if len(cores) == runtime.NumCPU() && len(cores) == len(c.cores) {
		for i := range cores {
			if v := cpuPercent(c.cores[i], cores[i]); v != nil {
				s.Cores = append(s.Cores, *v)
			}
		}
		if len(s.Cores) != len(cores) {
			s.Cores = nil
		}
	}
	c.cores = cores
}

// queryExact is NtQuerySystemInformation of a class that takes a buffer of one size only.
func (c *windowsCollector) queryExact(class int32, size int) ([]byte, error) {
	if len(c.buf) < size {
		c.buf = make([]byte, size)
	}
	var n uint32
	if err := windows.NtQuerySystemInformation(class, unsafe.Pointer(&c.buf[0]), uint32(size), &n); err != nil {
		return nil, err
	}
	return c.buf[:min(int(n), size)], nil
}

// query is NtQuerySystemInformation of one class, the buffer grown as it asks. The answer is only good until the next
// query.
func (c *windowsCollector) query(class int32) ([]byte, error) {
	if len(c.buf) == 0 {
		c.buf = make([]byte, 4096)
	}
	for {
		var n uint32
		err := windows.NtQuerySystemInformation(class, unsafe.Pointer(&c.buf[0]), uint32(len(c.buf)), &n)
		if err == nil {
			return c.buf[:min(int(n), len(c.buf))], nil
		}
		if err != windows.STATUS_INFO_LENGTH_MISMATCH || len(c.buf) >= 1<<20 {
			return nil, err
		}
		c.buf = make([]byte, max(int(n), 2*len(c.buf)))
	}
}

// memoryStatusEx is MEMORYSTATUSEX.
type memoryStatusEx struct {
	Length, MemoryLoad                                 uint32
	TotalPhys, AvailPhys, TotalPageFile, AvailPageFile uint64
	TotalVirtual, AvailVirtual, AvailExtendedVirtual   uint64
}

func memoryStatus() (memoryStatusEx, bool) {
	m := memoryStatusEx{}
	m.Length = uint32(unsafe.Sizeof(m))
	r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m)))
	return m, r != 0
}

func filetime(f windows.Filetime) uint64 { return uint64(f.HighDateTime)<<32 | uint64(f.LowDateTime) }

// physicalNICs are the interface indexes of the NICs that count (physicalWindows), sorted.
func physicalNICs() []uint32 {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var nics []uint32
	for _, i := range ifs {
		row := windows.MibIfRow2{InterfaceIndex: uint32(i.Index)}
		if windows.GetIfEntry2Ex(windows.MibIfEntryNormal, &row) == nil && physicalWindows(row.InterfaceAndOperStatusFlags) {
			nics = append(nics, uint32(i.Index))
		}
	}
	slices.Sort(nics)
	return nics
}

// ioctlVolumeGetVolumeDiskExtents is IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS.
const ioctlVolumeGetVolumeDiskExtents = 0x00560000

// diskDrives are the roots ("C:\"…) of the fixed drives whose volume sits on a disk, each volume once. Not removable,
// network, optical or RAM drives, and not the virtual drives of sync clients: Google Drive's G: says it is a fixed
// FAT32 drive the size of C:, but its volume has no extent on any disk.
func diskDrives() []string {
	buf := make([]uint16, 256)
	n, err := windows.GetLogicalDriveStrings(uint32(len(buf)), &buf[0])
	if err != nil || n == 0 || int(n) > len(buf) {
		return nil
	}
	var roots []string
	seen := map[string]bool{}
	for _, r := range parseDriveStrings(buf[:n]) {
		p, err := windows.UTF16PtrFromString(r)
		if err != nil || windows.GetDriveType(p) != windows.DRIVE_FIXED {
			continue
		}
		if key, ok := volumeExtent(strings.TrimSuffix(r, `\`)); ok && !seen[key] {
			seen[key] = true
			roots = append(roots, r)
		}
	}
	return roots
}

// volumeExtent is where the volume of a drive ("C:") starts on which disk (parseDiskExtents).
func volumeExtent(drive string) (string, bool) {
	p, err := windows.UTF16PtrFromString(`\\.\` + drive)
	if err != nil {
		return "", false
	}
	// No access asked for: the volume's layout needs none, and no administrator.
	h, err := windows.CreateFile(p, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return "", false
	}
	defer windows.CloseHandle(h)
	b := make([]byte, 8+8*diskExtentSize)
	var n uint32
	if err := windows.DeviceIoControl(h, ioctlVolumeGetVolumeDiskExtents, nil, 0, &b[0], uint32(len(b)), &n, nil); err != nil &&
		err != windows.ERROR_MORE_DATA {
		return "", false
	}
	return parseDiskExtents(b[:n])
}

func loadAverageOS() *float64 { return nil }
