// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Gojo Research

package security

import (
	"sync"
	"testing"
	"time"
)

func TestExtractRealClientIPUsesGFEClientPosition(t *testing.T) {
	headers := []string{"Host: relay.example", "X-Forwarded-For: 203.0.113.99, 198.51.100.20, 192.0.2.1"}
	got := ExtractRealClientIP(headers, "127.0.0.1:8081")
	if want := "198.51.100.20"; got != want {
		t.Fatalf("ExtractRealClientIP() = %q, want %q", got, want)
	}
}

func TestExtractRealClientIPFallsBackWhenXFFIsIncomplete(t *testing.T) {
	got := ExtractRealClientIP([]string{"X-Forwarded-For: 203.0.113.99"}, "127.0.0.1:8081")
	if want := "127.0.0.1"; got != want {
		t.Fatalf("ExtractRealClientIP() = %q, want %q", got, want)
	}
}

func TestGuardDoesNotReleaseRejectedAcquire(t *testing.T) {
	guard := NewIngressGuard(GuardConfig{
		MaxActivePerIP: 1,
		BurstPerWindow: 0,
		WindowDuration: time.Second,
		BanDuration:    time.Second,
	})

	ip, allowed := guard.Acquire("203.0.113.7")
	if !allowed {
		t.Fatal("first Acquire() rejected")
	}
	if _, allowed = guard.Acquire("203.0.113.7"); allowed {
		t.Fatal("second Acquire() unexpectedly allowed")
	}

	guard.Release(ip)
	if _, allowed = guard.Acquire("203.0.113.7"); !allowed {
		t.Fatal("Acquire() remained blocked after the successful lease was released")
	}
}

func TestGuardConcurrentAcquireRelease(t *testing.T) {
	guard := NewIngressGuard(GuardConfig{
		MaxActivePerIP: 64,
		BurstPerWindow: 0,
		WindowDuration: time.Second,
		BanDuration:    time.Second,
	})

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				ip, allowed := guard.Acquire("198.51.100.10")
				if allowed {
					guard.Release(ip)
				}
			}
		}()
	}
	wg.Wait()

	guard.mu.Lock()
	active := guard.buckets["198.51.100.10"].active
	guard.mu.Unlock()
	if active != 0 {
		t.Fatalf("active count = %d, want 0", active)
	}
}
