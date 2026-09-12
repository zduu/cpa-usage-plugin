package main

import (
	"fmt"
	"testing"
	"time"
)

func TestAccountingIdentityHashCollisionsRemainDistinct(t *testing.T) {
	index := make(accountingIdentityIndex)
	keys := []accountingIdentity{{Model: "same", APIKeyHash: "a"}, {Model: "same", APIKeyHash: "b"}, {Model: " same", APIKeyHash: "a"}, {Model: "Σ"}, {Model: "ς"}}
	for i, key := range keys {
		first := index.intern(key, 1) // force every distinct identity into one hash chain
		if *first != key || index.intern(key, 1) != first {
			t.Fatalf("identity %d merged or not interned", i)
		}
	}
	for _, key := range keys {
		if got := index.intern(key, 1); *got != key {
			t.Fatal("collision overwrote an identity")
		}
	}
}

func TestAccountingIdentityExpiryRebuildRetainsOnlyLiveIdentities(t *testing.T) {
	s := NewRequestStatistics()
	s.maxDetailsPerModel = 0
	s.retention = 24 * time.Hour
	now := time.Now()
	for i := 0; i < 2048; i++ {
		at := now.Add(-48 * time.Hour)
		if i >= 2040 {
			at = now.Add(-time.Hour)
		}
		s.recordDetailLocked("api", "model", RequestDetail{Timestamp: at, Model: "model", APIKeyHash: fmt.Sprint(i), Tokens: TokenStats{InputTokens: 1, TotalTokens: 1}}, requestDedupKey{}, now, false)
	}
	m := s.apis["api"].Models["model"]
	s.pruneLocked(now, true)
	if len(m.accountingIdentities) != 8 || m.accounting.count != 8 || s.totalRequests != 8 {
		t.Fatalf("expired identities retained or accounting changed: %d/%d/%d", len(m.accountingIdentities), m.accounting.count, s.totalRequests)
	}
	for r := range m.accounting.records() {
		if r.Identity != m.internAccountingIdentity(*r.Identity) {
			t.Fatal("live records reference the discarded identity dictionary")
		}
	}
}
