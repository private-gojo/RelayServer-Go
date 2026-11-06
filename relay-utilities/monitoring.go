// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Gojo Research

package utilities

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	metadataTokenURL = "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token"
	monitoringURL    = "https://monitoring.googleapis.com/v3/projects/"
)

type MonitoringSample struct {
	Snapshot        TelemetrySnapshot
	ClientToSSHMbps float64
	SSHToClientMbps float64
	Timestamp       time.Time
}

type CloudMonitoringExporter struct {
	metadata     InstanceMetadata
	client       *http.Client
	queue        chan MonitoringSample
	done         chan struct{}
	token        string
	tokenExpires time.Time
	lastErrorLog time.Time
	tokenMu      sync.Mutex
}

type monitoringRequest struct {
	TimeSeries []monitoringTimeSeries `json:"timeSeries"`
}

type monitoringTimeSeries struct {
	Metric   monitoringMetric   `json:"metric"`
	Resource monitoringResource `json:"resource"`
	Points   []monitoringPoint  `json:"points"`
}

type monitoringMetric struct {
	Type   string            `json:"type"`
	Labels map[string]string `json:"labels,omitempty"`
}

type monitoringResource struct {
	Type   string            `json:"type"`
	Labels map[string]string `json:"labels"`
}

type monitoringPoint struct {
	Interval monitoringInterval `json:"interval"`
	Value    monitoringValue    `json:"value"`
}

type monitoringInterval struct {
	EndTime time.Time `json:"endTime"`
}

type monitoringValue struct {
	Int64Value  *string  `json:"int64Value,omitempty"`
	DoubleValue *float64 `json:"doubleValue,omitempty"`
}

type metadataToken struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

func NewCloudMonitoringExporter(metadata InstanceMetadata) *CloudMonitoringExporter {
	enabled := strings.ToLower(strings.TrimSpace(os.Getenv("CLOUD_MONITORING_ENABLED")))
	if enabled == "0" || enabled == "false" || enabled == "no" {
		return nil
	}
	if metadata.ProjectID == "" || metadata.Location == "" || metadata.ServiceName == "" ||
		metadata.RevisionName == "" || metadata.ConfigurationName == "" || metadata.InstanceID == "" {
		return nil
	}
	return &CloudMonitoringExporter{
		metadata: metadata,
		client:   &http.Client{Timeout: 5 * time.Second},
		queue:    make(chan MonitoringSample, 1),
		done:     make(chan struct{}),
	}
}

func (e *CloudMonitoringExporter) Start(stopCh <-chan struct{}) {
	go func() {
		defer close(e.done)
		metricsLog := Logger.With("component", "cloud-monitoring")
		for {
			select {
			case <-stopCh:
				return
			case sample := <-e.queue:
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				err := e.write(ctx, sample)
				cancel()
				if err != nil && (e.lastErrorLog.IsZero() || time.Since(e.lastErrorLog) >= time.Minute) {
					metricsLog.Error("custom_metrics_write_failed", "error", err.Error())
					e.lastErrorLog = time.Now()
				}
			}
		}
	}()
}

func (e *CloudMonitoringExporter) Wait() {
	<-e.done
}

func (e *CloudMonitoringExporter) Submit(sample MonitoringSample) {
	select {
	case e.queue <- sample:
	default:
		select {
		case <-e.queue:
		default:
		}
		select {
		case e.queue <- sample:
		default:
		}
	}
}

func (e *CloudMonitoringExporter) write(ctx context.Context, sample MonitoringSample) error {
	token, err := e.accessToken(ctx)
	if err != nil {
		return fmt.Errorf("metadata token: %w", err)
	}
	payload, err := json.Marshal(monitoringRequest{TimeSeries: buildMonitoringTimeSeries(e.metadata, sample)})
	if err != nil {
		return fmt.Errorf("encode time series: %w", err)
	}

	endpoint := monitoringURL + url.PathEscape(e.metadata.ProjectID) + "/timeSeries"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create Monitoring request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("send time series: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("Monitoring API returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func (e *CloudMonitoringExporter) accessToken(ctx context.Context) (string, error) {
	e.tokenMu.Lock()
	defer e.tokenMu.Unlock()
	if e.token != "" && time.Now().Add(time.Minute).Before(e.tokenExpires) {
		return e.token, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataTokenURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := e.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("metadata server returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var token metadataToken
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16*1024)).Decode(&token); err != nil {
		return "", err
	}
	if token.AccessToken == "" {
		return "", fmt.Errorf("metadata server returned an empty access token")
	}
	ttl := time.Duration(token.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	e.token = token.AccessToken
	e.tokenExpires = time.Now().Add(ttl)
	return e.token, nil
}

func buildMonitoringTimeSeries(metadata InstanceMetadata, sample MonitoringSample) []monitoringTimeSeries {
	snapshot := sample.Snapshot
	resource := monitoringResource{
		Type: "cloud_run_revision",
		Labels: map[string]string{
			"project_id":         metadata.ProjectID,
			"location":           metadata.Location,
			"service_name":       metadata.ServiceName,
			"revision_name":      metadata.RevisionName,
			"configuration_name": metadata.ConfigurationName,
		},
	}
	labels := map[string]string{"instance_id": metadata.InstanceID}
	timestamp := sample.Timestamp.UTC()
	if timestamp.IsZero() {
		timestamp = time.Now().UTC()
	}

	int64Value := func(value int64) monitoringValue {
		encoded := strconv.FormatInt(value, 10)
		return monitoringValue{Int64Value: &encoded}
	}
	uint64Value := func(value uint64) monitoringValue {
		encoded := strconv.FormatUint(value, 10)
		return monitoringValue{Int64Value: &encoded}
	}
	doubleValue := func(value float64) monitoringValue {
		encoded := value
		return monitoringValue{DoubleValue: &encoded}
	}

	values := []struct {
		name  string
		value monitoringValue
	}{
		{"active_connections", int64Value(snapshot.ActiveConnections)},
		{"active_handshakes", int64Value(snapshot.ActiveHandshakes)},
		{"active_ssh_sessions", int64Value(snapshot.ActiveSSHSessions)},
		{"total_connections", uint64Value(snapshot.TotalConnections)},
		{"total_ssh_sessions", uint64Value(snapshot.TotalSSHSessions)},
		{"peak_active_ssh_sessions", int64Value(snapshot.PeakActiveSSHSessions)},
		{"failed_handshakes", uint64Value(snapshot.FailedHandshakes)},
		{"failed_ssh_connections", uint64Value(snapshot.FailedSSHConnections)},
		{"session_errors", uint64Value(snapshot.SessionErrors)},
		{"rejected_connections", uint64Value(snapshot.RejectedConns)},
		{"client_to_ssh_bytes_total", uint64Value(snapshot.TotalClientToSSH)},
		{"ssh_to_client_bytes_total", uint64Value(snapshot.TotalSSHToClient)},
		{"client_to_ssh_mbps", doubleValue(sample.ClientToSSHMbps)},
		{"ssh_to_client_mbps", doubleValue(sample.SSHToClientMbps)},
	}

	series := make([]monitoringTimeSeries, 0, len(values))
	for _, item := range values {
		series = append(series, monitoringTimeSeries{
			Metric: monitoringMetric{
				Type:   "custom.googleapis.com/x_relay/" + item.name,
				Labels: labels,
			},
			Resource: resource,
			Points: []monitoringPoint{{
				Interval: monitoringInterval{EndTime: timestamp},
				Value:    item.value,
			}},
		})
	}
	return series
}
