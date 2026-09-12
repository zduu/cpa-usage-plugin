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
	m.accounting.append(m.accountingRecord(d))
}

func (m *modelStats) accountingRecord(d RequestDetail) accountingRecord {
	key := accountingIdentity{d.Model, d.Provider, d.Source, d.AuthIndex, d.AuthID, d.AuthType, d.APIKey, d.APIKeyHash, d.BaseURL, d.RequestedModel, d.ExecutorType, d.Endpoint}
	identity := m.internAccountingIdentity(key)
	return accountingRecord{Correlation: cloneProtocolCorrelationMeta(d.Correlation), Timestamp: d.Timestamp, Identity: identity, Tokens: d.Tokens,
		LatencyMs: d.LatencyMs, TTFTMs: d.TTFTMs, Failure: d.Failure, StatusCode: d.StatusCode, Failed: d.Failed, Synthetic: d.TimestampSynthetic}
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
			if m == nil {
				continue
			}
			for i := range m.Details {
				if !yield(dashboardEventDetail{detail: &m.Details[i], upstreamAPI: api, modelName: name}) {
					return
				}
			}
			for r := range m.accounting.records() {
				d := r.detail()
				if !yield(dashboardEventDetail{detail: &d, upstreamAPI: api, modelName: name}) {
					return
				}
			}
		}
	}
}

func (m *modelStats) accountingCount() int { return len(m.Details) + m.accounting.count }

func (m *modelStats) accountingDetailAt(i int) RequestDetail {
	if i < len(m.Details) {
		return m.Details[i]
	}
	return m.accounting.at(i - len(m.Details)).detail()
}

func (m *modelStats) accountingSnapshot() []RequestDetail {
	if m.accounting.count == 0 {
		return nil
	}
	result := make([]RequestDetail, 0, m.accounting.count)
	for r := range m.accounting.records() {
		result = append(result, cloneRequestDetail(r.detail()))
	}
	return result
}

func (m *modelStats) pruneAccounting(s *RequestStatistics, api *apiStats, model string, cutoff time.Time) bool {
	if cutoff.IsZero() || m.accounting.count == 0 {
		return false
	}
	kept, scanned := 0, 0
	for r := range m.accounting.records() {
		if !r.Timestamp.IsZero() && r.Timestamp.Before(cutoff) {
			s.decrementCounters(r.detail(), api, m, model)
		} else {
			if kept != scanned {
				*m.accounting.mutableAt(kept) = r
			}
			kept++
		}
		scanned++
	}
	changed := kept != m.accounting.count
	if changed {
		m.accounting.truncate(kept)
		m.accountingIdentities = make(accountingIdentityIndex)
		for i := 0; i < kept; i++ {
			r := m.accounting.mutableAt(i)
			r.Identity = m.internAccountingIdentity(*r.Identity)
		}
	}
	if kept == 0 {
		m.accountingIdentities = nil
	}
	for r := range m.accounting.records() {
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
	*m.accounting.mutableAt(i - len(m.Details)) = m.accountingRecord(d)
}

func (m *modelStats) removeAccountingDetailAt(i int) {
	if i < len(m.Details) {
		if ref := m.Details[i].eventRef; ref != nil {
			ref.detail = nil
		}
		copy(m.Details[i:], m.Details[i+1:])
		m.Details[len(m.Details)-1] = RequestDetail{}
		m.Details = m.Details[:len(m.Details)-1]
		if len(m.Details) == 0 {
			m.detailStorage = nil
		}
		m.rebindEventRefs(i)
		return
	}
	i -= len(m.Details)
	m.accounting.remove(i)
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
	// Trimming advances Details through its allocation. Reclaim the cleared
	// prefix once the tail fills instead of allocating/copying the full visible
	// window again. The allocation budget is unchanged; all moved event refs
	// are rebound below before any query can observe them.
	rebased := false
	if len(m.Details) == cap(m.Details) && len(m.detailStorage) > len(m.Details) {
		length := len(m.Details)
		copy(m.detailStorage, m.Details)
		clear(m.detailStorage[length:])
		m.Details = m.detailStorage[:length]
		rebased = true
	}
	reallocated := len(m.Details) == cap(m.Details)
	m.Details = append(m.Details, d)
	if reallocated || m.detailStorage == nil {
		m.detailStorage = m.Details[:cap(m.Details)]
	}
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
	if reallocated || rebased {
		changedFrom = 0
	}
	m.rebindEventRefs(changedFrom)
}

// Bulk import, a smaller detail limit or expiry can leave a tiny visible
// window retaining a much larger allocation. Ordinary rolling eviction keeps
// its reusable capacity; only substantial underuse triggers a bounded copy.
func (m *modelStats) compactDetailStorage(previousCapacity int) {
	if len(m.Details) == 0 {
		m.Details, m.detailStorage = nil, nil
		return
	}
	capacity := max(previousCapacity, len(m.detailStorage))
	if capacity < 1024 || len(m.Details) > capacity/4 {
		return
	}
	// Leave append headroom so the first new request does not immediately
	// allocate and copy the window again.
	details := make([]RequestDetail, len(m.Details), len(m.Details)+max(1, len(m.Details)/4))
	copy(details, m.Details)
	m.Details = details
	m.detailStorage = details[:cap(details)]
	m.rebindEventRefs(0)
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
