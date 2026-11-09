//go:build linux

package sysmon

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// syntheticMountinfo is the mountinfo of a host running containers: two disks, then an overlay, a /proc, a tmpfs
// and a bind of a data-disk directory for each container.
func syntheticMountinfo(containers int) []byte {
	var b bytes.Buffer
	b.WriteString("22 1 252:1 / / rw,relatime shared:1 - ext4 /dev/vda1 rw\n")
	b.WriteString("30 22 252:16 / /data rw,relatime shared:2 - ext4 /dev/vdb rw\n")
	for i := range containers {
		id := 100 + 4*i
		fmt.Fprintf(&b, "%d 22 0:%d / /var/lib/docker/overlay2/%064x/merged rw,relatime - overlay overlay rw,lowerdir=/var/lib/docker/overlay2/l/A%d,upperdir=/var/lib/docker/overlay2/%064x/diff\n", id, 60+i, i, i, i)
		fmt.Fprintf(&b, "%d %d 0:%d / /run/netns/c%d rw - proc proc rw\n", id+1, id, 300+i, i)
		fmt.Fprintf(&b, "%d %d 0:%d / /run/containers/c%d/shm rw - tmpfs shm rw,size=65536k\n", id+2, id, 600+i, i)
		fmt.Fprintf(&b, "%d %d 252:16 /containers/c%d/log /run/containers/c%d/log rw - ext4 /dev/vdb rw\n", id+3, id, i, i)
	}
	return b.Bytes()
}

// BenchmarkLinuxSample also reports the process's user and system CPU per sample: most of a sample is the kernel
// writing /proc text, and on a busy host the wall time mostly measures waiting for a CPU.
func BenchmarkLinuxSample(b *testing.B) {
	c := NewCollector(context.Background())
	defer c.Close()
	now := time.Now()
	var r0, r1 syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &r0)
	b.ReportAllocs()
	for b.Loop() {
		now = now.Add(10 * time.Second)
		c.Sample(now)
	}
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &r1)
	b.ReportMetric(float64(r1.Utime.Nano()-r0.Utime.Nano())/float64(b.N), "user-ns/op")
	b.ReportMetric(float64(r1.Stime.Nano()-r0.Stime.Nano())/float64(b.N), "sys-ns/op")
}

func BenchmarkMountsUnchanged(b *testing.B) {
	path := filepath.Join(b.TempDir(), "mountinfo")
	if err := os.WriteFile(path, syntheticMountinfo(60), 0o644); err != nil {
		b.Fatal(err)
	}
	var c linuxCollector
	b.ReportAllocs()
	for b.Loop() {
		raw, err := c.read(path)
		if err != nil || len(c.mounts.of(raw)) != 2 {
			b.Fatal(err)
		}
	}
}

func TestMountCache(t *testing.T) {
	var m mountCache
	buf := syntheticMountinfo(3)
	first := m.of(buf)
	if !slices.Equal(first, diskMountinfo(buf)) || strings.Join(first, "|") != "/|/data" {
		t.Fatalf("mounts %q", first)
	}
	if again := m.of(slices.Clone(buf)); &again[0] != &first[0] {
		t.Fatal("the same mountinfo was parsed again")
	}
	// The caller reuses its buffer for the next read, so a change made in place, same length, must show.
	copy(buf, bytes.Replace(buf, []byte(" /data "), []byte(" /srv1 "), 1))
	if got := m.of(buf); !slices.Equal(got, diskMountinfo(buf)) || strings.Join(got, "|") != "/|/srv1" {
		t.Fatalf("changed mounts %q", got)
	}
}

func TestIsPhysicalCached(t *testing.T) {
	dev, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		t.Skip(err)
	}
	var c linuxCollector
	t0 := time.Now()
	for name := range parseNetDev(dev) {
		if got, want := c.isPhysical(name, t0), physical(name); got != want {
			t.Errorf("%s: cached %v, physical %v", name, got, want)
		}
	}
	// A wrong answer planted for lo stays until listEvery has passed, then lo is looked up again.
	c.nics["lo"] = true
	if !c.isPhysical("lo", t0.Add(listEvery-time.Second)) {
		t.Fatal("looked up again before listEvery")
	}
	if c.isPhysical("lo", t0.Add(listEvery)) {
		t.Fatal("not looked up again after listEvery")
	}
	// A clock that went back starts over too.
	c.nics["lo"] = true
	if c.isPhysical("lo", t0) {
		t.Fatal("not looked up again after the clock went back")
	}
}
