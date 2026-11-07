// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Gojo Research

package utilities

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

type TelemetryCounters struct {
	SessionSeq            atomic.Uint64
	ActiveConnections     atomic.Int64
	ActiveHandshakes      atomic.Int64
	ActiveSSHSessions     atomic.Int64
	TotalConnections      atomic.Uint64
	TotalSSHSessions      atomic.Uint64
	FailedHandshakes      atomic.Uint64
	FailedSSHConnections  atomic.Uint64
	SessionErrors         atomic.Uint64
	RejectedConns         atomic.Uint64
	PeakActiveSSHSessions atomic.Int64
	TotalClientToSSH      atomic.Uint64
	TotalSSHToClient      atomic.Uint64
}

type TelemetrySnapshot struct {
	ActiveConnections     int64
	ActiveHandshakes      int64
	ActiveSSHSessions     int64
	TotalConnections      uint64
	TotalSSHSessions      uint64
	FailedHandshakes      uint64
	FailedSSHConnections  uint64
	SessionErrors         uint64
	RejectedConns         uint64
	PeakActiveSSHSessions int64
	TotalClientToSSH      uint64
	TotalSSHToClient      uint64
}

var Counters = &TelemetryCounters{}

func (c *TelemetryCounters) ConnectionStarted() uint64 {
	id := c.SessionSeq.Add(1)
	c.TotalConnections.Add(1)
	c.ActiveConnections.Add(1)
	c.ActiveHandshakes.Add(1)
	return id
}

func (c *TelemetryCounters) HandshakeFinished(success bool) {
	c.ActiveHandshakes.Add(-1)
	if !success {
		c.FailedHandshakes.Add(1)
	}
}

func (c *TelemetryCounters) HandshakeCancelled() {
	c.ActiveHandshakes.Add(-1)
}

func (c *TelemetryCounters) SSHSessionStarted() {
	active := c.ActiveSSHSessions.Add(1)
	c.TotalSSHSessions.Add(1)
	for peak := c.PeakActiveSSHSessions.Load(); active > peak; peak = c.PeakActiveSSHSessions.Load() {
		if c.PeakActiveSSHSessions.CompareAndSwap(peak, active) {
			break
		}
	}
}

func (c *TelemetryCounters) SSHSessionFinished() {
	c.ActiveSSHSessions.Add(-1)
}

func (c *TelemetryCounters) ConnectionFinished() {
	c.ActiveConnections.Add(-1)
}

func (c *TelemetryCounters) Snapshot() TelemetrySnapshot {
	return TelemetrySnapshot{
		ActiveConnections:     c.ActiveConnections.Load(),
		ActiveHandshakes:      c.ActiveHandshakes.Load(),
		ActiveSSHSessions:     c.ActiveSSHSessions.Load(),
		TotalConnections:      c.TotalConnections.Load(),
		TotalSSHSessions:      c.TotalSSHSessions.Load(),
		FailedHandshakes:      c.FailedHandshakes.Load(),
		FailedSSHConnections:  c.FailedSSHConnections.Load(),
		SessionErrors:         c.SessionErrors.Load(),
		RejectedConns:         c.RejectedConns.Load(),
		PeakActiveSSHSessions: c.PeakActiveSSHSessions.Load(),
		TotalClientToSSH:      c.TotalClientToSSH.Load(),
		TotalSSHToClient:      c.TotalSSHToClient.Load(),
	}
}

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

func parseCPUStat(data string) (usageUsec *int64, throttledUsec *int64) {
	var throttledNsec *int64
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		value, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "usage_usec":
			v := value
			usageUsec = &v
		case "throttled_usec":
			v := value
			throttledUsec = &v
		case "throttled_time":
			v := value / 1000
			throttledNsec = &v
		}
	}
	if throttledUsec == nil {
		throttledUsec = throttledNsec
	}
	return usageUsec, throttledUsec
}

func readCgroupMetrics() (string, *int64, *int64, string, string) {
	quota := "n/a"
	var cpuUsageUsec *int64
	var throttledUsec *int64
	memCurrent := "n/a"
	memMax := "n/a"

	if data, err := os.ReadFile("/sys/fs/cgroup/cpu.max"); err == nil {
		quota = strings.TrimSpace(string(data))
	}
	for _, path := range []string{"/sys/fs/cgroup/cpu.stat", "/sys/fs/cgroup/cpu/cpu.stat"} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		cpuUsageUsec, throttledUsec = parseCPUStat(string(data))
		break
	}

	if cpuUsageUsec == nil {
		if data, err := os.ReadFile("/sys/fs/cgroup/cpuacct/cpuacct.usage"); err == nil {
			if value, parseErr := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64); parseErr == nil {
				converted := value / 1000
				cpuUsageUsec = &converted
			}
		}
	}

	if current, err := os.ReadFile("/sys/fs/cgroup/memory.current"); err == nil {
		memCurrent = strings.TrimSpace(string(current))
		if maximum, readErr := os.ReadFile("/sys/fs/cgroup/memory.max"); readErr == nil {
			memMax = strings.TrimSpace(string(maximum))
		}
	} else if current, err := os.ReadFile("/sys/fs/cgroup/memory/memory.usage_in_bytes"); err == nil {
		memCurrent = strings.TrimSpace(string(current))
		if maximum, readErr := os.ReadFile("/sys/fs/cgroup/memory/memory.limit_in_bytes"); readErr == nil {
			memMax = strings.TrimSpace(string(maximum))
		}
	}
	return quota, cpuUsageUsec, throttledUsec, memCurrent, memMax
}

func getProcessCPUTimeSeconds() float64 {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0
	}
	userTime := float64(usage.Utime.Sec) + float64(usage.Utime.Usec)/1e6
	systemTime := float64(usage.Stime.Sec) + float64(usage.Stime.Usec)/1e6
	return userTime + systemTime
}

func calculateMbps(current, previous uint64, elapsedSeconds float64) float64 {
	if current < previous || elapsedSeconds <= 0 {
		return 0
	}
	return float64(current-previous) * 8 / elapsedSeconds / 1e6
}

func StartMetricsLoop(stopCh <-chan struct{}, exporter *CloudMonitoringExporter) {
	const interval = 10 * time.Second
	nextTick := time.Now().Add(interval)
	prevWall := time.Now()
	prevCPU := getProcessCPUTimeSeconds()
	_, prevCgroupCPU, prevThrottled, _, _ := readCgroupMetrics()
	prevSnapshot := Counters.Snapshot()
	metricsLog := Logger.With("component", "telemetry")

	for {
		sleepDuration := time.Until(nextTick)
		if sleepDuration < 0 {
			sleepDuration = 0
		}
		timer := time.NewTimer(sleepDuration)
		select {
		case <-stopCh:
			timer.Stop()
			return
		case now := <-timer.C:
			lagMs := float64(now.Sub(nextTick).Microseconds()) / 1000
			if lagMs < 0 {
				lagMs = 0
			}
			elapsed := now.Sub(prevWall).Seconds()
			if elapsed < 0.001 {
				elapsed = 0.001
			}

			currentCPU := getProcessCPUTimeSeconds()
			processCores := (currentCPU - prevCPU) / elapsed
			if processCores < 0 {
				processCores = 0
			}

			cpuQuota, currentCgroupCPU, currentThrottled, memoryCurrent, memoryMax := readCgroupMetrics()
			containerCores := "n/a"
			if currentCgroupCPU != nil && prevCgroupCPU != nil {
				delta := *currentCgroupCPU - *prevCgroupCPU
				if delta < 0 {
					delta = 0
				}
				containerCores = fmt.Sprintf("%.3f", float64(delta)/1e6/elapsed)
			}

			throttledDeltaUsec := "n/a"
			if currentThrottled != nil && prevThrottled != nil {
				delta := *currentThrottled - *prevThrottled
				if delta < 0 {
					delta = 0
				}
				throttledDeltaUsec = strconv.FormatInt(delta, 10)
			}

			rssKB, openFDs := readProcMetrics()
			snapshot := Counters.Snapshot()
			clientToSSHMbps := calculateMbps(snapshot.TotalClientToSSH, prevSnapshot.TotalClientToSSH, elapsed)
			sshToClientMbps := calculateMbps(snapshot.TotalSSHToClient, prevSnapshot.TotalSSHToClient, elapsed)

			metricsLog.Info("runtime_metrics",
				"active_connections", snapshot.ActiveConnections,
				"active_handshakes", snapshot.ActiveHandshakes,
				"active_ssh_sessions", snapshot.ActiveSSHSessions,
				"total_connections", snapshot.TotalConnections,
				"total_ssh_sessions", snapshot.TotalSSHSessions,
				"peak_active_ssh_sessions", snapshot.PeakActiveSSHSessions,
				"failed_handshakes", snapshot.FailedHandshakes,
				"failed_ssh_connections", snapshot.FailedSSHConnections,
				"session_errors", snapshot.SessionErrors,
				"rejected_connections", snapshot.RejectedConns,
				"client_to_ssh_bytes_total", snapshot.TotalClientToSSH,
				"ssh_to_client_bytes_total", snapshot.TotalSSHToClient,
				"client_to_ssh_mbps", clientToSSHMbps,
				"ssh_to_client_mbps", sshToClientMbps,
				"process_cpu_cores", processCores,
				"container_cpu_cores", containerCores,
				"cpu_quota", cpuQuota,
				"cpu_throttled_delta_usec", throttledDeltaUsec,
				"rss_kb", rssKB,
				"open_fds", openFDs,
				"cgroup_memory_current", memoryCurrent,
				"cgroup_memory_max", memoryMax,
				"scheduler_lag_ms", lagMs,
			)

			if exporter != nil {
				exporter.Submit(MonitoringSample{
					Snapshot:        snapshot,
					ClientToSSHMbps: clientToSSHMbps,
					SSHToClientMbps: sshToClientMbps,
					Timestamp:       now,
				})
			}
			prevWall = now
			prevCPU = currentCPU
			prevCgroupCPU = currentCgroupCPU
			prevThrottled = currentThrottled
			prevSnapshot = snapshot
			nextTick = nextTick.Add(interval)
			if !nextTick.After(now) {
				nextTick = now.Add(interval)
			}
		}
	}
}
