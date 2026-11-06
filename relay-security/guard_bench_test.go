// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Gojo Research

package security

import "testing"

func BenchmarkIngressGuardAcquireRelease(b *testing.B) {
	guard := NewIngressGuard(GuardConfig{
		MaxActivePerIP: 0,
		BurstPerWindow: 0,
	})
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			ip, allowed := guard.Acquire("198.51.100.10")
			if allowed {
				guard.Release(ip)
			}
		}
	})
}
