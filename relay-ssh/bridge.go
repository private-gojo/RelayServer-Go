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
	defaultListenAddr = "0.0.0.0:8080"
	defaultSSHAddr    = "127.0.0.1:2224"
	bufSize           = 65536
	idleTimeout       = 600 * time.Second
	handshakeTimeout  = 4 * time.Second
	maxHeaderSize     = 16384
)

var bufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, bufSize)
		return &b
	},
}

type sessionStats struct {
	clientToSSHBytes uint64
	sshToClientBytes uint64
	clientToSSHEnd   string
	sshToClientEnd   string
	handshakeMs      string
	sshConnectMs     string
	endReason        string
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
			if errors.Is(err, io.EOF) {
				endReason = "eof"
			} else if isTimeoutError(err) {
				endReason = "idle_timeout"
			} else {
				endReason = "read_error"
			}
			break
		}
	}

	// Half-close the destination write side so reverse traffic can finish cleanly (exact proxy.py write_eof behavior).
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

// inspectHandshakeHeaders parses HTTP headers case-insensitively and checks for Upgrade: websocket tokens
// or /healthz probe requests.
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
		if !found {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(name), "upgrade") {
			for _, token := range strings.Split(value, ",") {
				if strings.EqualFold(strings.TrimSpace(token), "websocket") {
					isWSUpgrade = true
					break
				}
			}
		}
	}

	return isWSUpgrade, false, lines[1:]
}

func handleClient(clientConn net.Conn, sshAddr string, guard *security.IngressGuard) {
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
	peerIP, allowed := guard.Acquire(peerAddr)

	defer func() {
		guard.Release(peerIP)
		_ = clientConn.Close()
		utilities.Counters.ActiveSessions.Add(-1)
		durationMs := float64(time.Since(started).Microseconds()) / 1000.0
		log.Printf(
			"[SESSION %d] closed duration_ms=%.1f handshake_ms=%s ssh_connect_ms=%s "+
				"client_to_ssh_bytes=%d ssh_to_client_bytes=%d "+
				"client_to_ssh_end=%s ssh_to_client_end=%s reason=%s",
			sessionID,
			durationMs,
			stats.handshakeMs,
			stats.sshConnectMs,
			stats.clientToSSHBytes,
			stats.sshToClientBytes,
			stats.clientToSSHEnd,
			stats.sshToClientEnd,
			stats.endReason,
		)
	}()

	if !allowed {
		utilities.Counters.RejectedConns.Add(1)
		stats.endReason = "security_rate_limited"
		return
	}

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
			if errors.Is(err, io.EOF) && len(raw) == 0 {
				stats.endReason = "client_closed_during_handshake"
			} else if isTimeoutError(err) {
				stats.endReason = "handshake_timeout"
				log.Printf("[SESSION %d] Handshake read timed out, dropping", sessionID)
			} else {
				stats.endReason = "handshake_error"
				log.Printf("[SESSION %d] Handshake error: %v", sessionID, err)
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
	realIP := security.ExtractRealClientIP(headerLines, peerIP)
	log.Printf("[SESSION %d] New connection from %s (real_ip=%s)", sessionID, peerAddr, realIP)

	if isHealthCheck {
		stats.endReason = "healthcheck_200"
		_, _ = clientConn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 3\r\nConnection: close\r\n\r\nOK\n"))
		return
	}

	if isWSUpgrade {
		resp := fmt.Sprintf(
			"HTTP/1.1 101 Switching Protocols\r\n"+
				"Upgrade: websocket\r\n"+
				"Connection: Upgrade\r\n"+
				"Date: %s\r\n"+
				"Server: cloudflare\r\n\r\n",
			security.FormatHTTPDate(),
		)
		if _, err := clientConn.Write([]byte(resp)); err != nil {
			stats.endReason = "handshake_write_error"
			return
		}
	} else {
		stats.endReason = "http_403_no_websocket_upgrade"
		log.Printf("[SESSION %d] No websocket upgrade header found, sending 403 and dropping", sessionID)
		_, _ = clientConn.Write([]byte(
			"HTTP/1.1 403 Forbidden\r\n" +
				"Content-Type: text/plain\r\n" +
				"Content-Length: 9\r\n" +
				"Connection: close\r\n\r\n" +
				"Forbidden",
		))
		return
	}

	// Clear handshake deadline before entering tunnel phase
	_ = clientConn.SetDeadline(time.Time{})

	sshConnectStarted := time.Now()
	sshConn, err := net.DialTimeout("tcp", sshAddr, 5*time.Second)
	stats.sshConnectMs = fmt.Sprintf("%.1f", float64(time.Since(sshConnectStarted).Microseconds())/1000.0)
	if err != nil {
		stats.endReason = "ssh_connect_error"
		log.Printf("[SESSION %d] Could not reach dropbear on %s: %v", sessionID, sshAddr, err)
		return
	}
	defer sshConn.Close()

	utilities.OptimizeSocket(clientConn)
	utilities.OptimizeSocket(sshConn)

	// Preserve pipelined SSH client identification bytes sent immediately after \r\n\r\n
	if len(leftover) > 0 {
		_ = sshConn.SetWriteDeadline(time.Now().Add(idleTimeout))
		written, err := sshConn.Write(leftover)
		if written > 0 {
			u := uint64(written)
			stats.clientToSSHBytes += u
			utilities.Counters.TotalClientToSSH.Add(u)
		}
		if err != nil {
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
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetPrefix("[X-RELAY-BRIDGE] ")

	// Enable full multi-core parallelism across all available vCPUs
	numCPU := runtime.NumCPU()
	runtime.GOMAXPROCS(numCPU)

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

	stopMetrics := make(chan struct{})
	go utilities.StartMetricsLoop(stopMetrics)

	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("Failed to bind %s: %v", listenAddr, err)
	}
	log.Printf("Go multi-core bridge online (GOMAXPROCS=%d) listening on %s -> forwarding to %s", numCPU, listenAddr, sshAddr)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		log.Printf("Received %v, initiating graceful shutdown...", sig)
		close(stopMetrics)
		_ = listener.Close()
		os.Exit(0)
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("Accept error: %v", err)
			time.Sleep(50 * time.Millisecond)
			continue
		}
		go handleClient(conn, sshAddr, guard)
	}
}
