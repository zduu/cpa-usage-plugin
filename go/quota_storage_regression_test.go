package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestQuotaStorageRestoresLegacyTotalsWithQuotaOnlyMemory(t *testing.T) {
	for _, state := range []string{"observation", "fact", "deletion"} {
		t.Run(state, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Second)
			dir := legacySnapshotFixture(t, 1, 10, now.Add(-time.Hour), now)
			raw, err := os.ReadFile(storageSnapshotPath(dir))
			if err != nil {
				t.Fatal(err)
			}
			var disk persistedStorageSnapshot
			if err := json.Unmarshal(raw, &disk); err != nil {
				t.Fatal(err)
			}
			disk.Version, disk.Usage.QuotaCycles = 2, nil
			raw, err = json.Marshal(disk)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(storageSnapshotPath(dir), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if _, ok := parseStorageFileDate(entry.Name()); ok {
					if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
						t.Fatal(err)
					}
				}
			}
			dest := NewRequestStatistics()
			defer dest.Close()
			quotaTestObserve(dest, now, now.Add(time.Hour), .4, 18000)
			if state != "observation" {
				r := quotaTestRecord("memory", "m", now.Add(-2*time.Hour), 100)
				dest.Record(r)
				if state == "deletion" && !dest.RemoveRecordedUsage(r) {
					t.Fatal("could not remove the record")
				}
				dest.retention = time.Hour
				dest.pruneLocked(now, true)
			}
			before := cloneQuotaSnapshot(&dest.quota.quotaSnapshot)
			enabled := true
			pricePath := filepath.Join(dir, "prices.json")
			dest.ConfigurePatch(runtimeConfigPatch{StorageEnabled: &enabled, StoragePath: &dir, PriceStoragePath: &pricePath})
			if dest.totalRequests != 10 || dest.totalTokens != 150 {
				t.Fatalf("lost legacy totals: requests=%d tokens=%d", dest.totalRequests, dest.totalTokens)
			}
			if len(dest.quota.Facts) != len(before.Facts) || len(dest.quota.Deleted) != len(before.Deleted) || len(dest.quota.Windows) != len(before.Windows) {
				t.Fatal("loading legacy storage discarded quota-only memory")
			}
			dest.Close()
			restored := NewRequestStatistics()
			defer restored.Close()
			restored.ConfigurePatch(runtimeConfigPatch{StorageEnabled: &enabled, StoragePath: &dir, PriceStoragePath: &pricePath})
			if restored.totalRequests != 10 || restored.totalTokens != 150 || len(restored.quota.Windows) != len(before.Windows) || len(restored.quota.Facts) != len(before.Facts) || len(restored.quota.Deleted) != len(before.Deleted) {
				t.Fatal("legacy totals or quota-only memory did not survive restart")
			}
		})
	}
}

func TestQuotaLegacyImportPreservesRetainedFact(t *testing.T) {
	for _, mode := range []string{"visible", "archived", "masked", "genuine", "synthetic", "deleted"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Second)
			d := pollutedClaudeCacheDetail()
			d.AuthType, d.AuthID, d.AuthIndex = "oauth", "account.json", "account-index"
			d.Timestamp = now.Add(-2 * time.Hour)
			if mode == "masked" {
				d.APIKey, d.APIKeyHash = maskAPIKey("sk-legacy-client-0123456789"), hashAPIKey("sk-legacy-client-0123456789")
			}
			if mode == "genuine" {
				d.Tokens.CacheReadTokens = d.Tokens.CachedTokens
			}
			api := usageGroupKeyFromDetail("claude", d)
			id := quotaLegacyRecordID(api, d.Model, d)
			s := NewRequestStatistics()
			defer s.Close()
			s.recordDetailLocked(api, d.Model, d, requestDedupKey{}, now, false)
			quotaTestObserve(s, now, now.Add(time.Hour), .5, 18000)
			s.retention = time.Hour
			s.pruneLocked(now, true)
			s.claudeCacheRepairEnabled = true
			s.repairClaudeCacheFallbackDetailsLocked(now)
			want := s.quota.Facts[id]
			if mode == "synthetic" {
				want.TimestampSynthetic = true
				s.quota.Facts[id] = want
			}
			if mode == "deleted" {
				deleted := d
				deleted.RecordID = id
				s.removeQuotaFactLocked(deleted)
			}
			s.claudeCacheRepairEnabled = false
			s.retention = 3 * time.Hour
			model := ModelSnapshot{Details: []RequestDetail{d}}
			if mode == "archived" {
				model.Details, model.Accounting = nil, []RequestDetail{d}
			}
			backup := StatisticsSnapshot{APIs: map[string]APISnapshot{api: {Models: map[string]ModelSnapshot{d.Model: model}}}}
			before, _ := json.Marshal(backup)
			for i := 0; i < 2; i++ {
				if _, err := s.mergeSnapshotChecked(backup); err != nil {
					t.Fatal(err)
				}
				if mode == "deleted" {
					if s.totalRequests != 0 || len(s.quota.Facts) != 0 || len(s.quota.Deleted) != 1 {
						t.Fatal("legacy import resurrected deleted accounting")
					}
					continue
				}
				if s.totalRequests != 1 || s.totalTokens != want.Tokens.TotalTokens || !quotaFactsEqual(s.quota.Facts[id], want) {
					t.Fatalf("import %d overwrote retained accounting: requests=%d tokens=%d fact=%+v", i, s.totalRequests, s.totalTokens, s.quota.Facts[id])
				}
				if err := validateQuotaImport(s.Snapshot(), nil, false); err != nil {
					t.Fatalf("main detail differs from retained fact: %v", err)
				}
			}
			after, _ := json.Marshal(backup)
			if !bytes.Equal(before, after) {
				t.Fatal("import changed the caller's backup")
			}
		})
	}
}

func TestQuotaStorageReconcilesLegacyDetailsWithoutLosingResiduals(t *testing.T) {
	for _, mode := range []string{"repaired", "deleted"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Second)
			d := pollutedClaudeCacheDetail()
			d.AuthType, d.AuthID, d.AuthIndex = "oauth", "account.json", "account-index"
			d.Timestamp = now.Add(-2 * time.Hour)
			api := usageGroupKeyFromDetail("claude", d)
			source, dest := NewRequestStatistics(), NewRequestStatistics()
			defer source.Close()
			defer dest.Close()
			for i := 0; i < 10; i++ {
				item := d
				item.Timestamp = d.Timestamp.Add(-time.Duration(i) * time.Minute)
				source.recordDetailLocked(api, d.Model, item, requestDedupKey{}, now, false)
			}
			backup := source.Snapshot()
			backup.QuotaCycles = nil
			a := backup.APIs[api]
			m := a.Models[d.Model]
			m.Details, m.Accounting = []RequestDetail{d}, nil
			a.Models[d.Model] = m
			backup.APIs[api] = a
			dir := t.TempDir()
			raw, err := json.Marshal(persistedStorageSnapshot{Version: 2, GeneratedAt: now.Format(time.RFC3339), Usage: backup})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(storageSnapshotPath(dir), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			dest.recordDetailLocked(api, d.Model, d, requestDedupKey{}, now, false)
			quotaTestObserve(dest, now, now.Add(time.Hour), .5, 18000)
			dest.retention = time.Hour
			dest.pruneLocked(now, true)
			id := quotaLegacyRecordID(api, d.Model, d)
			wantRequests, wantTokens := int64(10), 9*d.Tokens.TotalTokens
			if mode == "deleted" {
				deleted := d
				deleted.RecordID = id
				dest.removeQuotaFactLocked(deleted)
				wantRequests = 9
			} else {
				dest.claudeCacheRepairEnabled = true
				dest.repairClaudeCacheFallbackDetailsLocked(now)
				dest.claudeCacheRepairEnabled = false
				wantTokens += dest.quota.Facts[id].Tokens.TotalTokens
			}
			dest.retention = 3 * time.Hour
			if _, err := dest.loadStorageSnapshotLocked(dir, now); err != nil {
				t.Fatal(err)
			}
			if dest.totalRequests != wantRequests || dest.totalTokens != wantTokens {
				t.Fatalf("lost residuals or restored stale detail: requests=%d tokens=%d", dest.totalRequests, dest.totalTokens)
			}
			if mode == "deleted" && (dest.countDetailsLocked() != 0 || len(dest.quota.Facts) != 0) {
				t.Fatal("snapshot resurrected a deleted legacy detail")
			}
			if err := validateQuotaImport(dest.Snapshot(), nil, false); err != nil {
				t.Fatalf("restored detail disagrees with quota fact: %v", err)
			}
		})
	}
}

func TestQuotaLegacyImportRejectsCredentialConflictAtomically(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	d := pollutedClaudeCacheDetail()
	d.AuthType, d.AuthID, d.AuthIndex = "oauth", "account.json", "account-index"
	d.Timestamp = now.Add(-2 * time.Hour)
	api := usageGroupKeyFromDetail("claude", d)
	s := NewRequestStatistics()
	defer s.Close()
	s.recordDetailLocked(api, d.Model, d, requestDedupKey{}, now, false)
	quotaTestObserve(s, now, now.Add(time.Hour), .5, 18000)
	s.retention = time.Hour
	s.pruneLocked(now, true)
	s.retention = 3 * time.Hour
	before, _ := json.Marshal(s.Snapshot())
	conflict, additional := d, d
	conflict.AuthID = "different-account.json"
	additional.Timestamp = now.Add(-time.Minute)
	backup := StatisticsSnapshot{APIs: map[string]APISnapshot{api: {Models: map[string]ModelSnapshot{d.Model: {Details: []RequestDetail{additional, conflict}}}}}}
	if _, err := s.mergeSnapshotChecked(backup); err == nil {
		t.Fatal("accepted conflicting legacy credential")
	}
	after, _ := json.Marshal(s.Snapshot())
	if !bytes.Equal(before, after) {
		t.Fatal("rejected import changed live accounting")
	}
}

func TestQuotaEnableRepairDuringStorageMerge(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	d := pollutedClaudeCacheDetail()
	d.Timestamp = now.Add(-time.Minute)
	d.AuthType, d.AuthID, d.AuthIndex = "oauth", "account.json", "account-index"
	api := usageGroupKeyFromDetail("claude", d)
	d.RecordID = "first"
	source := NewRequestStatistics()
	defer source.Close()
	source.recordDetailLocked(api, d.Model, d, requestDedupKey{}, now, false)
	other := d
	other.RecordID = "second"
	other.Timestamp = now.Add(-30 * time.Second)
	source.recordDetailLocked(api, other.Model, other, requestDedupKey{}, now, false)
	quotaTestObserve(source, now, now.Add(time.Hour), .5, 18000)
	dir := t.TempDir()
	if err := writeStorageSnapshotViewFile(dir, source.captureStorageSnapshot(), now); err != nil {
		t.Fatal(err)
	}
	dest := NewRequestStatistics()
	defer dest.Close()
	dest.recordDetailLocked(api, d.Model, d, requestDedupKey{}, now, false)
	enabled := true
	pricePath := filepath.Join(dir, "prices.json")
	dest.ConfigurePatch(runtimeConfigPatch{
		ClaudeCacheRepairEnabled: &enabled, StorageEnabled: &enabled, StoragePath: &dir,
		PriceStoragePath: &pricePath,
	})
	if dest.totalRequests != 2 || len(dest.quota.Facts) != 2 {
		t.Fatalf("storage merge lost snapshot records: requests=%d facts=%d", dest.totalRequests, len(dest.quota.Facts))
	}
	want := repairClaudeCacheFallbackTokens(d).Tokens
	for _, f := range dest.quota.Facts {
		if f.Tokens != want {
			t.Fatal("storage merge did not repair both existing and incoming facts")
		}
	}
	if err := validateQuotaImport(dest.Snapshot(), nil, false); err != nil {
		t.Fatal(err)
	}
}

func TestQuotaStorageEnablePreservesQuotaOnlyMemory(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	source, dest := NewRequestStatistics(), NewRequestStatistics()
	defer source.Close()
	defer dest.Close()
	for i, s := range []*RequestStatistics{source, dest} {
		s.Record(quotaTestRecord([]string{"disk", "memory"}[i], "m", now.Add(-2*time.Hour), 100))
		quotaTestObserve(s, now, now.Add(time.Hour), .5, 18000)
		s.retention = time.Hour
		s.pruneLocked(now, true)
	}
	dir := t.TempDir()
	if err := writeStorageSnapshotViewFile(dir, source.captureStorageSnapshot(), now); err != nil {
		t.Fatal(err)
	}
	enabled := true
	pricePath := filepath.Join(dir, "prices.json")
	dest.ConfigurePatch(runtimeConfigPatch{StorageEnabled: &enabled, StoragePath: &dir, PriceStoragePath: &pricePath})
	if len(dest.quota.Facts) != 2 {
		t.Fatalf("enabling storage discarded quota-only memory: facts=%d", len(dest.quota.Facts))
	}
	if dest.totalRequests != 0 {
		t.Fatal("storage merge resurrected expired main accounting")
	}
	backup := dest.Snapshot()
	dest.Close()
	restored := NewRequestStatistics()
	defer restored.Close()
	restored.ConfigurePatch(runtimeConfigPatch{StorageEnabled: &enabled, StoragePath: &dir, PriceStoragePath: &pricePath})
	if len(restored.quota.Facts) != 2 || len(backup.QuotaCycles.Facts) != 2 {
		t.Fatal("merged quota-only facts did not survive restart")
	}
}

func TestQuotaStorageEnablePreservesMemoryTombstones(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	r := quotaTestRecord("deleted", "m", now.Add(-time.Minute), 100)
	source, dest := NewRequestStatistics(), NewRequestStatistics()
	defer source.Close()
	defer dest.Close()
	source.Record(r)
	dest.Record(r)
	if !dest.RemoveRecordedUsage(r) {
		t.Fatal("could not remove the record")
	}
	dir := t.TempDir()
	if err := writeStorageSnapshotViewFile(dir, source.captureStorageSnapshot(), now); err != nil {
		t.Fatal(err)
	}
	enabled := true
	pricePath := filepath.Join(dir, "prices.json")
	dest.ConfigurePatch(runtimeConfigPatch{StorageEnabled: &enabled, StoragePath: &dir, PriceStoragePath: &pricePath})
	if dest.totalRequests != 0 || len(dest.quota.Facts) != 0 || len(dest.quota.Deleted) != 1 {
		t.Fatal("loading storage resurrected a record deleted in memory")
	}
}

func TestQuotaStorageEnablePreservesObservationOnlyMemory(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	source, dest := NewRequestStatistics(), NewRequestStatistics()
	defer source.Close()
	defer dest.Close()
	quotaTestObserve(source, now, now.Add(time.Hour), .5, 18000)
	dest.applyQuotaObservationLocked(quotaObservation{Provider: "claude", AuthIndex: "account-index", AuthID: "account.json", Group: "shared", Slot: "7d", Seconds: 604800, Reset: now.Add(time.Hour), ObservedAt: now, Used: .2})
	dir := t.TempDir()
	if err := writeStorageSnapshotViewFile(dir, source.captureStorageSnapshot(), now); err != nil {
		t.Fatal(err)
	}
	enabled := true
	pricePath := filepath.Join(dir, "prices.json")
	dest.ConfigurePatch(runtimeConfigPatch{StorageEnabled: &enabled, StoragePath: &dir, PriceStoragePath: &pricePath})
	if len(dest.quota.Windows) != 2 || dest.totalRequests != 0 {
		t.Fatal("loading storage did not merge observation-only state")
	}
}

func TestQuotaConflictingStorageSnapshotDoesNotChangeMemory(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	source, dest := NewRequestStatistics(), NewRequestStatistics()
	defer source.Close()
	defer dest.Close()
	source.Record(quotaTestRecord("same-execution", "m", now.Add(-time.Minute), 200))
	dest.Record(quotaTestRecord("same-execution", "m", now.Add(-time.Minute), 100))
	source.Record(quotaTestRecord("additional", "m", now.Add(-30*time.Second), 300))
	dir := t.TempDir()
	if err := writeStorageSnapshotViewFile(dir, source.captureStorageSnapshot(), now); err != nil {
		t.Fatal(err)
	}
	if at, err := dest.loadStorageSnapshotLocked(dir, now); err == nil || !at.IsZero() {
		t.Fatal("conflicting snapshot was accepted or advanced the replay cutoff")
	}
	if dest.totalRequests != 1 || dest.totalTokens != 100 || len(dest.quota.Facts) != 1 {
		t.Fatal("conflicting snapshot changed memory accounting")
	}
}
