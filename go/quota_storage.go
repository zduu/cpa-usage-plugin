package main

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"sort"
	"time"
)

func validQuotaObservation(o quotaObservation) bool {
	identityValid := quotaProvider(o.Provider) && (o.AuthIndex != "" || o.AuthID != "") && len(o.AuthID) <= 512 && len(o.AuthIndex) <= 512 &&
		o.Group != "" && len(o.Group) <= 256 && o.Slot != "" && len(o.Slot) <= 64 && len(o.Name) <= 256 && !o.ObservedAt.IsZero()
	if !identityValid || o.Revoked {
		return identityValid && o.Revoked
	}
	return o.Seconds > 0 && o.Seconds <= 366*86400 && !o.Reset.IsZero() &&
		!math.IsNaN(o.Used) && !math.IsInf(o.Used, 0) && o.Used >= 0 && o.Used <= 1 &&
		!o.ObservedAt.Before(o.Reset.Add(-time.Duration(o.Seconds)*time.Second)) && !o.ObservedAt.After(o.Reset)
}

func quotaFactsEqual(a, b quotaFact) bool {
	if !a.Timestamp.Equal(b.Timestamp) || !a.CompletedAt.Equal(b.CompletedAt) {
		return false
	}
	a.Timestamp, b.Timestamp = time.Time{}, time.Time{}
	a.CompletedAt, b.CompletedAt = time.Time{}, time.Time{}
	return a == b
}

// Older quota facts omitted the synthetic timestamp flag. Recover it from a
// retained main-ledger reference before validating or restoring the snapshot.
// Clone lazily so importing a backup never changes the caller's data.
func backfillQuotaTimestampMetadata(snapshot StatisticsSnapshot) StatisticsSnapshot {
	if snapshot.QuotaCycles == nil {
		return snapshot
	}
	copied := false
	for _, api := range snapshot.APIs {
		for _, model := range api.Models {
			for _, d := range model.accountingDetails() {
				if d.RecordID == "" || !d.TimestampSynthetic || !quotaEligible(d) {
					continue
				}
				f, exists := snapshot.QuotaCycles.Facts[d.RecordID]
				if !exists || f.TimestampSynthetic {
					continue
				}
				if !copied {
					snapshot.QuotaCycles = cloneQuotaSnapshot(snapshot.QuotaCycles)
					copied = true
				}
				f.TimestampSynthetic = true
				snapshot.QuotaCycles.Facts[d.RecordID] = f
			}
		}
	}
	return snapshot
}

// Check all references before importing either the main ledger or quota facts.
func validateQuotaImport(snapshot StatisticsSnapshot, current *quotaState, repairClaudeCache bool) error {
	if err := validateQuotaSnapshot(snapshot.QuotaCycles, current); err != nil {
		return err
	}
	seen := make(map[string]quotaFact)
	for api, a := range snapshot.APIs {
		for model, m := range a.Models {
			for _, d := range m.accountingDetails() {
				if !quotaEligible(d) {
					continue
				}
				legacy := d.RecordID == ""
				d.Model = normalizeDetailModelName(model, d.Model)
				if legacy {
					// A missing timestamp is assigned at insertion and cannot be
					// matched to a stable legacy identity during validation.
					if d.Timestamp.IsZero() {
						continue
					}
					d = normalizeStorageSnapshotDetailFields(d.Model, d, d.Timestamp)
					d = normalizeStoredClientAPIIdentity(d)
					d.RecordID = quotaLegacyRecordID(usageGroupKeyFromDetail(api, d), d.Model, d)
				}
				if repairClaudeCache {
					d = normalizeClaudeCacheFallbackDetail(d)
				}
				f := quotaFactFromDetail(api, d)
				candidates := []map[string]quotaFact{seen}
				if snapshot.QuotaCycles != nil {
					candidates = append(candidates, snapshot.QuotaCycles.Facts)
				}
				if current != nil {
					candidates = append(candidates, current.Facts)
				}
				for _, facts := range candidates {
					if old, ok := facts[f.ID]; ok {
						candidate := f
						if legacy {
							// Retained accounting and timestamp metadata take
							// precedence, but credential/model conflicts still fail
							// before any part of the import changes live state.
							candidate.Tokens, candidate.TimestampSynthetic = old.Tokens, old.TimestampSynthetic
						}
						// Display groups may be normalized across instances.
						old.API = f.API
						if !quotaFactsEqual(old, candidate) {
							return errors.New("conflicting quota record reference")
						}
					}
				}
				seen[f.ID] = f
			}
		}
	}
	return nil
}

// Import quota-only facts using the same opt-in normalization as main details,
// before checking identities or persisting either representation. Never mutate
// the caller's backup, including when a later conflict rejects the import.
func (s *RequestStatistics) prepareQuotaImportSnapshotLocked(snapshot StatisticsSnapshot) StatisticsSnapshot {
	snapshot = backfillQuotaTimestampMetadata(snapshot)
	if !s.claudeCacheRepairEnabled || snapshot.QuotaCycles == nil {
		return snapshot
	}
	copied := false
	for id, fact := range snapshot.QuotaCycles.Facts {
		detail := fact.detail()
		if !validQuotaFact(fact) || !isPollutedClaudeCacheFallbackDetail(detail) {
			continue
		}
		if !copied {
			snapshot.QuotaCycles = cloneQuotaSnapshot(snapshot.QuotaCycles)
			copied = true
		}
		fact.Tokens = repairClaudeCacheFallbackTokens(detail).Tokens
		snapshot.QuotaCycles.Facts[id] = fact
	}
	return snapshot
}

func cloneQuotaSnapshot(in *quotaSnapshot) *quotaSnapshot {
	if in == nil {
		return nil
	}
	out := &quotaSnapshot{Version: in.Version, StartedAt: in.StartedAt, Facts: make(map[string]quotaFact, len(in.Facts)), Windows: make(map[string]quotaWindow, len(in.Windows))}
	for key, f := range in.Facts {
		out.Facts[key] = f
	}
	out.Deleted = make(map[string]time.Time, len(in.Deleted))
	for key, at := range in.Deleted {
		out.Deleted[key] = at
	}
	clonePeriod := func(p *quotaPeriod) *quotaPeriod {
		if p == nil {
			return nil
		}
		cloned := *p
		cloned.Samples = append([]quotaObservation(nil), p.Samples...)
		if cloned.CollectionStartedAt.IsZero() {
			cloned.CollectionStartedAt = in.StartedAt
		}
		return &cloned
	}
	for key, w := range in.Windows {
		w.Current = clonePeriod(w.Current)
		w.Previous = clonePeriod(w.Previous)
		out.Windows[key] = w
	}
	return out
}

func (s *RequestStatistics) captureQuotaSnapshotLocked() *quotaSnapshot {
	if s.quota == nil {
		return nil
	}
	s.pruneQuotaLocked(time.Now())
	return cloneQuotaSnapshot(&s.quota.quotaSnapshot)
}

func validQuotaFact(f quotaFact) bool {
	t := f.Tokens
	return f.ID != "" && len(f.ID) <= 256 && quotaProvider(f.Provider) && (f.AuthIndex != "" || f.AuthID != "") &&
		len(f.AuthID) <= 512 && len(f.AuthIndex) <= 512 && len(f.API) <= 4096 && len(f.Model) <= 1024 && !f.Timestamp.IsZero() &&
		t.InputTokens >= 0 && t.OutputTokens >= 0 && t.ReasoningTokens >= 0 && t.CachedTokens >= 0 && t.CacheReadTokens >= 0 && t.CacheWriteTokens >= 0 && t.CacheTokens >= 0 && t.TotalTokens >= 0
}

func validateQuotaSnapshot(in *quotaSnapshot, current *quotaState) error {
	if in == nil {
		return nil
	}
	if in.Version != 1 {
		return errors.New("unsupported quota snapshot")
	}
	for key, f := range in.Facts {
		if key != f.ID || !validQuotaFact(f) {
			return errors.New("invalid quota fact")
		}
		if current != nil {
			if old, ok := current.Facts[key]; ok && !quotaFactsEqual(old, f) {
				return errors.New("conflicting quota fact identity")
			}
		}
	}
	for key, at := range in.Deleted {
		if key == "" || len(key) > 256 || at.IsZero() {
			return errors.New("invalid quota deletion")
		}
	}
	for key, w := range in.Windows {
		revocationOnly := w.Hidden && w.Seconds == 0 && w.Current == nil && w.Previous == nil && !w.UpdatedAt.IsZero()
		if !quotaProvider(w.Provider) || (!revocationOnly && w.Seconds <= 0) || w.Seconds > 366*86400 ||
			(w.AuthID == "" && w.AuthIndex == "") || len(w.AuthID) > 512 || len(w.AuthIndex) > 512 ||
			w.Group == "" || len(w.Group) > 256 || w.Slot == "" || len(w.Slot) > 64 || len(w.Name) > 256 || len(w.Model) > 1024 {
			return errors.New("invalid quota window")
		}
		o := quotaObservation{Provider: w.Provider, AuthIndex: w.AuthIndex, AuthID: w.AuthID, Group: w.Group, Slot: w.Slot}
		if key != quotaWindowKey(o) || (!revocationOnly && w.Group != "shared" && !w.Unmapped && w.Model == "") {
			return errors.New("invalid quota group")
		}
		for _, p := range []*quotaPeriod{w.Current, w.Previous} {
			if p == nil {
				continue
			}
			if len(p.Samples) > 64 || !p.Start.Before(p.End) || p.End.Sub(p.Start) != time.Duration(w.Seconds)*time.Second {
				return errors.New("invalid quota period")
			}
			for _, sample := range p.Samples {
				if !validQuotaObservation(sample) || sample.Revoked || quotaWindowKey(sample) != key || sample.Seconds != w.Seconds {
					return errors.New("invalid quota observation")
				}
			}
			if !sort.SliceIsSorted(p.Samples, func(i, j int) bool { return p.Samples[i].ObservedAt.Before(p.Samples[j].ObservedAt) }) {
				return errors.New("unordered quota observations")
			}
		}
	}
	return nil
}

func (s *RequestStatistics) mergeQuotaSnapshotLocked(in *quotaSnapshot) {
	if in == nil || validateQuotaSnapshot(in, s.quota) != nil {
		return
	}
	copy := cloneQuotaSnapshot(in)
	q := s.ensureQuotaLocked()
	// A merged backup does not prove continuous collection before this process.
	if q.StartedAt.Before(copy.StartedAt) {
		q.StartedAt = copy.StartedAt
	}
	if q.Deleted == nil {
		q.Deleted = make(map[string]time.Time)
	}
	for key, at := range copy.Deleted {
		if at.After(q.Deleted[key]) {
			q.Deleted[key] = at
		}
		delete(q.Facts, key)
	}
	s.removeQuotaDeletedDetailsLocked(copy.Deleted)
	for key, f := range copy.Facts {
		if _, deleted := q.Deleted[key]; !deleted {
			q.Facts[key] = f
		}
	}
	for key, w := range copy.Windows {
		if old, ok := q.Windows[key]; ok {
			if w.UpdatedAt.Before(old.UpdatedAt) || (w.UpdatedAt.Equal(old.UpdatedAt) && (!w.Hidden || old.Hidden)) {
				w, old = old, w
			}
			mergeQuotaWindowHistory(&w, old)
		}
		q.Windows[key] = w
	}
	q.VersionCounter++
	s.invalidateCachedResponsesLocked()
}

// Keep the newest window metadata, while retaining samples and a real adjacent
// previous period from either backup. Import order must not discard history.
func mergeQuotaWindowHistory(w *quotaWindow, incoming quotaWindow) {
	if w.Hidden && incoming.Seconds > 0 && (w.Seconds == 0 || quotaWindowObservedAt(incoming).After(quotaWindowObservedAt(*w))) {
		// The newest revocation may have older period history. Choose the
		// history by usage observation time, independently of visibility.
		updated := w.UpdatedAt
		*w, incoming = incoming, *w
		w.UpdatedAt, w.Hidden = updated, true
	}
	if w.Seconds != incoming.Seconds {
		return
	}
	for _, p := range []*quotaPeriod{incoming.Previous, incoming.Current} {
		if p == nil {
			continue
		}
		if w.Current != nil && w.Previous == nil && quotaResetDriftIsSmall(w.Current.Start, p.End, w.Seconds) {
			w.Previous = &quotaPeriod{Start: w.Current.Start.Add(-time.Duration(w.Seconds) * time.Second), End: w.Current.Start,
				CollectionStartedAt: p.CollectionStartedAt}
		}
		var target *quotaPeriod
		for _, period := range []*quotaPeriod{w.Previous, w.Current} {
			if period != nil && quotaResetDriftIsSmall(period.End, p.End, w.Seconds) {
				target = period
				break
			}
		}
		if target == nil {
			// Reset cards may move the deadline beyond ordinary clock drift.
			// Preserve the preceding observations when both histories describe
			// overlapping live windows. A real next cycle has no such overlap.
			for _, period := range []*quotaPeriod{w.Previous, w.Current} {
				if quotaPeriodsShareObservedWindow(period, p) {
					// Do not join an ambiguous history to either cycle.
					if target != nil {
						target = nil
						break
					}
					target = period
				}
			}
		}
		if target != nil {
			for _, sample := range p.Samples {
				quotaAppendSample(target, sample, p.CollectionStartedAt)
			}
		}
	}
}

func quotaPeriodsShareObservedWindow(a, b *quotaPeriod) bool {
	return a != nil && b != nil && len(a.Samples) > 0 && len(b.Samples) > 0 &&
		a.Start.Before(b.End) && b.Start.Before(a.End) &&
		a.Samples[0].ObservedAt.Before(b.End) && b.Samples[0].ObservedAt.Before(a.End)
}

func (s *RequestStatistics) restoreQuotaSnapshotLocked(snapshot StatisticsSnapshot) {
	if snapshot.QuotaCycles != nil {
		snapshot = backfillQuotaTimestampMetadata(snapshot)
		s.quota = &quotaState{quotaSnapshot: *cloneQuotaSnapshot(snapshot.QuotaCycles)}
		// Restart can leave an unobserved interval; calibrate with a new pair.
		s.quota.StartedAt = time.Now()
		s.removeQuotaDeletedDetailsLocked(s.quota.Deleted)
		return
	}
	for api, a := range s.apis {
		for _, m := range a.Models {
			for i := 0; i < m.accountingCount(); i++ {
				d := m.accountingDetailAt(i)
				if quotaEligible(d) {
					if d.RecordID == "" {
						d.RecordID = quotaLegacyRecordID(api, d.Model, d)
						m.setAccountingDetailAt(i, d)
					}
					s.addQuotaFactLocked(api, d)
				}
			}
		}
	}
}

// A tombstone identifies an accounting deletion, including when it arrives
// after the original detail through an import or journal replay. Apply only
// addressable per-detail deltas so legacy aggregate residuals survive.
func (s *RequestStatistics) removeQuotaDeletedDetailsLocked(deleted map[string]time.Time) {
	if len(deleted) == 0 {
		return
	}
	changed := false
	for apiName, api := range s.apis {
		if api == nil {
			continue
		}
		for modelName, model := range api.Models {
			if model == nil {
				continue
			}
			for i := model.accountingCount() - 1; i >= 0; i-- {
				d := model.accountingDetailAt(i)
				id := d.RecordID
				if id == "" && quotaEligible(d) {
					id = quotaLegacyRecordID(apiName, modelName, d)
				}
				if _, ok := deleted[id]; !ok {
					continue
				}
				s.decrementCounters(d, api, model, modelName)
				model.removeAccountingDetailAt(i)
				changed = true
			}
			if model.accountingCount() == 0 && model.TotalRequests <= 0 {
				delete(api.Models, modelName)
			}
		}
		if len(api.Models) == 0 && api.TotalRequests <= 0 {
			delete(s.apis, apiName)
		}
	}
	if changed {
		s.rebuildSeenLocked(time.Now())
		s.invalidateSummaryLocked()
	}
}

// Reconcile a restored main ledger with quota state retained in memory. Apply
// only per-detail deltas: rebuilding totals would discard old snapshot counters
// whose detail records were truncated by earlier plugin versions.
func (s *RequestStatistics) mergeRestoredQuotaDetailsLocked(now time.Time) {
	var corrected []persistedDetail
	for api, a := range s.apis {
		for model, m := range a.Models {
			for i := m.accountingCount() - 1; i >= 0; i-- {
				d := m.accountingDetailAt(i)
				if !quotaEligible(d) {
					continue
				}
				if d.RecordID == "" {
					d.RecordID = quotaLegacyRecordID(api, model, d)
					m.setAccountingDetailAt(i, d)
				}
				if _, deleted := s.quota.Deleted[d.RecordID]; deleted {
					s.decrementCounters(d, a, m, model)
					m.removeAccountingDetailAt(i)
					continue
				}
				if fact, exists := s.quota.Facts[d.RecordID]; exists && (d.Tokens != fact.Tokens || d.TimestampSynthetic != fact.TimestampSynthetic) {
					s.decrementCounters(d, a, m, model)
					archived := i >= len(m.Details)
					m.removeAccountingDetailAt(i)
					d.Tokens, d.TimestampSynthetic = fact.Tokens, fact.TimestampSynthetic
					corrected = append(corrected, persistedDetail{API: api, Model: model, Detail: d, Archived: archived})
					continue
				}
				s.addQuotaFactLocked(api, d)
			}
		}
	}
	for _, item := range corrected {
		s.recordDetailWithAccountingLocked(item.API, item.Model, item.Detail, requestDedupKey{}, now, false, item.Archived)
	}
	// Drop empty groups left by tombstones and retain the usual import cutoff.
	s.pruneLocked(now, true)
	s.rebuildSeenLocked(now)
	s.invalidateSummaryLocked()
}

func quotaStorageRecords(in *quotaSnapshot) []persistedDetail {
	if in == nil {
		return nil
	}
	out := make([]persistedDetail, 0, len(in.Facts)+len(in.Windows))
	for _, f := range in.Facts {
		out = append(out, persistedDetail{Kind: "quota", QuotaFacts: []quotaFact{f}})
	}
	for key, at := range in.Deleted {
		out = append(out, persistedDetail{Kind: "quota", QuotaSnapshot: &quotaSnapshot{Version: 1, StartedAt: in.StartedAt, Deleted: map[string]time.Time{key: at}}})
	}
	for key, w := range in.Windows {
		out = append(out, persistedDetail{Kind: "quota", QuotaSnapshot: &quotaSnapshot{Version: 1, StartedAt: in.StartedAt, Windows: map[string]quotaWindow{key: w}}})
	}
	return out
}

func writeQuotaSnapshot(w io.Writer, snapshot *quotaSnapshot) error {
	if snapshot == nil {
		_, err := io.WriteString(w, "null")
		return err
	}
	if err := writeJSONObjectPrefix(w, struct {
		Version   int                    `json:"version"`
		StartedAt time.Time              `json:"started_at"`
		Windows   map[string]quotaWindow `json:"windows"`
		Deleted   map[string]time.Time   `json:"deleted,omitempty"`
	}{snapshot.Version, snapshot.StartedAt, snapshot.Windows, snapshot.Deleted}); err != nil {
		return err
	}
	if _, err := io.WriteString(w, `,"facts":{`); err != nil {
		return err
	}
	keys := make([]string, 0, len(snapshot.Facts))
	for key := range snapshot.Facts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	encoder := json.NewEncoder(w)
	for i, key := range keys {
		if i > 0 {
			if _, err := io.WriteString(w, ","); err != nil {
				return err
			}
		}
		raw, _ := json.Marshal(key)
		if _, err := w.Write(append(raw, ':')); err != nil {
			return err
		}
		if err := encoder.Encode(snapshot.Facts[key]); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "}}")
	return err
}
