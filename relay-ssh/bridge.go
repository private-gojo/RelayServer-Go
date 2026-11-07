// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Gojo Research

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/private-gojo/RelayServer-Go/relay-security"
	"github.com/private-gojo/RelayServer-Go/relay-utilities"
)

const (
	defaultSSHAddr   = "127.0.0.1:2224"
	bufSize          = 256 * 1024
	idleTimeout      = 600 * time.Second
	handshakeTimeout = 4 * time.Second
	maxHeaderSize    = 16 * 1024
	shutdownGrace    = 7 * time.Second
)

var bridgeLog = slog.Default().With("component", "go-bridge")

var bufferPool = sync.Pool{
	New: func() any {
		buffer := make([]byte, bufSize)
		return &buffer
	},
}

var trackedConns sync.Map

type sessionStats struct {
	clientToSSHBytes uint64
	sshToClientBytes uint64
	clientToSSHEnd   string
	sshToClientEnd   string
	handshakeMs      string
	sshConnectMs     string
	endReason        string
}

func trackConn(conn net.Conn) {
	trackedConns.Store(conn, struct{}{})
}

func untrackConn(conn net.Conn) {
	trackedConns.Delete(conn)
}

func closeTrackedConns() {
	trackedConns.Range(func(key, _ any) bool {
		if conn, ok := key.(net.Conn); ok {
			_ = conn.Close()
		}
		return true
	})
}

func relayLeg(src net.Conn, dst net.Conn, isClientToSSH bool) (uint64, string) {
	bufferPointer := bufferPool.Get().(*[]byte)
	buffer := *bufferPointer
	defer bufferPool.Put(bufferPointer)

	var totalBytes uint64
	endReason := "eof"

	for {
		_ = src.SetReadDeadline(time.Now().Add(idleTimeout))
		n, readErr := src.Read(buffer)
		if n > 0 {
			_ = dst.SetWriteDeadline(time.Now().Add(idleTimeout))
			written, writeErr := dst.Write(buffer[:n])
			if written > 0 {
				count := uint64(written)
				totalBytes += count
				if isClientToSSH {
					utilities.Counters.TotalClientToSSH.Add(count)
				} else {
					utilities.Counters.TotalSSHToClient.Add(count)
				}
			}
			if writeErr != nil {
				switch {
				case errors.Is(writeErr, net.ErrClosed):
					endReason = "peer_leg_closed"
				case isTimeoutError(writeErr):
					endReason = "idle_timeout"
				default:
					endReason = "write_error"
				}
				break
			}
		}
		if readErr != nil {
			switch {
			case errors.Is(readErr, io.EOF):
				endReason = "eof"
			case errors.Is(readErr, net.ErrClosed):
				endReason = "peer_leg_closed"
			case isTimeoutError(readErr):
				endReason = "idle_timeout"
			default:
				endReason = "read_error"
			}
			break
		}
	}

	if endReason == "eof" {
		if tcpDestination, ok := dst.(*net.TCPConn); ok {
			_ = tcpDestination.CloseWrite()
		} else {
			_ = dst.Close()
		}
	} else {
		_ = src.Close()
		_ = dst.Close()
	}

	return totalBytes, endReason
}

func isTimeoutError(err error) bool {
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}

func inspectHandshakeHeaders(headerBlock []byte) (isWSUpgrade bool, isHealthCheck bool, headerLines []string) {
	lines := strings.Split(string(headerBlock), "\r\n")
	if len(lines) == 0 {
		return false, false, nil
	}

	requestParts := strings.Fields(lines[0])
	if len(requestParts) >= 2 && (requestParts[1] == "/healthz" || requestParts[1] == "/health") {
		return false, true, lines[1:]
	}

	for _, line := range lines[1:] {
		name, value, found := strings.Cut(line, ":")
		if !found || !strings.EqualFold(strings.TrimSpace(name), "upgrade") {
			continue
		}
		for _, token := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), "websocket") {
				isWSUpgrade = true
				break
			}
		}
	}

	return isWSUpgrade, false, lines[1:]
}

func writeHTTPResponse(conn net.Conn, response string) error {
	_, err := io.WriteString(conn, response)
	return err
}

func relayEndReason(clientToSSH, sshToClient string) string {
	for _, reason := range []string{clientToSSH, sshToClient} {
		if reason == "read_error" || reason == "write_error" {
			return "relay_" + reason
		}
	}
	for _, reason := range []string{clientToSSH, sshToClient} {
		if reason == "idle_timeout" {
			return "relay_idle_timeout"
		}
	}
	return "relay_complete"
}

func isSessionError(reason string) bool {
	switch reason {
	case "relay_complete", "relay_idle_timeout", "healthcheck_200", "client_closed_during_handshake",
		"security_rate_limited", "peer_leg_closed", "http_403_no_websocket_upgrade":
		return false
	default:
		return true
	}
}

func handleClient(clientConn net.Conn, sshAddr string, guard *security.IngressGuard) {
	trackConn(clientConn)
	defer func() {
		untrackConn(clientConn)
		_ = clientConn.Close()
	}()

	sessionID := utilities.Counters.ConnectionStarted()
	stats := sessionStats{
		clientToSSHEnd: "not_started",
		sshToClientEnd: "not_started",
		handshakeMs:    "None",
		sshConnectMs:   "None",
		endReason:      "handler_exit",
	}
	started := time.Now()
	peerAddr := clientConn.RemoteAddr().String()
	peerIP := security.ExtractRealClientIP(nil, peerAddr)
	guardAcquired := false
	handshakePending := true
	activeSSHSession := false

	finishHandshake := func(success bool) {
		if !handshakePending {
			return
		}
		handshakePending = false
		utilities.Counters.HandshakeFinished(success)
	}

	cancelHandshake := func() {
		if !handshakePending {
			return
		}
		handshakePending = false
		utilities.Counters.HandshakeCancelled()
	}

	defer func() {
		finishHandshake(false)
		if activeSSHSession {
			utilities.Counters.SSHSessionFinished()
		}
		if guardAcquired {
			guard.Release(peerIP)
		}
		utilities.Counters.ConnectionFinished()

		if isSessionError(stats.endReason) {
			utilities.Counters.SessionErrors.Add(1)
			bridgeLog.Error("session_failed",
				"session_id", sessionID,
				"reason", stats.endReason,
				"peer_ip", peerIP,
			)
		}

		durationMs := float64(time.Since(started).Microseconds()) / 1000
		logIP := peerIP
		if logIP == "" {
			logIP = "unknown"
		}
		bridgeLog.Info("session_closed",
			"session_id", sessionID,
			"duration_ms", durationMs,
			"handshake_ms", stats.handshakeMs,
			"ssh_connect_ms", stats.sshConnectMs,
			"peer_ip", logIP,
			"client_to_ssh_bytes", stats.clientToSSHBytes,
			"ssh_to_client_bytes", stats.sshToClientBytes,
			"client_to_ssh_end", stats.clientToSSHEnd,
			"ssh_to_client_end", stats.sshToClientEnd,
			"reason", stats.endReason,
		)
	}()

	handshakeStarted := time.Now()
	_ = clientConn.SetDeadline(handshakeStarted.Add(handshakeTimeout))

	raw := make([]byte, 0, 4096)
	temporary := make([]byte, 4096)
	headerEnd := -1

	for headerEnd < 0 {
		n, readErr := clientConn.Read(temporary)
		if n > 0 {
			raw = append(raw, temporary[:n]...)
			if index := bytes.Index(raw, []byte("\r\n\r\n")); index >= 0 {
				headerEnd = index
				break
			}
			if len(raw) > maxHeaderSize {
				stats.endReason = "handshake_header_too_large"
				return
			}
		}
		if readErr != nil {
			switch {
			case errors.Is(readErr, io.EOF) && len(raw) == 0:
				cancelHandshake()
				stats.endReason = "client_closed_during_handshake"
			case isTimeoutError(readErr):
				stats.endReason = "handshake_timeout"
				bridgeLog.Warn("handshake_read_timeout", "session_id", sessionID)
			default:
				stats.endReason = "handshake_error"
				bridgeLog.Error("handshake_read_failed", "session_id", sessionID, "error", readErr.Error())
			}
			return
		}
	}

	headerEnd += 4
	stats.handshakeMs = fmt.Sprintf("%.1f", float64(time.Since(handshakeStarted).Microseconds())/1000)
	if headerEnd > maxHeaderSize {
		stats.endReason = "handshake_header_too_large"
		return
	}

	headerBlock := raw[:headerEnd-4]
	leftover := raw[headerEnd:]
	isWSUpgrade, isHealthCheck, headerLines := inspectHandshakeHeaders(headerBlock)

	if isHealthCheck {
		finishHandshake(true)
		stats.endReason = "healthcheck_200"
		_ = writeHTTPResponse(clientConn,
			"HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 3\r\nConnection: close\r\n\r\nOK\n")
		return
	}

	peerIP = security.ExtractRealClientIP(headerLines, peerAddr)
	if peerIP == "" {
		peerIP = "unknown"
	}
	bridgeLog.Info("connection_received",
		"session_id", sessionID,
		"peer_addr", peerAddr,
		"peer_ip", peerIP,
	)

	var allowed bool
	peerIP, allowed = guard.Acquire(peerIP)
	if !allowed {
		cancelHandshake()
		utilities.Counters.RejectedConns.Add(1)
		stats.endReason = "security_rate_limited"
		bridgeLog.Warn("connection_rejected", "session_id", sessionID, "peer_ip", peerIP, "component_reason", "ingress_guard")
		return
	}
	guardAcquired = peerIP != ""

	if !isWSUpgrade {
		finishHandshake(false)
		stats.endReason = "http_403_no_websocket_upgrade"
		bridgeLog.Warn("websocket_upgrade_missing", "session_id", sessionID, "peer_ip", peerIP)
		_ = writeHTTPResponse(clientConn,
			"HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return
	}

	sshConnectStarted := time.Now()
	sshConn, dialErr := net.DialTimeout("tcp", sshAddr, 2*time.Second)
	stats.sshConnectMs = fmt.Sprintf("%.1f", float64(time.Since(sshConnectStarted).Microseconds())/1000)
	if dialErr != nil {
		utilities.Counters.FailedSSHConnections.Add(1)
		stats.endReason = "ssh_connect_error"
		bridgeLog.Error("ssh_backend_connect_failed",
			"session_id", sessionID,
			"backend", sshAddr,
			"error", dialErr.Error(),
		)
		_ = writeHTTPResponse(clientConn,
			"HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return
	}

	trackConn(sshConn)
	defer func() {
		untrackConn(sshConn)
		_ = sshConn.Close()
	}()

	response := fmt.Sprintf(
		"HTTP/1.1 101 Switching Protocols\r\n"+
			"Upgrade: websocket\r\n"+
			"Connection: keep-alive, Upgrade\r\n"+
			"Date: %s\r\n"+
			"Server: Google Frontend\r\n"+
			"X-Powered-By: RelayServer-Go\r\n"+
			"X-Origin-To: X-Sorcerer\r\n"+
			"X-Source-Code: https://github.com/private-gojo/RelayServer-Go\r\n\r\n",
		security.FormatHTTPDate(),
	)
	if err := writeHTTPResponse(clientConn, response); err != nil {
		stats.endReason = "handshake_write_error"
		return
	}
	finishHandshake(true)

	_ = clientConn.SetDeadline(time.Time{})

	utilities.Counters.SSHSessionStarted()
	activeSSHSession = true

	utilities.OptimizeSocket(clientConn)
	utilities.OptimizeSocket(sshConn)

	if len(leftover) > 0 {
		_ = sshConn.SetWriteDeadline(time.Now().Add(idleTimeout))
		written, writeErr := io.Copy(sshConn, bytes.NewReader(leftover))
		if written > 0 {
			count := uint64(written)
			stats.clientToSSHBytes += count
			utilities.Counters.TotalClientToSSH.Add(count)
		}
		if writeErr != nil {
			stats.endReason = "ssh_leftover_write_error"
			bridgeLog.Error("ssh_initial_payload_failed", "session_id", sessionID, "error", writeErr.Error())
			return
		}
	}

	var waitGroup sync.WaitGroup
	waitGroup.Add(2)

	var clientToSSHBytes, sshToClientBytes atomic.Uint64
	var clientToSSHEnd, sshToClientEnd atomic.Value

	go func() {
		defer waitGroup.Done()
		count, reason := relayLeg(clientConn, sshConn, true)
		clientToSSHBytes.Store(count)
		clientToSSHEnd.Store(reason)
	}()

	go func() {
		defer waitGroup.Done()
		count, reason := relayLeg(sshConn, clientConn, false)
		sshToClientBytes.Store(count)
		sshToClientEnd.Store(reason)
	}()

	waitGroup.Wait()

	stats.clientToSSHBytes += clientToSSHBytes.Load()
	stats.sshToClientBytes += sshToClientBytes.Load()
	if reason, ok := clientToSSHEnd.Load().(string); ok {
		stats.clientToSSHEnd = reason
	}
	if reason, ok := sshToClientEnd.Load().(string); ok {
		stats.sshToClientEnd = reason
	}
	stats.endReason = relayEndReason(stats.clientToSSHEnd, stats.sshToClientEnd)
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func main() {
	metadata := utilities.LoadInstanceMetadata()
	utilities.ConfigureLogger(metadata)
	bridgeLog = utilities.Logger.With("component", "go-bridge")

	numCPU := runtime.NumCPU()
	maxProcs := runtime.GOMAXPROCS(0)

	listenPort := envOrDefault("BRIDGE_PORT", "")
	listenAddr := envOrDefault("BRIDGE_LISTEN_ADDR", "")
	if listenAddr == "" {
		if listenPort != "" {
			listenAddr = "0.0.0.0:" + listenPort
		} else if cloudRunPort := envOrDefault("PORT", ""); cloudRunPort != "" && os.Getenv("DIRECT_INGRESS") == "1" {
			listenAddr = "0.0.0.0:" + cloudRunPort
		} else {
			listenAddr = "127.0.0.1:8081"
		}
	}

	sshAddr := envOrDefault("SSH_BACKEND_ADDR", defaultSSHAddr)
	guard := security.NewIngressGuard(security.DefaultGuardConfig())

	bridgeLog.Info("runtime_started", "go_version", runtime.Version(), "online_cpus", numCPU, "gomaxprocs", maxProcs)
	bridgeLog.Info("bridge_configuration",
		"listen_addr", listenAddr,
		"ssh_backend", sshAddr,
		"buffer_size", bufSize,
		"idle_timeout", idleTimeout.String(),
		"handshake_timeout", handshakeTimeout.String(),
	)
	bridgeLog.Info("ingress_guard_configuration",
		"max_active_per_ip", security.DefaultGuardConfig().MaxActivePerIP,
		"burst_per_window", security.DefaultGuardConfig().BurstPerWindow,
		"window", security.DefaultGuardConfig().WindowDuration.String(),
		"ban", security.DefaultGuardConfig().BanDuration.String(),
	)

	stopMetrics := make(chan struct{})
	exporter := utilities.NewCloudMonitoringExporter(metadata)
	if exporter != nil {
		exporter.Start(stopMetrics)
		bridgeLog.Info("cloud_monitoring_exporter_started")
	} else {
		bridgeLog.Info("cloud_monitoring_exporter_disabled_or_unavailable")
	}
	go utilities.StartMetricsLoop(stopMetrics, exporter)

	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		bridgeLog.Error("bridge_bind_failed", "listen_addr", listenAddr, "error", err.Error())
		close(stopMetrics)
		if exporter != nil {
			exporter.Wait()
		}
		os.Exit(1)
	}
	bridgeLog.Info("bridge_ready", "listen_addr", listenAddr, "ssh_backend", sshAddr)

	signalChannel := make(chan os.Signal, 1)
	signal.Notify(signalChannel, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-signalChannel
		bridgeLog.Info("shutdown_signal_received", "signal", sig.String())
		_ = listener.Close()
	}()

	var sessions sync.WaitGroup
	for {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			if errors.Is(acceptErr, net.ErrClosed) {
				break
			}
			bridgeLog.Warn("listener_accept_failed", "error", acceptErr.Error())
			time.Sleep(50 * time.Millisecond)
			continue
		}
		sessions.Add(1)
		go func(connection net.Conn) {
			defer sessions.Done()
			handleClient(connection, sshAddr, guard)
		}(conn)
	}

	signal.Stop(signalChannel)
	close(stopMetrics)
	bridgeLog.Info("shutdown_waiting_for_sessions", "grace_period", shutdownGrace.String())

	done := make(chan struct{})
	go func() {
		sessions.Wait()
		close(done)
	}()

	timer := time.NewTimer(shutdownGrace)
	defer timer.Stop()
	select {
	case <-done:
		bridgeLog.Info("shutdown_sessions_drained")
	case <-timer.C:
		bridgeLog.Warn("shutdown_grace_expired")
		closeTrackedConns()
		select {
		case <-done:
			bridgeLog.Info("shutdown_sessions_stopped")
		case <-time.After(2 * time.Second):
			bridgeLog.Error("shutdown_sessions_still_active")
		}
	}
	if exporter != nil {
		exporter.Wait()
	}
	bridgeLog.Info("bridge_stopped")
}
