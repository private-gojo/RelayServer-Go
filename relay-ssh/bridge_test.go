// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Gojo Research

package main

import (
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/private-gojo/RelayServer-Go/relay-security"
)

func TestInspectHandshakeHeadersWebSocketTokens(t *testing.T) {
	headers := []byte("GET / HTTP/1.1\r\nHost: relay.example\r\nConnection: keep-alive, Upgrade\r\nUpgrade: WebSocket\r\n")
	isWS, isHealth, _ := inspectHandshakeHeaders(headers)
	if !isWS || isHealth {
		t.Fatalf("inspectHandshakeHeaders() = (%v, %v), want (true, false)", isWS, isHealth)
	}
}

func TestInspectHandshakeHeadersHealth(t *testing.T) {
	headers := []byte("GET /healthz HTTP/1.1\r\nHost: localhost\r\n")
	isWS, isHealth, _ := inspectHandshakeHeaders(headers)
	if isWS || !isHealth {
		t.Fatalf("inspectHandshakeHeaders() = (%v, %v), want (false, true)", isWS, isHealth)
	}
}

func BenchmarkInspectHandshakeHeaders(b *testing.B) {
	headers := []byte("GET / HTTP/1.1\r\nHost: relay.example\r\nConnection: keep-alive, Upgrade\r\nUpgrade: websocket\r\n")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		inspectHandshakeHeaders(headers)
	}
}

func TestRelayEndReason(t *testing.T) {
	tests := []struct {
		name        string
		clientToSSH string
		sshToClient string
		want        string
	}{
		{name: "normal EOF", clientToSSH: "eof", sshToClient: "eof", want: "relay_complete"},
		{name: "read error", clientToSSH: "read_error", sshToClient: "eof", want: "relay_read_error"},
		{name: "write error", clientToSSH: "eof", sshToClient: "write_error", want: "relay_write_error"},
		{name: "idle timeout is reported", clientToSSH: "idle_timeout", sshToClient: "eof", want: "relay_idle_timeout"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := relayEndReason(test.clientToSSH, test.sshToClient); got != test.want {
				t.Fatalf("relayEndReason() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRelayLegClosesPeerConnectionsOnAbnormalExit(t *testing.T) {
	src, srcPeer := net.Pipe()
	dst, dstPeer := net.Pipe()
	defer srcPeer.Close()
	defer dstPeer.Close()

	result := make(chan string, 1)
	go func() {
		_, reason := relayLeg(src, dst, true)
		result <- reason
	}()

	time.Sleep(20 * time.Millisecond)
	_ = src.Close()

	select {
	case reason := <-result:
		if reason != "peer_leg_closed" && reason != "read_error" {
			t.Fatalf("relayLeg() reason = %q, want a closed-source error", reason)
		}
	case <-time.After(time.Second):
		t.Fatal("relayLeg() did not exit after source connection closed")
	}

	_ = dstPeer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := dstPeer.Read(make([]byte, 1)); err == nil {
		t.Fatal("destination peer remained open after abnormal relay exit")
	}
}

func TestHandleClientReturns502WhenSSHBackendIsUnavailable(t *testing.T) {
	clientConn, bridgeConn := net.Pipe()
	guard := security.NewIngressGuard(security.DefaultGuardConfig())
	done := make(chan struct{})
	go func() {
		handleClient(bridgeConn, "127.0.0.1:1", guard)
		close(done)
	}()

	request := "GET / HTTP/1.1\r\nHost: relay.example\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n"
	if _, err := io.WriteString(clientConn, request); err != nil {
		t.Fatalf("write request: %v", err)
	}
	response, err := io.ReadAll(clientConn)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if !strings.HasPrefix(string(response), "HTTP/1.1 502 Bad Gateway") {
		t.Fatalf("response = %q, want HTTP 502", response)
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handleClient() did not exit")
	}
}
