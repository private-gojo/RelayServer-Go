// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Gojo Research

package security

import (
	"sync"
	"testing"
	"time"
)

func TestExtractRealClientIPUsesRightmostCloudRunAddress(t *testing.T) {
	headers := []string{"Host: relay.example", "X-Forwarded-For: 203.0.113.99, 198.51.100.20"}
	got := ExtractRealClientIP(headers, "127.0.0.1:8081")
	if want := "198.51.100.20"; got != want {
		t.Fatalf("ExtractRealClientIP() = %q, want %q", got, want)
	}
}

func TestExtractRealClientIPIgnoresUntrustedProxyHeaders(t *testing.T) {
	headers := []string{
		"CF-Connecting-IP: 203.0.113.44",
		"True-Client-IP: 203.0.113.45",
	}
	if got := ExtractRealClientIP(headers, "127.0.0.1:8081"); got != "" {
		t.Fatalf("ExtractRealClientIP() = %q, want empty", got)
	}
}

func TestExtractRealClientIPAcceptsSingleForwardedAddress(t *testing.T) {
	got := ExtractRealClientIP([]string{"X-Forwarded-For: 198.51.100.9"}, "127.0.0.1:8081")
	if want := "198.51.100.9"; got != want {
		t.Fatalf("ExtractRealClientIP() = %q, want %q", got, want)
	}
}

func TestExtractRealClientIPUsesOnlyPublicPeerFallback(t *testing.T) {
	if got := ExtractRealClientIP(nil, "127.0.0.1:8081"); got != "" {
		t.Fatalf("loopback fallback = %q, want empty", got)
	}
	if got := ExtractRealClientIP(nil, "203.0.113.7:443"); got != "203.0.113.7" {
		t.Fatalf("public peer fallback = %q, want 203.0.113.7", got)
	}
}

func TestGuardSkipsPerIPBucketWhenClientIPIsUnavailable(t *testing.T) {
	guard := NewIngressGuard(DefaultGuardConfig())

	ip, allowed := guard.Acquire("")
	if !allowed || ip != "" {
		t.Fatalf("Acquire(empty) = (%q, %v), want (empty, true)", ip, allowed)
	}

	guard.Release(ip)
	guard.mu.Lock()
	bucketCount := len(guard.buckets)
	guard.mu.Unlock()
	if bucketCount != 0 {
		t.Fatalf("unknown client created %d shared bucket(s)", bucketCount)
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

func TestGuardActiveLimitDoesNotConsumeBurstBudget(t *testing.T) {
	guard := NewIngressGuard(GuardConfig{
		MaxActivePerIP: 1,
		BurstPerWindow: 1,
		WindowDuration: time.Minute,
		BanDuration:    time.Minute,
	})

	ip, allowed := guard.Acquire("203.0.113.8")
	if !allowed {
		t.Fatal("first Acquire() rejected")
	}
	if _, allowed = guard.Acquire("203.0.113.8"); allowed {
		t.Fatal("second Acquire() unexpectedly allowed")
	}

	guard.Release(ip)
	if _, allowed = guard.Acquire("203.0.113.8"); !allowed {
		t.Fatal("active-limit rejection consumed burst budget and caused a ban")
	}
}
