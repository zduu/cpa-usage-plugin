package main

import (
	"sort"
	"unsafe"
)

const (
	dashboardEventCacheByteBudget = 8 << 20
	dashboardEventIndexByteBudget = 32 << 20
	dashboardEventIndexMaxKeys    = 32
	dashboardEventPendingMax      = 4096
)

// A pointer into a slice is not a stable identity: late insertion, append
// reallocation and retention all move its values. Indexes share this indirection
// instead. Removing a visible detail clears the pointer before releasing it.
type dashboardEventRef struct {
	detail   *RequestDetail
	sequence int64
}

func (d dashboardEventDetail) detailPointer() *RequestDetail {
	if d.ref != nil {
		return d.ref.detail
	}
	return d.detail
}

func (m *modelStats) rebindEventRefs(start int) {
	if m == nil || !m.hasEventRefs {
		return
	}
	for i := start; i < len(m.Details); i++ {
		if ref := m.Details[i].eventRef; ref != nil {
			ref.detail = &m.Details[i]
		}
	}
}

// Restored arrays have no runtime identities. Initialize the entire model
// before the first filtered query or append: assigning only matching records
// would mix old and new slice positions after late insertion or retention.
func (m *modelStats) initializeEventSequences() {
	if m.nextEventSequence != 0 {
		return
	}
	for i := range m.Details {
		m.Details[i].eventSequence = int64(i + 1)
	}
	m.nextEventSequence = int64(len(m.Details))
}

func (m *modelStats) eventAt(api, model string, i int) dashboardEventDetail {
	m.initializeEventSequences()
	d := &m.Details[i]
	if d.eventRef == nil {
		d.eventRef = &dashboardEventRef{sequence: d.eventSequence}
	}
	d.eventRef.detail = d
	m.hasEventRefs = true
	return dashboardEventDetail{ref: d.eventRef, upstreamAPI: api, sortKey: api, modelName: model, sequence: d.eventSequence}
}

func (s *RequestStatistics) hasDashboardEventIndexesLocked() bool {
	return s.eventIndex != nil || len(s.eventAPIIndex)+len(s.eventModelIndex)+len(s.eventSourceIndex)+len(s.eventAuthIndex) > 0
}

func (s *RequestStatistics) clearDashboardEventIndexesLocked() {
	s.eventIndexVersion = 0
	s.eventIndex = nil
	s.eventAPIIndex = nil
	s.eventModelIndex = nil
	s.eventSourceIndex = nil
	s.eventAuthIndex = nil
	s.eventIndexPending = nil
	s.eventIndexDirty = false
}

func (s *RequestStatistics) queueDashboardEventLocked(api, model string, ref *dashboardEventRef) {
	if ref == nil || !s.hasDashboardEventIndexesLocked() {
		return
	}
	if len(s.eventIndexPending) >= dashboardEventPendingMax {
		// A dashboard that stopped querying must not leave an unbounded update
		// log. Its next query can rebuild the indexes it actually needs.
		s.clearDashboardEventIndexesLocked()
		return
	}
	previousCapacity := cap(s.eventIndexPending)
	s.eventIndexPending = append(s.eventIndexPending, dashboardEventDetail{ref: ref, upstreamAPI: api, sortKey: api, modelName: model, sequence: ref.sequence})
	if cap(s.eventIndexPending) != previousCapacity && s.dashboardEventIndexBytesLocked() > dashboardEventIndexByteBudget {
		// Enforce the shared budget when queue capacity grows, including while
		// no dashboard queries arrive to apply the pending updates.
		s.clearDashboardEventIndexesLocked()
		return
	}
	s.eventIndexDirty = true
}

func mergeDashboardEventIndex(events, pending []dashboardEventDetail, matches func(dashboardEventDetail) bool) []dashboardEventDetail {
	kept := events[:0]
	for _, event := range events {
		if event.detailPointer() != nil {
			kept = append(kept, event)
		}
	}
	clear(events[len(kept):])
	var added []dashboardEventDetail
	for _, event := range pending {
		if event.detailPointer() != nil && (matches == nil || matches(event)) {
			added = append(added, event)
		}
	}
	oldLen := len(kept)
	kept = append(kept, added...)
	i, j := oldLen-1, len(added)-1
	for at := len(kept) - 1; j >= 0; at-- {
		if i >= 0 && dashboardEventBefore(added[j], kept[i]) {
			kept[at] = kept[i]
			i--
		} else {
			kept[at] = added[j]
			j--
		}
	}
	return kept
}

func (s *RequestStatistics) applyDashboardEventUpdatesLocked() {
	if !s.eventIndexDirty {
		return
	}
	pending := s.eventIndexPending
	sort.Slice(pending, func(i, j int) bool { return dashboardEventBefore(pending[i], pending[j]) })
	if s.eventIndex != nil {
		s.eventIndex = mergeDashboardEventIndex(s.eventIndex, pending, nil)
	}
	for key, events := range s.eventAPIIndex {
		s.eventAPIIndex[key] = mergeDashboardEventIndex(events, pending, func(e dashboardEventDetail) bool { return e.upstreamAPI == key })
	}
	for key, events := range s.eventModelIndex {
		s.eventModelIndex[key] = mergeDashboardEventIndex(events, pending, func(e dashboardEventDetail) bool { return dashboardEventModelKey(e) == key })
	}
	for key, events := range s.eventSourceIndex {
		s.eventSourceIndex[key] = mergeDashboardEventIndex(events, pending, func(e dashboardEventDetail) bool { return dashboardEventSourceKey(e) == key })
	}
	for key, events := range s.eventAuthIndex {
		s.eventAuthIndex[key] = mergeDashboardEventIndex(events, pending, func(e dashboardEventDetail) bool { return dashboardEventAuthKey(e) == key })
	}
	clear(s.eventIndexPending)
	s.eventIndexPending = s.eventIndexPending[:0]
	s.eventIndexDirty = false
	if s.dashboardEventIndexBytesLocked() > dashboardEventIndexByteBudget {
		s.clearDashboardEventIndexesLocked()
		s.eventIndexVersion = s.summaryVersion
	}
}

// This is the actual reserved backing-array space, including every cached
// index (not just the largest). Referenced record/string storage is shared.
func (s *RequestStatistics) dashboardEventIndexBytesLocked() int64 {
	entries := cap(s.eventIndex) + cap(s.eventIndexPending)
	for _, indexes := range []map[string][]dashboardEventDetail{s.eventAPIIndex, s.eventModelIndex, s.eventSourceIndex, s.eventAuthIndex} {
		for _, events := range indexes {
			entries += cap(events)
		}
	}
	return int64(entries) * int64(unsafe.Sizeof(dashboardEventDetail{}))
}

func (s *RequestStatistics) admitDashboardEventIndexLocked(events []dashboardEventDetail) bool {
	keys := len(s.eventAPIIndex) + len(s.eventModelIndex) + len(s.eventSourceIndex) + len(s.eventAuthIndex)
	return keys < dashboardEventIndexMaxKeys && s.dashboardEventIndexBytesLocked()+int64(cap(events))*int64(unsafe.Sizeof(dashboardEventDetail{})) <= dashboardEventIndexByteBudget
}

// Conservatively charge the copied structs/maps and referenced string bytes.
// Go's allocator/map overhead is implementation dependent; report this as an
// estimate, separately from the exact index backing-array reservation above.
func eventResultEstimatedBytes(result EventsResult) int64 {
	n := int64(unsafe.Sizeof(result)) + int64(len(result.Events))*int64(unsafe.Sizeof(RequestDetail{})) + int64(len(result.GeneratedAt))
	for _, d := range result.Events {
		n += int64(len(d.UpstreamAPI) + len(d.Model) + len(d.RequestedModel) + len(d.APIKey) + len(d.APIKeyHash) + len(d.Source) + len(d.Provider) + len(d.ExecutorType) + len(d.AuthID) + len(d.AuthIndex) + len(d.AuthType) + len(d.Endpoint) + len(d.BaseURL) + len(d.Failure))
		n += int64(len(d.Thinking.Intensity) + len(d.Thinking.Mode) + len(d.Thinking.Level))
		if d.Correlation != nil {
			n += int64(unsafe.Sizeof(*d.Correlation))
			n += int64(len(d.Correlation.InputMode) + len(d.Correlation.OutputMode) + len(d.Correlation.CacheMode))
		}
		if d.CostUSD != nil {
			n += 8
		}
		if d.Headers != nil {
			n += 64
		}
		for key, values := range d.Headers {
			n += int64(128 + len(key) + cap(values)*16)
			for _, value := range values {
				n += int64(len(value))
			}
		}
	}
	return n
}

func eventCacheEntryEstimatedBytes(key dashboardEventCacheKey, result EventsResult) int64 {
	// The key is retained by both the lookup map and the eviction order.
	return eventResultEstimatedBytes(result) + 2*int64(unsafe.Sizeof(key)) +
		int64(len(key.rangeKey)+len(key.model)+len(key.source)+len(key.authIndex)+len(key.api)+len(key.clientAPI))
}
