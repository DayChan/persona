package sysmon

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// referenceParseProcStat is parseProcStat before it skipped the lines it does not use; the faster one must agree.
func referenceParseProcStat(b []byte) (all cpuTimes, cores []cpuTimes, boot int64) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
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

// syntheticProcStat is a /proc/stat of a many-core host whose intr line is about intrLen bytes long.
func syntheticProcStat(cores, intrLen int) []byte {
	var b bytes.Buffer
	line := func(name string, i int) {
		fmt.Fprintf(&b, "%s %d %d %d %d %d 0 %d 0 0 0\n", name, 4705+i, 356+i, 584+i, 3699176+i, 23060+i, 277+i)
	}
	line("cpu", 0)
	for i := range cores {
		line("cpu"+strconv.Itoa(i), i)
	}
	b.WriteString("intr 114930548")
	for i := 0; b.Len() < 64*cores+intrLen; i++ {
		fmt.Fprintf(&b, " %d", i*7919%100000)
	}
	b.WriteString("\nctxt 1990473\nbtime 1790788452\nprocesses 2915\nprocs_running 1\nprocs_blocked 0\n")
	b.WriteString("softirq 1000 1 2 3 4 5 6 7 8 9 10\n")
	return b.Bytes()
}

func TestParseProcStatMatchesReference(t *testing.T) {
	inputs := map[string][]byte{
		"proc_stat":       fixture(t, "proc_stat"),
		"proc_stat_later": fixture(t, "proc_stat_later"),
		"synthetic":       syntheticProcStat(64, 20<<10),
		"odd lines":       []byte(" cpu  1 2 3 4\r\n\tcpu0 5 6 7 8\ncpu\ncpux 9\nbtime\n btime 42\nbtimes 7\nintr 1 2\n\n"),
	}
	if b, err := os.ReadFile("/proc/stat"); err == nil {
		inputs["this machine"] = b
	}
	for name, b := range inputs {
		all, cores, boot := parseProcStat(b)
		wantAll, wantCores, wantBoot := referenceParseProcStat(b)
		if all != wantAll || !slices.Equal(cores, wantCores) || boot != wantBoot {
			t.Errorf("%s: got %v %v %d, want %v %v %d", name, all, cores, boot, wantAll, wantCores, wantBoot)
		}
	}
}

func BenchmarkParseProcStat(b *testing.B) {
	stat := syntheticProcStat(64, 20<<10)
	b.ReportAllocs()
	for b.Loop() {
		parseProcStat(stat)
	}
}
