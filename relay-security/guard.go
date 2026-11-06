package security

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// GuardConfig defines connection limits, rate window, and header rules.
type GuardConfig struct {
	MaxActivePerIP   int
	BurstPerWindow   int
	WindowDuration   time.Duration
	BanDuration      time.Duration
	MaxHandshakeSize int
	HandshakeTimeout time.Duration
}

// DefaultGuardConfig returns balanced anti-DDoS defaults for Cloud Run + CDN ingress.
func DefaultGuardConfig() GuardConfig {
	return GuardConfig{
		MaxActivePerIP:   64,
		BurstPerWindow:   120,
		WindowDuration:   10 * time.Second,
		BanDuration:      60 * time.Second,
		MaxHandshakeSize: 16384,
		HandshakeTimeout: 4 * time.Second,
	}
}

type ipBucket struct {
	active      int
	windowStart time.Time
	hits        int
	bannedUntil time.Time
}

// IngressGuard enforces multi-core thread-safe rate limiting and header inspection.
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

// Acquire checks if a peer IP is allowed to open a new connection.
func (g *IngressGuard) Acquire(peerAddr string) (string, bool) {
	ip := extractIP(peerAddr)
	now := time.Now()

	g.mu.Lock()
	defer g.mu.Unlock()

	b, exists := g.buckets[ip]
	if !exists {
		b = &ipBucket{windowStart: now}
		g.buckets[ip] = b
	}

	if now.Before(b.bannedUntil) {
		return ip, false
	}

	if now.Sub(b.windowStart) > g.cfg.WindowDuration {
		b.windowStart = now
		b.hits = 0
	}

	b.hits++
	if g.cfg.BurstPerWindow > 0 && b.hits > g.cfg.BurstPerWindow {
		b.bannedUntil = now.Add(g.cfg.BanDuration)
		return ip, false
	}

	if g.cfg.MaxActivePerIP > 0 && b.active >= g.cfg.MaxActivePerIP {
		return ip, false
	}

	b.active++
	return ip, true
}

// Release decrements the active connection count for an IP.
func (g *IngressGuard) Release(ip string) {
	if ip == "" {
		return
	}
	g.mu.Lock()
	if b, ok := g.buckets[ip]; ok {
		b.active--
		if b.active < 0 {
			b.active = 0
		}
	}
	g.mu.Unlock()
}

// ExtractRealClientIP resolves real client IP from CDN headers (Cloudflare / CloudFront / GFE) if present.
func ExtractRealClientIP(headerLines []string, fallbackIP string) string {
	for _, line := range headerLines {
		name, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(name))
		v := strings.TrimSpace(val)
		if v == "" {
			continue
		}
		switch key {
		case "cf-connecting-ip", "true-client-ip":
			return v
		case "x-forwarded-for":
			first, _, _ := strings.Cut(v, ",")
			if trimmed := strings.TrimSpace(first); trimmed != "" {
				return trimmed
			}
		}
	}
	return fallbackIP
}

// FormatHTTPDate returns an RFC1123 GMT date string matching Python's email.utils.formatdate(usegmt=True).
func FormatHTTPDate() string {
	return time.Now().UTC().Format(http.TimeFormat)
}

func (g *IngressGuard) cleanupLoop() {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		g.mu.Lock()
		for ip, b := range g.buckets {
			if b.active == 0 && now.After(b.bannedUntil) && now.Sub(b.windowStart) > 2*g.cfg.WindowDuration {
				delete(g.buckets, ip)
			}
		}
		g.mu.Unlock()
	}
}

func extractIP(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}
