// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Gojo Research

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
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

var bufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, bufSize)
		return &b
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
	bufPtr := bufferPool.Get().(*[]byte)
	buf := *bufPtr
	defer bufferPool.Put(bufPtr)

	var totalBytes uint64
	endReason := "eof"

	for {
		_ = src.SetReadDeadline(time.Now().Add(idleTimeout))
		n, err := src.Read(buf)
		if n > 0 {
			_ = dst.SetWriteDeadline(time.Now().Add(idleTimeout))
			written, wErr := dst.Write(buf[:n])
			if written > 0 {
				u := uint64(written)
				totalBytes += u
				if isClientToSSH {
					utilities.Counters.TotalClientToSSH.Add(u)
				} else {
					utilities.Counters.TotalSSHToClient.Add(u)
				}
			}
			if wErr != nil {
				if isTimeoutError(wErr) {
					endReason = "idle_timeout"
				} else {
					endReason = "write_error"
				}
				break
			}
		}
		if err != nil {
			switch {
			case errors.Is(err, io.EOF):
				endReason = "eof"
			case isTimeoutError(err):
				endReason = "idle_timeout"
			default:
				endReason = "read_error"
			}
			break
		}
	}

	if tcpDst, ok := dst.(*net.TCPConn); ok {
		_ = tcpDst.CloseWrite()
	} else {
		_ = dst.Close()
	}

	return totalBytes, endReason
}

func isTimeoutError(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func inspectHandshakeHeaders(headerBlock []byte) (isWSUpgrade bool, isHealthCheck bool, headerLines []string) {
	lines := strings.Split(string(headerBlock), "\r\n")
	if len(lines) == 0 {
		return false, false, nil
	}

	reqParts := strings.Fields(lines[0])
	if len(reqParts) >= 2 && (reqParts[1] == "/healthz" || reqParts[1] == "/health") {
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

func handleClient(clientConn net.Conn, sshAddr string, guard *security.IngressGuard) {
	trackConn(clientConn)
	defer func() {
		untrackConn(clientConn)
		_ = clientConn.Close()
	}()

	sessionID := utilities.Counters.SessionSeq.Add(1)
	utilities.Counters.ActiveSessions.Add(1)
	utilities.Counters.TotalSessions.Add(1)

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

	defer func() {
		if guardAcquired {
			guard.Release(peerIP)
		}
		utilities.Counters.ActiveSessions.Add(-1)
		durationMs := float64(time.Since(started).Microseconds()) / 1000.0
		log.Printf(
			"[SESSION %d] closed duration_ms=%.1f handshake_ms=%s ssh_connect_ms=%s "+
				"peer_ip=%s client_to_ssh_bytes=%d ssh_to_client_bytes=%d "+
				"client_to_ssh_end=%s ssh_to_client_end=%s reason=%s",
			sessionID,
			durationMs,
			stats.handshakeMs,
			stats.sshConnectMs,
			peerIP,
			stats.clientToSSHBytes,
			stats.sshToClientBytes,
			stats.clientToSSHEnd,
			stats.sshToClientEnd,
			stats.endReason,
		)
	}()

	handshakeStarted := time.Now()
	_ = clientConn.SetDeadline(handshakeStarted.Add(handshakeTimeout))

	raw := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	headerEnd := -1

	for headerEnd < 0 {
		n, err := clientConn.Read(tmp)
		if n > 0 {
			raw = append(raw, tmp[:n]...)
			if idx := bytes.Index(raw, []byte("\r\n\r\n")); idx >= 0 {
				headerEnd = idx
				break
			}
			if len(raw) > maxHeaderSize {
				stats.endReason = "handshake_header_too_large"
				return
			}
		}
		if err != nil {
			switch {
			case errors.Is(err, io.EOF) && len(raw) == 0:
				stats.endReason = "client_closed_during_handshake"
			case isTimeoutError(err):
				stats.endReason = "handshake_timeout"
				log.Printf("[SESSION %d] Handshake read timed out, dropping", sessionID)
			default:
				stats.endReason = "handshake_error"
				log.Printf("[SESSION %d] Handshake read failed: %v", sessionID, err)
			}
			return
		}
	}

	headerEnd += 4
	stats.handshakeMs = fmt.Sprintf("%.1f", float64(time.Since(handshakeStarted).Microseconds())/1000.0)
	if headerEnd > maxHeaderSize {
		stats.endReason = "handshake_header_too_large"
		return
	}

	headerBlock := raw[:headerEnd-4]
	leftover := raw[headerEnd:]
	isWSUpgrade, isHealthCheck, headerLines := inspectHandshakeHeaders(headerBlock)

	if isHealthCheck {
		stats.endReason = "healthcheck_200"
		_ = writeHTTPResponse(clientConn,
			"HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 3\r\nConnection: close\r\n\r\nOK\n")
		return
	}

	peerIP = security.ExtractRealClientIP(headerLines, peerAddr)
	log.Printf("[SESSION %d] New connection peer=%s real_ip=%s", sessionID, peerAddr, peerIP)

	var allowed bool
	peerIP, allowed = guard.Acquire(peerIP)
	if !allowed {
		utilities.Counters.RejectedConns.Add(1)
		stats.endReason = "security_rate_limited"
		log.Printf("[SESSION %d] Rejected real_ip=%s by ingress guard", sessionID, peerIP)
		return
	}
	guardAcquired = peerIP != ""

	if !isWSUpgrade {
		stats.endReason = "http_403_no_websocket_upgrade"
		log.Printf("[SESSION %d] No websocket upgrade header found, sending 403", sessionID)
		_ = writeHTTPResponse(clientConn,
			"HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return
	}

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

	_ = clientConn.SetDeadline(time.Time{})

	sshConnectStarted := time.Now()
	sshConn, err := net.DialTimeout("tcp", sshAddr, 5*time.Second)
	stats.sshConnectMs = fmt.Sprintf("%.1f", float64(time.Since(sshConnectStarted).Microseconds())/1000.0)
	if err != nil {
		stats.endReason = "ssh_connect_error"
		log.Printf("[SESSION %d] Could not reach Dropbear on %s: %v", sessionID, sshAddr, err)
		return
	}
	trackConn(sshConn)
	defer func() {
		untrackConn(sshConn)
		_ = sshConn.Close()
	}()

	utilities.OptimizeSocket(clientConn)
	utilities.OptimizeSocket(sshConn)

	if len(leftover) > 0 {
		_ = sshConn.SetWriteDeadline(time.Now().Add(idleTimeout))
		written, writeErr := io.Copy(sshConn, bytes.NewReader(leftover))
		if written > 0 {
			u := uint64(written)
			stats.clientToSSHBytes += u
			utilities.Counters.TotalClientToSSH.Add(u)
		}
		if writeErr != nil {
			stats.endReason = "ssh_leftover_write_error"
			return
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)

	var c2sBytes, s2cBytes atomic.Uint64
	var c2sEnd, s2cEnd atomic.Value

	go func() {
		defer wg.Done()
		b, reason := relayLeg(clientConn, sshConn, true)
		c2sBytes.Store(b)
		c2sEnd.Store(reason)
	}()

	go func() {
		defer wg.Done()
		b, reason := relayLeg(sshConn, clientConn, false)
		s2cBytes.Store(b)
		s2cEnd.Store(reason)
	}()

	wg.Wait()

	stats.clientToSSHBytes += c2sBytes.Load()
	stats.sshToClientBytes += s2cBytes.Load()
	if v, ok := c2sEnd.Load().(string); ok {
		stats.clientToSSHEnd = v
	}
	if v, ok := s2cEnd.Load().(string); ok {
		stats.sshToClientEnd = v
	}
	stats.endReason = "relay_complete"
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetPrefix("[X-RELAY-BRIDGE] ")

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

	log.Printf("[BOOT] runtime=%s online_cpus=%d gomaxprocs=%d", runtime.Version(), numCPU, maxProcs)
	log.Printf("[BOOT] bridge_listen=%s ssh_backend=%s buffer_size=%d idle_timeout=%s handshake_timeout=%s",
		listenAddr, sshAddr, bufSize, idleTimeout, handshakeTimeout)
	log.Printf("[BOOT] ingress_guard max_active_per_ip=%d burst_per_window=%d window=%s ban=%s",
		security.DefaultGuardConfig().MaxActivePerIP,
		security.DefaultGuardConfig().BurstPerWindow,
		security.DefaultGuardConfig().WindowDuration,
		security.DefaultGuardConfig().BanDuration)

	stopMetrics := make(chan struct{})
	go utilities.StartMetricsLoop(stopMetrics)

	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("Failed to bind %s: %v", listenAddr, err)
	}
	log.Printf("[BOOT] Go bridge ready; listening on %s -> %s", listenAddr, sshAddr)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		log.Printf("[SHUTDOWN] received %v; closing listener", sig)
		_ = listener.Close()
	}()

	var sessions sync.WaitGroup
	for {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			if errors.Is(acceptErr, net.ErrClosed) {
				break
			}
			log.Printf("[WARN] accept failed: %v", acceptErr)
			time.Sleep(50 * time.Millisecond)
			continue
		}
		sessions.Add(1)
		go func(conn net.Conn) {
			defer sessions.Done()
			handleClient(conn, sshAddr, guard)
		}(conn)
	}

	signal.Stop(sigCh)
	close(stopMetrics)
	log.Printf("[SHUTDOWN] waiting up to %s for active sessions", shutdownGrace)

	done := make(chan struct{})
	go func() {
		sessions.Wait()
		close(done)
	}()

	timer := time.NewTimer(shutdownGrace)
	defer timer.Stop()
	select {
	case <-done:
		log.Printf("[SHUTDOWN] all sessions closed cleanly")
	case <-timer.C:
		log.Printf("[SHUTDOWN] grace period expired; closing tracked connections")
		closeTrackedConns()
		select {
		case <-done:
			log.Printf("[SHUTDOWN] active sessions stopped")
		case <-time.After(2 * time.Second):
			log.Printf("[SHUTDOWN] some sessions did not stop within the final timeout")
		}
	}
	log.Printf("[SHUTDOWN] bridge stopped")
}
