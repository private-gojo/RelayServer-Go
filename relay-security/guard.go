// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Gojo Research

package security

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type GuardConfig struct {
	MaxActivePerIP   int
	BurstPerWindow   int
	WindowDuration   time.Duration
	BanDuration      time.Duration
	MaxHandshakeSize int
	HandshakeTimeout time.Duration
}

func DefaultGuardConfig() GuardConfig {
	return GuardConfig{
		MaxActivePerIP:   1024,
		BurstPerWindow:   5000,
		WindowDuration:   10 * time.Second,
		BanDuration:      30 * time.Second,
		MaxHandshakeSize: 16 * 1024,
		HandshakeTimeout: 4 * time.Second,
	}
}

type ipBucket struct {
	active      int
	windowStart time.Time
	hits        int
	bannedUntil time.Time
}

type IngressGuard struct {
	cfg     GuardConfig
	mu      sync.Mutex
	buckets map[string]*ipBucket
}

func NewIngressGuard(cfg GuardConfig) *IngressGuard {
	g := &IngressGuard{
		cfg:     cfg,
		buckets: make(map[string]*ipBucket),
	}
	go g.cleanupLoop()
	return g
}

func (g *IngressGuard) Acquire(peerIP string) (string, bool) {
	ip := normalizeIP(peerIP)
	if ip == "" {
		ip = "unknown"
	}
	now := time.Now()

	g.mu.Lock()
	defer g.mu.Unlock()

	bucket, exists := g.buckets[ip]
	if !exists {
		bucket = &ipBucket{windowStart: now}
		g.buckets[ip] = bucket
	}

	if now.Before(bucket.bannedUntil) {
		return ip, false
	}

	if g.cfg.WindowDuration > 0 && now.Sub(bucket.windowStart) >= g.cfg.WindowDuration {
		bucket.windowStart = now
		bucket.hits = 0
	}

	bucket.hits++
	if g.cfg.BurstPerWindow > 0 && bucket.hits > g.cfg.BurstPerWindow {
		bucket.bannedUntil = now.Add(g.cfg.BanDuration)
		return ip, false
	}

	if g.cfg.MaxActivePerIP > 0 && bucket.active >= g.cfg.MaxActivePerIP {
		return ip, false
	}

	bucket.active++
	return ip, true
}

func (g *IngressGuard) Release(ip string) {
	ip = normalizeIP(ip)
	if ip == "" {
		return
	}

	g.mu.Lock()
	if bucket, ok := g.buckets[ip]; ok && bucket.active > 0 {
		bucket.active--
	}
	g.mu.Unlock()
}

// GFE appends the client and load-balancer addresses to X-Forwarded-For.
// Selecting the second-to-last valid address ignores any client-supplied prefix.
func ExtractRealClientIP(headerLines []string, fallbackIP string) string {
	var forwarded []net.IP

	for _, line := range headerLines {
		name, value, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), "x-forwarded-for") {
			continue
		}
		for _, item := range strings.Split(value, ",") {
			if ip := net.ParseIP(strings.TrimSpace(item)); ip != nil {
				forwarded = append(forwarded, ip)
			}
		}
	}

	if len(forwarded) >= 2 {
		return forwarded[len(forwarded)-2].String()
	}
	return normalizeIP(fallbackIP)
}

func normalizeIP(value string) string {
	value = strings.TrimSpace(value)
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	if ip := net.ParseIP(strings.Trim(value, "[]")); ip != nil {
		return ip.String()
	}
	return ""
}

func FormatHTTPDate() string {
	return time.Now().UTC().Format(http.TimeFormat)
}

func (g *IngressGuard) cleanupLoop() {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()
		g.mu.Lock()
		for ip, bucket := range g.buckets {
			if bucket.active == 0 && !now.Before(bucket.bannedUntil) &&
				now.Sub(bucket.windowStart) > 2*g.cfg.WindowDuration {
				delete(g.buckets, ip)
			}
		}
		g.mu.Unlock()
	}
}
