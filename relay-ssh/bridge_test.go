// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Gojo Research

package main

import "testing"

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
