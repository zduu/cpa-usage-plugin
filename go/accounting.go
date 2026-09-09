package main

import (
	"iter"
	"sort"
	"time"
)

// Accounting survives the visible-detail limit. Keep only the fields needed
// for statistics, pricing, identity and expiry; never keep headers or transport
// payloads. Repeated dimensions are interned within a model.
type accountingIdentity struct {
	Model, Provider, Source, AuthIndex, AuthID, AuthType, APIKey, APIKeyHash, BaseURL, RequestedModel, ExecutorType, Endpoint string
}

type accountingRecord struct {
	Correlation       *ProtocolCorrelationMeta
	Timestamp         time.Time
	Identity          *accountingIdentity
	Tokens            TokenStats
	LatencyMs, TTFTMs int64
	Failure           string
	StatusCode        int
	Failed, Synthetic bool
}

func (m *modelStats) archiveDetail(d RequestDetail) {
	key := accountingIdentity{d.Model, d.Provider, d.Source, d.AuthIndex, d.AuthID, d.AuthType, d.APIKey, d.APIKeyHash, d.BaseURL, d.RequestedModel, d.ExecutorType, d.Endpoint}
	if m.accountingIdentities == nil {
		m.accountingIdentities = make(map[accountingIdentity]*accountingIdentity)
	}
	identity := m.accountingIdentities[key]
	if identity == nil {
		identity = &key
		m.accountingIdentities[key] = identity
	}
	m.Accounting = append(m.Accounting, accountingRecord{Correlation: cloneProtocolCorrelationMeta(d.Correlation), Timestamp: d.Timestamp, Identity: identity, Tokens: d.Tokens,
		LatencyMs: d.LatencyMs, TTFTMs: d.TTFTMs, Failure: d.Failure, StatusCode: d.StatusCode, Failed: d.Failed, Synthetic: d.TimestampSynthetic})
}

func (r accountingRecord) detail() RequestDetail {
	i := r.Identity
	return RequestDetail{Model: i.Model, Provider: i.Provider, Source: i.Source, AuthIndex: i.AuthIndex, AuthID: i.AuthID, AuthType: i.AuthType, BaseURL: i.BaseURL, RequestedModel: i.RequestedModel, ExecutorType: i.ExecutorType, Endpoint: i.Endpoint, Correlation: r.Correlation,
		APIKey: i.APIKey, APIKeyHash: i.APIKeyHash, Timestamp: r.Timestamp, Tokens: r.Tokens, LatencyMs: r.LatencyMs, TTFTMs: r.TTFTMs,
		Failure: r.Failure, StatusCode: r.StatusCode, Failed: r.Failed, TimestampSynthetic: r.Synthetic}
}

// Aggregations see both populations; recent event tables only see Details.
func apiAccountingEvents(api string, a *apiStats) iter.Seq[dashboardEventDetail] {
	return func(yield func(dashboardEventDetail) bool) {
		if a == nil {
			return
		}
		for name, m := range a.Models {
			for i := range m.Details {
				if !yield(dashboardEventDetail{detail: &m.Details[i], upstreamAPI: api, modelName: name}) {
					return
				}
			}
			for _, r := range m.Accounting {
				d := r.detail()
				if !yield(dashboardEventDetail{detail: &d, upstreamAPI: api, modelName: name, accounting: true}) {
					return
				}
			}
		}
	}
}

func (m *modelStats) accountingCount() int { return len(m.Details) + len(m.Accounting) }

func (m *modelStats) accountingDetailAt(i int) RequestDetail {
	if i < len(m.Details) {
		return m.Details[i]
	}
	return m.Accounting[i-len(m.Details)].detail()
}

func (m *modelStats) accountingSnapshot() []RequestDetail {
	if len(m.Accounting) == 0 {
		return nil
	}
	result := make([]RequestDetail, len(m.Accounting))
	for i, r := range m.Accounting {
		result[i] = cloneRequestDetail(r.detail())
	}
	return result
}

func (m *modelStats) pruneAccounting(s *RequestStatistics, api *apiStats, model string, cutoff time.Time) bool {
	if cutoff.IsZero() || len(m.Accounting) == 0 {
		return false
	}
	kept := m.Accounting[:0]
	for _, r := range m.Accounting {
		if !r.Timestamp.IsZero() && r.Timestamp.Before(cutoff) {
			s.decrementCounters(r.detail(), api, m, model)
		} else {
			kept = append(kept, r)
		}
	}
	changed := len(kept) != len(m.Accounting)
	clear(m.Accounting[len(kept):])
	m.Accounting = kept
	if changed {
		m.accountingIdentities = make(map[accountingIdentity]*accountingIdentity)
		for _, r := range kept {
			m.accountingIdentities[*r.Identity] = r.Identity
		}
	}
	if len(kept) == 0 {
		m.Accounting = nil
		m.accountingIdentities = nil
	}
	for _, r := range kept {
		expires := r.Timestamp.Add(s.retention)
		if s.nextDetailExpiry.IsZero() || expires.Before(s.nextDetailExpiry) {
			s.nextDetailExpiry = expires
		}
	}
	return changed
}

func (m ModelSnapshot) accountingDetails() []RequestDetail {
	if len(m.Accounting) == 0 {
		return m.Details
	}
	result := make([]RequestDetail, 0, len(m.Details)+len(m.Accounting))
	result = append(result, m.Details...)
	return append(result, m.Accounting...)
}

// Combined indexes keep archived records addressable during repair and replay.
func (m *modelStats) setAccountingDetailAt(i int, d RequestDetail) {
	if i < len(m.Details) {
		d.eventRef = m.Details[i].eventRef
		d.eventSequence = m.Details[i].eventSequence
		m.Details[i] = d
		return
	}
	m.archiveDetail(d)
	m.Accounting[i-len(m.Details)] = m.Accounting[len(m.Accounting)-1]
	m.Accounting[len(m.Accounting)-1] = accountingRecord{}
	m.Accounting = m.Accounting[:len(m.Accounting)-1]
}

func (m *modelStats) removeAccountingDetailAt(i int) {
	if i < len(m.Details) {
		if ref := m.Details[i].eventRef; ref != nil {
			ref.detail = nil
		}
		copy(m.Details[i:], m.Details[i+1:])
		m.Details[len(m.Details)-1] = RequestDetail{}
		m.Details = m.Details[:len(m.Details)-1]
		m.rebindEventRefs(i)
		return
	}
	i -= len(m.Details)
	copy(m.Accounting[i:], m.Accounting[i+1:])
	m.Accounting[len(m.Accounting)-1] = accountingRecord{}
	m.Accounting = m.Accounting[:len(m.Accounting)-1]
}

func (m *modelStats) appendDetail(d RequestDetail, archived bool) {
	m.lastEventRef = nil
	if archived {
		m.archiveDetail(d)
		return
	}
	m.initializeEventSequences()
	m.nextEventSequence++
	d.eventSequence = m.nextEventSequence
	if d.eventRef != nil {
		d.eventRef.sequence = d.eventSequence
	}
	reallocated := len(m.Details) == cap(m.Details)
	m.Details = append(m.Details, d)
	last := len(m.Details) - 1
	changedFrom := last
	if last > 0 && m.Details[last-1].Timestamp.After(d.Timestamp) {
		at := sort.Search(last, func(i int) bool { return m.Details[i].Timestamp.After(d.Timestamp) })
		copy(m.Details[at+1:], m.Details[at:last])
		m.Details[at] = d
		changedFrom = at
	}
	if d.eventRef != nil {
		m.hasEventRefs = true
		m.lastEventRef = d.eventRef
	}
	if reallocated {
		changedFrom = 0
	}
	m.rebindEventRefs(changedFrom)
}

func (m ModelSnapshot) accountingCount() int { return len(m.Details) + len(m.Accounting) }
func (m ModelSnapshot) accountingDetailAt(i int) RequestDetail {
	if i < len(m.Details) {
		return m.Details[i]
	}
	return m.Accounting[i-len(m.Details)]
}
func (m *ModelSnapshot) setAccountingDetailAt(i int, d RequestDetail) {
	if i < len(m.Details) {
		m.Details[i] = d
	} else {
		m.Accounting[i-len(m.Details)] = d
	}
}

func (s *RequestStatistics) countAccountingLocked() int64 {
	var count int64
	for _, api := range s.apis {
		for _, model := range api.Models {
			count += int64(model.accountingCount())
		}
	}
	return count
}
