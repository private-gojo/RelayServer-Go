// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Gojo Research

package utilities

import (
	"encoding/json"
	"testing"
	"time"
)

func TestBuildMonitoringTimeSeries(t *testing.T) {
	metadata := InstanceMetadata{
		ProjectID:         "test-project",
		Location:          "asia-east1",
		ServiceName:       "x-relay",
		RevisionName:      "x-relay-00001-abc",
		ConfigurationName: "x-relay",
		InstanceID:        "123456789",
	}
	sample := MonitoringSample{
		Snapshot: TelemetrySnapshot{
			ActiveConnections:     3,
			ActiveHandshakes:      1,
			ActiveSSHSessions:     2,
			TotalConnections:      12,
			TotalSSHSessions:      9,
			PeakActiveSSHSessions: 4,
			FailedHandshakes:      2,
			FailedSSHConnections:  1,
			SessionErrors:         3,
			RejectedConns:         1,
			TotalClientToSSH:      1000,
			TotalSSHToClient:      2000,
		},
		ClientToSSHMbps: 1.25,
		SSHToClientMbps: 2.5,
		Timestamp:       time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC),
	}

	series := buildMonitoringTimeSeries(metadata, sample)
	if len(series) != 14 {
		t.Fatalf("time series count = %d, want 14", len(series))
	}
	for _, item := range series {
		if item.Resource.Type != "cloud_run_revision" {
			t.Fatalf("resource type = %q, want cloud_run_revision", item.Resource.Type)
		}
		if item.Metric.Labels["instance_id"] != metadata.InstanceID {
			t.Fatalf("instance_id label = %q, want %q", item.Metric.Labels["instance_id"], metadata.InstanceID)
		}
		if len(item.Points) != 1 {
			t.Fatalf("metric %q has %d points, want 1", item.Metric.Type, len(item.Points))
		}
	}

	encoded, err := json.Marshal(monitoringRequest{TimeSeries: series})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if _, ok := decoded["timeSeries"]; !ok {
		t.Fatal("request is missing timeSeries")
	}
}
