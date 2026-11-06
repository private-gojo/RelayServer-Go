// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Gojo Research

package utilities

import (
	"sync"
	"testing"
)

func TestParseCPUStatConvertsV1ThrottledTimeToMicroseconds(t *testing.T) {
	usage, throttled := parseCPUStat("usage 900\nthrottled_time 242104013\n")
	if usage != nil {
		t.Fatalf("usage_usec = %d, want nil when usage_usec is absent", *usage)
	}
	if throttled == nil || *throttled != 242104 {
		t.Fatalf("throttled_usec = %v, want 242104", throttled)
	}
}

func TestParseCPUStatKeepsV2ThrottledMicroseconds(t *testing.T) {
	usage, throttled := parseCPUStat("usage_usec 1200000\nthrottled_usec 4567\n")
	if usage == nil || *usage != 1200000 {
		t.Fatalf("usage_usec = %v, want 1200000", usage)
	}
	if throttled == nil || *throttled != 4567 {
		t.Fatalf("throttled_usec = %v, want 4567", throttled)
	}
}

func TestCalculateMbps(t *testing.T) {
	if got := calculateMbps(2_000_000, 1_000_000, 2); got != 4 {
		t.Fatalf("calculateMbps() = %v, want 4", got)
	}
	if got := calculateMbps(10, 20, 1); got != 0 {
		t.Fatalf("calculateMbps() with reset counter = %v, want 0", got)
	}
}

func TestTelemetryLifecycleCountersConcurrent(t *testing.T) {
	counters := &TelemetryCounters{}
	const workers = 32
	var waitGroup sync.WaitGroup

	for range workers {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			counters.ConnectionStarted()
			counters.HandshakeFinished(true)
			counters.SSHSessionStarted()
			counters.SSHSessionFinished()
			counters.ConnectionFinished()
		}()
	}
	waitGroup.Wait()

	snapshot := counters.Snapshot()
	if snapshot.ActiveConnections != 0 || snapshot.ActiveHandshakes != 0 || snapshot.ActiveSSHSessions != 0 {
		t.Fatalf("active counters did not return to zero: %+v", snapshot)
	}
	if snapshot.TotalConnections != workers || snapshot.TotalSSHSessions != workers {
		t.Fatalf("total counters = (%d, %d), want (%d, %d)",
			snapshot.TotalConnections, snapshot.TotalSSHSessions, workers, workers)
	}
	if snapshot.PeakActiveSSHSessions < 1 {
		t.Fatalf("peak_active_ssh_sessions = %d, want at least 1", snapshot.PeakActiveSSHSessions)
	}
}
