package agentsvc

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/israel-duff/pgdock/internal/agentapi"
)

// hostMetrics reads load, memory, and disk usage from /proc and statfs.
func hostMetrics(diskPath string) agentapi.HostMetrics {
	m := agentapi.HostMetrics{CPUs: runtime.NumCPU(), DiskPath: diskPath}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		f := strings.Fields(string(b))
		if len(f) >= 2 {
			m.Load1, _ = strconv.ParseFloat(f[0], 64)
			m.Load5, _ = strconv.ParseFloat(f[1], 64)
		}
	}
	if f, err := os.Open("/proc/meminfo"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) < 2 {
				continue
			}
			kb, _ := strconv.ParseInt(fields[1], 10, 64)
			switch fields[0] {
			case "MemTotal:":
				m.MemTotalBytes = kb * 1024
			case "MemAvailable:":
				m.MemAvailableBytes = kb * 1024
			}
		}
		_ = f.Close()
	}
	m.CPUBusyTicks, m.CPUTotalTicks = cpuTicks()
	m.DiskReadBytes, m.DiskWriteBytes = diskBytes()
	var st syscall.Statfs_t
	if err := syscall.Statfs(diskPath, &st); err == nil {
		m.DiskTotalBytes = int64(st.Blocks) * int64(st.Bsize) //nolint:unconvert // Bsize is int32 on some platforms
		m.DiskFreeBytes = int64(st.Bavail) * int64(st.Bsize)  //nolint:unconvert // likewise
	}
	return m
}

// cpuTicks sums the aggregate "cpu" line of /proc/stat: busy is everything
// but idle and iowait.
func cpuTicks() (busy, total uint64) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0
	}
	line, _, _ := strings.Cut(string(b), "\n")
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "cpu" {
		return 0, 0
	}
	var idle uint64
	for i, v := range f[1:] {
		n, _ := strconv.ParseUint(v, 10, 64)
		if i >= 8 { // guest and guest_nice are already in user and nice
			break
		}
		total += n
		if i == 3 || i == 4 {
			idle += n
		}
	}
	return total - idle, total
}

// diskBytes sums sectors read and written by whole disks (those listed in
// /sys/block, less loop and RAM devices) from /proc/diskstats.
func diskBytes() (read, written uint64) {
	ents, err := os.ReadDir("/sys/block")
	if err != nil {
		return 0, 0
	}
	disks := map[string]bool{}
	for _, e := range ents {
		n := e.Name()
		if !strings.HasPrefix(n, "loop") && !strings.HasPrefix(n, "ram") && !strings.HasPrefix(n, "zram") {
			disks[n] = true
		}
	}
	f, err := os.Open("/proc/diskstats")
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 10 || !disks[fields[2]] {
			continue
		}
		r, _ := strconv.ParseUint(fields[5], 10, 64)
		w, _ := strconv.ParseUint(fields[9], 10, 64)
		read += r * 512
		written += w * 512
	}
	return read, written
}
