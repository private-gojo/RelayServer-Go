package utilities

import (
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// TelemetryCounters tracks lock-free atomic counters across all CPU cores.
type TelemetryCounters struct {
	SessionSeq       atomic.Uint64
	ActiveSessions   atomic.Int64
	TotalSessions    atomic.Uint64
	TotalClientToSSH atomic.Uint64
	TotalSSHToClient atomic.Uint64
	RejectedConns    atomic.Uint64
}

var Counters = &TelemetryCounters{}

// OptimizeSocket applies exact low-latency TCP options from proxy.py while preserving kernel TCP autotuning.
func OptimizeSocket(conn net.Conn) {
	tcpConn, ok := conn.(*net.TCPConn)
	if !ok || tcpConn == nil {
		return
	}
	_ = tcpConn.SetNoDelay(true)
	_ = tcpConn.SetKeepAlive(true)
	_ = tcpConn.SetKeepAliveConfig(net.KeepAliveConfig{
		Enable:   true,
		Idle:     15 * time.Second,
		Interval: 10 * time.Second,
		Count:    3,
	})
}

func readProcMetrics() (string, string) {
	rssKB := "n/a"
	openFDs := "n/a"

	if data, err := os.ReadFile("/proc/self/status"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "VmRSS:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					rssKB = fields[1]
				}
				break
			}
		}
	}

	if entries, err := os.ReadDir("/proc/self/fd"); err == nil {
		openFDs = strconv.Itoa(len(entries))
	}

	return rssKB, openFDs
}

func readCgroupMetrics() (string, *int64, *int64, string, string) {
	quota := "n/a"
	var cpuUsageUsec *int64
	var throttled *int64
	memCurrent := "n/a"
	memMax := "n/a"

	if b, err := os.ReadFile("/sys/fs/cgroup/cpu.max"); err == nil {
		quota = strings.TrimSpace(string(b))
	}

	for _, p := range []string{"/sys/fs/cgroup/cpu.stat", "/sys/fs/cgroup/cpu/cpu.stat"} {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			fields := strings.Fields(line)
			if len(fields) != 2 {
				continue
			}
			val, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				continue
			}
			switch fields[0] {
			case "usage_usec":
				v := val
				cpuUsageUsec = &v
			case "throttled_usec", "throttled_time":
				if throttled == nil {
					v := val
					throttled = &v
				}
			}
		}
		break
	}

	if cpuUsageUsec == nil {
		if b, err := os.ReadFile("/sys/fs/cgroup/cpuacct/cpuacct.usage"); err == nil {
			if val, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil {
				v := val / 1000
				cpuUsageUsec = &v
			}
		}
	}

	if bCurr, err1 := os.ReadFile("/sys/fs/cgroup/memory.current"); err1 == nil {
		memCurrent = strings.TrimSpace(string(bCurr))
		if bMax, err2 := os.ReadFile("/sys/fs/cgroup/memory.max"); err2 == nil {
			memMax = strings.TrimSpace(string(bMax))
		}
	} else if bCurr, err1 := os.ReadFile("/sys/fs/cgroup/memory/memory.usage_in_bytes"); err1 == nil {
		memCurrent = strings.TrimSpace(string(bCurr))
		if bMax, err2 := os.ReadFile("/sys/fs/cgroup/memory/memory.limit_in_bytes"); err2 == nil {
			memMax = strings.TrimSpace(string(bMax))
		}
	}

	return quota, cpuUsageUsec, throttled, memCurrent, memMax
}

func getProcessCPUTimeSeconds() float64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	utime := float64(ru.Utime.Sec) + float64(ru.Utime.Usec)/1e6
	stime := float64(ru.Stime.Sec) + float64(ru.Stime.Usec)/1e6
	return utime + stime
}

// StartMetricsLoop runs the 10-second container & process resource diagnostic loop.
func StartMetricsLoop(stopCh <-chan struct{}) {
	const interval = 10 * time.Second
	nextTick := time.Now().Add(interval)
	prevWall := time.Now()
	prevCPU := getProcessCPUTimeSeconds()
	_, prevCgroupCPU, prevThrottled, _, _ := readCgroupMetrics()

	for {
		sleepDur := time.Until(nextTick)
		if sleepDur < 0 {
			sleepDur = 0
		}
		timer := time.NewTimer(sleepDur)
		select {
		case <-stopCh:
			timer.Stop()
			return
		case now := <-timer.C:
			lagMs := float64(now.Sub(nextTick).Microseconds()) / 1000.0
			if lagMs < 0 {
				lagMs = 0
			}
			elapsed := now.Sub(prevWall).Seconds()
			if elapsed < 0.001 {
				elapsed = 0.001
			}

			currCPU := getProcessCPUTimeSeconds()
			cpuCores := (currCPU - prevCPU) / elapsed
			if cpuCores < 0 {
				cpuCores = 0
			}

			cpuQuota, currCgroupCPU, currThrottled, memCurrent, memMax := readCgroupMetrics()
			containerCPUCores := "n/a"
			if currCgroupCPU != nil && prevCgroupCPU != nil {
				delta := *currCgroupCPU - *prevCgroupCPU
				if delta < 0 {
					delta = 0
				}
				containerCPUCores = fmt.Sprintf("%.3f", float64(delta)/1e6/elapsed)
			}

			throttledDelta := "n/a"
			if currThrottled != nil && prevThrottled != nil {
				delta := *currThrottled - *prevThrottled
				if delta < 0 {
					delta = 0
				}
				throttledDelta = strconv.FormatInt(delta, 10)
			}

			rssKB, openFDs := readProcMetrics()

			log.Printf(
				"[METRICS] active_sessions=%d total_sessions=%d rejected_conns=%d "+
					"client_to_ssh_bytes=%d ssh_to_client_bytes=%d "+
					"process_cpu_cores=%.3f container_cpu_cores=%s cpu_quota=%s "+
					"cpu_throttled_delta=%s rss_kb=%s open_fds=%s "+
					"cgroup_memory_current=%s cgroup_memory_max=%s scheduler_lag_ms=%.2f",
				Counters.ActiveSessions.Load(),
				Counters.TotalSessions.Load(),
				Counters.RejectedConns.Load(),
				Counters.TotalClientToSSH.Load(),
				Counters.TotalSSHToClient.Load(),
				cpuCores,
				containerCPUCores,
				cpuQuota,
				throttledDelta,
				rssKB,
				openFDs,
				memCurrent,
				memMax,
				lagMs,
			)

			prevWall = now
			prevCPU = currCPU
			prevCgroupCPU = currCgroupCPU
			prevThrottled = currThrottled
			nextTick = nextTick.Add(interval)
			if !nextTick.After(now) {
				nextTick = now.Add(interval)
			}
		}
	}
}
