package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

func newQuotaRecordID() string { return "local:" + rand.Text() }

// Legacy storage has no execution ID and already deduplicates by content.
// Persist that identity so replay can find the same quota fact even after
// ordinary retention removes the detail. Live executions keep their own IDs.
func quotaLegacyRecordID(api, model string, d RequestDetail) string {
	k := claudeCacheCanonicalDedupKey(api, model, d)
	raw, _ := json.Marshal([]any{k.apiName, k.modelName, k.timestamp, k.source, k.authIndex,
		k.clientAPIHash, k.clientAPIKey, k.failure, k.failed, k.latencyMs, k.ttftMs, k.statusCode,
		k.inputTokens, k.outputTokens, k.reasoning, k.cachedTokens, k.cacheTokens, k.cacheWriteTokens, k.totalTokens})
	sum := sha256.Sum256(raw)
	return "legacy:" + hex.EncodeToString(sum[:])
}

func quotaRequestID(record UsageRecord) string {
	if !quotaEligible(requestDetailFromQuotaUsage(record)) {
		return ""
	}
	if record.RequestID == "" {
		return newQuotaRecordID()
	}
	sum := sha256.Sum256([]byte(quotaCredentialKey(record.Provider, record.AuthIndex, record.AuthID) + "\x00" + record.RequestID))
	return "request:" + hex.EncodeToString(sum[:])
}

func quotaEligible(d RequestDetail) bool {
	kind := strings.ToLower(strings.TrimSpace(d.AuthType))
	return quotaProvider(d.Provider) && (d.AuthIndex != "" || d.AuthID != "") && kind != "api_key" && kind != "apikey" && kind != "api-key"
}

func (s *RequestStatistics) ensureQuotaLocked() *quotaState {
	if s.quota == nil {
		s.quota = &quotaState{quotaSnapshot: quotaSnapshot{Version: 1, StartedAt: s.startedAt,
			Facts: make(map[string]quotaFact), Windows: make(map[string]quotaWindow)}}
	}
	return s.quota
}

func (s *RequestStatistics) addQuotaFactLocked(api string, d RequestDetail) {
	if !quotaEligible(d) {
		return
	}
	q := s.ensureQuotaLocked()
	id := d.RecordID
	if id == "" {
		id = quotaLegacyRecordID(api, d.Model, d)
	}
	if _, deleted := q.Deleted[id]; deleted {
		return
	}
	f := quotaFactFromDetail(api, d)
	f.ID = id
	if old, exists := q.Facts[id]; exists && quotaFactsEqual(old, f) {
		return
	}
	q.Facts[id] = f
	q.VersionCounter++
}

func quotaFactFromDetail(api string, d RequestDetail) quotaFact {
	f := quotaFact{ID: d.RecordID, API: api, Provider: strings.ToLower(strings.TrimSpace(d.Provider)), AuthIndex: d.AuthIndex,
		AuthID: d.AuthID, Model: d.Model, Timestamp: d.Timestamp, TimestampSynthetic: d.TimestampSynthetic, Failed: d.Failed, Tokens: d.Tokens}
	if d.LatencyMs >= 0 && d.LatencyMs <= int64((366*24*time.Hour)/time.Millisecond) {
		f.CompletedAt = d.Timestamp.Add(time.Duration(d.LatencyMs) * time.Millisecond)
	}
	return f
}

func quotaAppendSample(period *quotaPeriod, o quotaObservation, started time.Time) bool {
	for _, old := range period.Samples {
		if old == o {
			return false
		}
	}
	period.Samples = append(period.Samples, o)
	// Preserve the calibration that was valid when this period was observed.
	// A later process starts a new baseline only upon a new live observation;
	// replaying older samples must not erase a completed period's calibration.
	if period.CollectionStartedAt.IsZero() || (!o.ObservedAt.Before(started) && started.After(period.CollectionStartedAt)) {
		period.CollectionStartedAt = started
	}
	sort.SliceStable(period.Samples, func(i, j int) bool { return period.Samples[i].ObservedAt.Before(period.Samples[j].ObservedAt) })
	if len(period.Samples) > 64 {
		// Preserve the first observation of this run and both sides of the
		// latest decrease. Keeping only the global first sample can erase a
		// reset and make later estimates include costs from before the reset.
		first, drop := -1, -1
		for i, sample := range period.Samples {
			if first < 0 && !sample.ObservedAt.Before(period.CollectionStartedAt) {
				first = i
			}
			if i > 0 && sample.Used < period.Samples[i-1].Used {
				drop = i
			}
		}
		kept := make([]quotaObservation, 0, 64)
		for i, sample := range period.Samples {
			if i == 0 || i == first || i == drop-1 || i == drop || i >= len(period.Samples)-60 {
				kept = append(kept, sample)
			}
		}
		period.Samples = kept
	}
	return true
}

func quotaWindowObservedAt(w quotaWindow) time.Time {
	var latest time.Time
	for _, p := range []*quotaPeriod{w.Current, w.Previous} {
		if p != nil && len(p.Samples) > 0 {
			at := p.Samples[len(p.Samples)-1].ObservedAt
			if at.After(latest) {
				latest = at
			}
		}
	}
	return latest
}

func (s *RequestStatistics) applyQuotaObservationLocked(o quotaObservation) bool {
	if !validQuotaObservation(o) {
		return false
	}
	q := s.ensureQuotaLocked()
	key := quotaWindowKey(o)
	if o.CollectionStartedAt.IsZero() {
		o.CollectionStartedAt = q.StartedAt
	}
	w, exists := q.Windows[key]
	if o.Revoked {
		if exists && (o.ObservedAt.Before(w.UpdatedAt) || (w.Hidden && o.ObservedAt.Equal(w.UpdatedAt))) {
			return false
		}
		if !exists {
			// Remember revocation even before the first window arrives. A
			// delayed usage callback must not make that older window visible.
			w = quotaWindow{Provider: o.Provider, AuthIndex: o.AuthIndex, AuthID: o.AuthID, Group: o.Group, Slot: o.Slot}
		}
		w.Hidden, w.UpdatedAt = true, o.ObservedAt
		q.Windows[key] = w
		q.VersionCounter++
		s.invalidateCachedResponsesLocked()
		return true
	}
	start := o.Reset.Add(-time.Duration(o.Seconds) * time.Second)
	if !exists {
		w = quotaWindow{Provider: o.Provider, AuthIndex: o.AuthIndex, AuthID: o.AuthID, Group: o.Group,
			Name: o.Name, Slot: o.Slot, Seconds: o.Seconds, Model: o.Model, Unmapped: o.Unmapped}
	}
	var revokedAt time.Time
	if w.Hidden && !o.ObservedAt.After(w.UpdatedAt) {
		// Revocation and usage have separate ordering: older usage may still
		// advance the retained history without making the window visible.
		revokedAt, w.UpdatedAt = w.UpdatedAt, quotaWindowObservedAt(w)
	}
	commit := func() bool {
		if !revokedAt.IsZero() {
			w.Hidden, w.UpdatedAt = true, revokedAt
		}
		q.Windows[key] = w
		q.VersionCounter++
		s.invalidateCachedResponsesLocked()
		return true
	}
	if o.ObservedAt.Before(w.UpdatedAt) || (w.Hidden && o.ObservedAt.Equal(w.UpdatedAt)) {
		for _, p := range []*quotaPeriod{w.Current, w.Previous} {
			// Boundary alignment must still accept delayed relative-reset samples
			// without moving the shared boundary back to their older timestamp.
			if w.Seconds == o.Seconds && p != nil && quotaResetDriftIsSmall(p.End, o.Reset, o.Seconds) && quotaAppendSample(p, o, o.CollectionStartedAt) {
				return commit()
			}
		}
		// Observations can be delivered out of order, including across a
		// journal restart. A real adjacent window may not have a slot yet.
		if w.Seconds == o.Seconds && w.Current != nil && w.Previous == nil &&
			quotaResetDriftIsSmall(w.Current.Start, o.Reset, o.Seconds) {
			w.Previous = &quotaPeriod{Start: w.Current.Start.Add(-time.Duration(o.Seconds) * time.Second),
				End: w.Current.Start, CollectionStartedAt: o.CollectionStartedAt, Samples: []quotaObservation{o}}
			return commit()
		}
		return false
	}
	if exists && w.Seconds != o.Seconds {
		w.Current = nil
		w.Previous = nil
	}
	if w.Current != nil && (!w.Current.End.After(start) || quotaResetDriftIsSmall(w.Current.End, start, o.Seconds)) {
		w.Previous, w.Current = w.Current, nil
	}
	p := w.Current
	if p == nil && w.Previous != nil && w.Previous.End.Equal(o.Reset) {
		p = w.Previous
	}
	if p != nil && p.End.Equal(o.Reset) && w.Seconds == o.Seconds {
		if !quotaAppendSample(p, o, o.CollectionStartedAt) {
			return false
		}
	} else if w.Current != nil && w.Seconds == o.Seconds && o.ObservedAt.Before(w.Current.End) &&
		start.Before(w.Current.End) && len(w.Current.Samples) > 0 &&
		(o.Used >= w.Current.Samples[len(w.Current.Samples)-1].Used || quotaResetDriftIsSmall(w.Current.End, o.Reset, o.Seconds)) {
		// A relative-reset timestamp can drift while referring to the same window.
		// Small rounding/arrival drift remains compatible when usage decreases;
		// retaining that sample lets the displayed ratio follow the correction.
		w.Current.Start, w.Current.End = start, o.Reset
		quotaAppendSample(w.Current, o, o.CollectionStartedAt)
	} else {
		if w.Current != nil {
			if !w.Current.End.After(start) {
				w.Previous = w.Current
			} else {
				return false
			}
		}
		w.Current = &quotaPeriod{Start: start, End: o.Reset, CollectionStartedAt: o.CollectionStartedAt, Samples: []quotaObservation{o}}
	}
	if w.Current != nil && w.Previous != nil {
		if quotaResetDriftIsSmall(w.Previous.End, w.Current.Start, o.Seconds) {
			// The newer observation fixes the shared boundary. Align the old
			// window too so a request cannot belong to both periods after drift.
			w.Previous.End = w.Current.Start
			w.Previous.Start = w.Previous.End.Add(-time.Duration(o.Seconds) * time.Second)
		} else {
			w.Previous = nil
		}
	}
	w.Seconds, w.Name, w.Model, w.Unmapped = o.Seconds, o.Name, o.Model, o.Unmapped
	w.UpdatedAt, w.Hidden = o.ObservedAt, false
	w.EstimateExpired = false
	return commit()
}

func quotaResetDriftIsSmall(previous, next time.Time, seconds int64) bool {
	tolerance := min(time.Minute, time.Duration(seconds)*time.Second/4)
	drift := next.Sub(previous)
	return drift >= -tolerance && drift <= tolerance
}

func (s *RequestStatistics) advanceQuotaCyclesLocked(now time.Time) {
	if s.quota == nil {
		return
	}
	changed := false
	for key, w := range s.quota.Windows {
		if w.Current != nil && !w.EstimateExpired && len(w.Current.Samples) > 0 {
			last := w.Current.Samples[len(w.Current.Samples)-1]
			if now.Sub(last.ObservedAt) > time.Duration(w.Seconds)*time.Second/4 {
				w.EstimateExpired = true
				s.quota.Windows[key] = w
				changed = true
			}
		}
		if w.Current != nil && !now.Before(w.Current.End) {
			w.Previous, w.Current = w.Current, nil
			s.quota.Windows[key] = w
			changed = true
		}
	}
	if changed {
		s.quota.VersionCounter++
		s.invalidateCachedResponsesLocked()
	}
}

func (s *RequestStatistics) pruneQuotaLocked(now time.Time) {
	const revocationRetention = 2 * 366 * 24 * time.Hour
	if s.quota == nil {
		return
	}
	s.advanceQuotaCyclesLocked(now)
	if now.Before(s.quota.NextPrune) {
		return
	}
	s.quota.NextPrune = now.Add(time.Minute)
	starts := make(map[string]time.Time)
	for key, w := range s.quota.Windows {
		p := w.Current
		if p == nil {
			p = w.Previous
		}
		if p == nil {
			// A marker without retained usage has no duration. Bound its
			// lifetime by two maximum supported periods.
			if w.Hidden && now.After(w.UpdatedAt.Add(revocationRetention)) {
				delete(s.quota.Windows, key)
				s.quota.VersionCounter++
				s.invalidateCachedResponsesLocked()
			}
			continue
		}
		if now.After(p.End.Add(time.Duration(w.Seconds) * time.Second)) {
			if w.Hidden && !now.After(w.UpdatedAt.Add(revocationRetention)) {
				// Expiring old usage must not erase a newer revocation. Keep
				// only its marker; late observations can still carry new periods.
				w.Current, w.Previous, w.Seconds = nil, nil, 0
				w.EstimateExpired = false
				s.quota.Windows[key] = w
			} else {
				delete(s.quota.Windows, key)
			}
			s.quota.VersionCounter++
			s.invalidateCachedResponsesLocked()
			continue
		}
		start := p.Start
		if w.Previous != nil && w.Previous.Start.Before(start) {
			start = w.Previous.Start
		}
		credential := quotaCredentialKey(w.Provider, w.AuthIndex, w.AuthID)
		if old, ok := starts[credential]; !ok || start.Before(old) {
			starts[credential] = start
		}
	}
	changed := false
	for id, f := range s.quota.Facts {
		keepAfter := now.Add(-s.retention)
		if s.retention <= 0 {
			continue
		}
		if start, ok := starts[quotaCredentialKey(f.Provider, f.AuthIndex, f.AuthID)]; ok && start.Before(keepAfter) {
			keepAfter = start
		}
		if f.Timestamp.Before(keepAfter) {
			delete(s.quota.Facts, id)
			changed = true
		}
	}
	for id, at := range s.quota.Deleted {
		keepAfter := now.Add(-s.retention)
		if s.retention <= 0 {
			continue
		}
		for _, start := range starts {
			if start.Before(keepAfter) {
				keepAfter = start
			}
		}
		if at.Before(keepAfter) {
			delete(s.quota.Deleted, id)
		}
	}
	if changed {
		s.quota.VersionCounter++
		s.invalidateCachedResponsesLocked()
	}
}

func (s *RequestStatistics) observeQuotaUsageLocked(record UsageRecord, now time.Time) []quotaObservation {
	if !quotaEligible(requestDetailFromQuotaUsage(record)) {
		return nil
	}
	signals := make(map[string]string)
	for key, values := range record.ResponseHeaders {
		if len(values) > 0 {
			signals[key] = values[len(values)-1]
		}
	}
	observed := record.RequestedAt.Add(record.Latency)
	if record.RequestedAt.IsZero() || record.Latency < 0 || observed.After(now.Add(time.Minute)) {
		observed = now
	}
	input := quotaSignalsInput{Provider: record.Provider, AuthIndex: record.AuthIndex, AuthID: record.AuthID, ObservedAt: observed, Signals: signals}
	var accepted []quotaObservation
	for _, o := range parseQuotaSignals(input) {
		o.CollectionStartedAt = s.ensureQuotaLocked().StartedAt
		if s.applyQuotaObservationLocked(o) {
			accepted = append(accepted, o)
		}
	}
	return accepted
}

func requestDetailFromQuotaUsage(r UsageRecord) RequestDetail {
	return RequestDetail{Provider: r.Provider, AuthIndex: r.AuthIndex, AuthID: r.AuthID, AuthType: r.AuthType}
}

// removeQuotaFactLocked records a tombstone only for an actual accounting deletion.
func (s *RequestStatistics) removeQuotaFactLocked(d RequestDetail) {
	if s.quota == nil || d.RecordID == "" {
		return
	}
	if s.quota.Deleted == nil {
		s.quota.Deleted = make(map[string]time.Time)
	}
	delete(s.quota.Facts, d.RecordID)
	s.quota.Deleted[d.RecordID] = d.Timestamp
	s.quota.VersionCounter++
	s.invalidateCachedResponsesLocked()
}
