package main

import (
	"time"
	"unsafe"
)

const (
	rangeAggregateBlockRecords = 2048
	rangeAggregateMinRecords   = 128
	rangeAggregateCacheBytes   = 8 << 20
	rangeAggregateCacheEntries = 4096
)

// Cache identities never point at record arrays. A cached block must not keep
// an evicted model or a replaced, much larger detail allocation alive.
type rangeBlockIdentity struct{ valid bool }

type rangeAggregateBlock struct {
	identity     *rangeBlockIdentity
	from, end    int
	minimum      time.Time
	maximum      time.Time
	allNonZero   bool
	summaryBytes int64
	detailBytes  int64
}

func (b *rangeAggregateBlock) invalidate() {
	if b.identity == nil {
		return
	}
	b.identity.valid = false
	*b = rangeAggregateBlock{}
}

func invalidateRangeBlocksFrom(blocks []rangeAggregateBlock, from int) {
	for i := max(0, from/rangeAggregateBlockRecords); i < len(blocks); i++ {
		blocks[i].invalidate()
	}
}

func prepareRangeBlock(blocks *[]rangeAggregateBlock, index, from, end int, timestamp func(int) time.Time) *rangeAggregateBlock {
	if index >= len(*blocks) {
		*blocks = append(*blocks, make([]rangeAggregateBlock, index-len(*blocks)+1)...)
	}
	b := &(*blocks)[index]
	if b.identity != nil && b.identity.valid && b.from == from && b.end == end {
		return b
	}
	b.invalidate()
	b.identity = &rangeBlockIdentity{valid: true}
	b.from, b.end, b.allNonZero = from, end, true
	for i := from; i < end; i++ {
		t := timestamp(i)
		if t.IsZero() {
			b.allNonZero = false
		}
		if i == from || t.Before(b.minimum) {
			b.minimum = t
		}
		if i == from || t.After(b.maximum) {
			b.maximum = t
		}
	}
	return b
}

func (m *modelStats) rangeDetailOffset() int {
	return max(0, len(m.detailStorage)-cap(m.Details))
}

func (m *modelStats) invalidateRangeDetailsFrom(index int) {
	invalidateRangeBlocksFrom(m.rangeDetailBlocks, m.rangeDetailOffset()+index)
}

func (m *modelStats) rangeDetailsAreOrdered() bool {
	if !m.rangeDetailOrderKnown {
		m.rangeDetailOrdered = true
		for i := 1; i < len(m.Details); i++ {
			if m.Details[i-1].Timestamp.After(m.Details[i].Timestamp) {
				m.rangeDetailOrdered = false
				break
			}
		}
		m.rangeDetailOrderKnown = true
	}
	return m.rangeDetailOrdered
}

func (s *RequestStatistics) invalidateRangeAggregatesLocked() {
	s.rangeAggregates.clear()
	for _, api := range s.apis {
		if api == nil {
			continue
		}
		for _, model := range api.Models {
			if model == nil {
				continue
			}
			invalidateRangeBlocksFrom(model.rangeDetailBlocks, 0)
			model.rangeDetailBlocks = nil
			model.rangeDetailOrderKnown = false
			invalidateRangeBlocksFrom(model.accounting.rangeBlocks, 0)
			model.accounting.rangeBlocks = nil
		}
	}
}

type rangeAggregateKey struct {
	block     *rangeBlockIdentity
	clientAPI string
	apiDetail bool
}

type rangeAggregateEntry struct {
	data      *rangeSummaryAccumulator
	apiDetail *rangeAPIDetailAccumulator
	bytes     int64
}

type rangeAggregateCache struct {
	entries      map[rangeAggregateKey]rangeAggregateEntry
	bytes        int64
	priceVersion uint64
	hits, misses int64
	evictions    int64
	scannedRows  int64
	sweeps       int64
	swept        bool
}

func (c *rangeAggregateCache) clear() {
	c.evictions += int64(len(c.entries))
	c.entries = nil
	c.bytes = 0
	c.swept = false
}

func (c *rangeAggregateCache) sweepInvalid() {
	c.sweeps++
	c.swept = true
	for key, entry := range c.entries {
		if !key.block.valid {
			delete(c.entries, key)
			c.bytes -= entry.bytes
			c.evictions++
		}
	}
}

func (c *rangeAggregateCache) retain(key rangeAggregateKey, data *rangeSummaryAccumulator) bool {
	return c.retainEntry(key, rangeAggregateEntry{data: data}, data.estimatedBytes())
}

func (c *rangeAggregateCache) retainEntry(key rangeAggregateKey, entry rangeAggregateEntry, estimated int64) bool {
	if _, exists := c.entries[key]; exists {
		return true
	}
	size := estimated + int64(len(key.clientAPI)) + 128
	if !c.canRetain(size) {
		return false
	}
	if c.entries == nil {
		c.entries = make(map[rangeAggregateKey]rangeAggregateEntry)
	}
	entry.bytes = size
	c.entries[key] = entry
	c.bytes += size
	return true
}

func (c *rangeAggregateCache) canRetain(size int64) bool {
	if size > rangeAggregateCacheBytes/4 {
		return false
	}
	if !c.swept && (c.bytes+size > rangeAggregateCacheBytes || len(c.entries) >= rangeAggregateCacheEntries) {
		c.sweepInvalid()
	}
	// Do not evict a useful block to admit the next block in the same scan.
	// A working set larger than the budget would otherwise miss on every
	// query. Stable admitted blocks still help while uncached blocks scan.
	if c.bytes+size > rangeAggregateCacheBytes || len(c.entries) >= rangeAggregateCacheEntries {
		return false
	}
	return true
}

func (s *RequestStatistics) prepareRangeAggregateCacheLocked() {
	// A full cache must not trigger an O(cache entries) sweep for every
	// rejected block. Newly invalidated entries get another chance on the
	// next query; capacity is never exceeded while reclamation is deferred.
	s.rangeAggregates.swept = false
	if s.rangeAggregates.priceVersion != s.priceVersion {
		s.rangeAggregates.clear()
		s.rangeAggregates.priceVersion = s.priceVersion
	}
}

func (s *RequestStatistics) rangeSummaryLocked(cutoff time.Time, clientAPI string) *rangeSummaryAccumulator {
	s.prepareRangeAggregateCacheLocked()
	result := newRangeSummaryAccumulator()
	pricer := queryDetailPricer{stats: s}
	s.forEachRangeBlockLocked("", false, func(api, model string, block *rangeAggregateBlock, from, end int, read func(int) RequestDetail) {
		if block == nil {
			s.scanRangeSummary(result, api, model, from, end, read, cutoff, clientAPI, &pricer)
		} else {
			s.mergeRangeBlock(result, block, api, model, read, cutoff, clientAPI, &pricer)
		}
	})
	return result
}

// Blocks describe stable storage positions and exact time bounds. Both range
// summaries and API detail queries use the same invalidation identities.
func (s *RequestStatistics) forEachRangeBlockLocked(apiFilter string, filterAPI bool, visit func(string, string, *rangeAggregateBlock, int, int, func(int) RequestDetail)) {
	for apiName, api := range s.apis {
		if api == nil || (filterAPI && apiName != apiFilter) {
			continue
		}
		for modelName, model := range api.Models {
			if model == nil {
				continue
			}
			offset := model.rangeDetailOffset()
			readDetail := func(i int) RequestDetail { return model.Details[i-offset] }
			timestamp := func(i int) time.Time { return model.Details[i-offset].Timestamp }
			for from := offset; from < offset+len(model.Details); {
				index := from / rangeAggregateBlockRecords
				end := min((index+1)*rangeAggregateBlockRecords, offset+len(model.Details))
				var block *rangeAggregateBlock
				if end-from >= rangeAggregateMinRecords {
					block = prepareRangeBlock(&model.rangeDetailBlocks, index, from, end, timestamp)
				}
				visit(apiName, modelName, block, from, end, readDetail)
				from = end
			}
			readArchived := func(i int) RequestDetail { return model.accounting.at(i).detail() }
			archivedTime := func(i int) time.Time { return model.accounting.at(i).Timestamp }
			for from := 0; from < model.accounting.count; from += rangeAggregateBlockRecords {
				end := min(from+rangeAggregateBlockRecords, model.accounting.count)
				var block *rangeAggregateBlock
				if end-from >= rangeAggregateMinRecords {
					block = prepareRangeBlock(&model.accounting.rangeBlocks, from/rangeAggregateBlockRecords, from, end, archivedTime)
				}
				visit(apiName, modelName, block, from, end, readArchived)
			}
		}
	}
}

func (s *RequestStatistics) scanRangeSummary(result *rangeSummaryAccumulator, api, model string, from, end int, read func(int) RequestDetail, cutoff time.Time, clientAPI string, pricer *queryDetailPricer) {
	s.rangeAggregates.scannedRows += int64(end - from)
	for i := from; i < end; i++ {
		detail := read(i)
		if !cutoff.IsZero() && (detail.Timestamp.IsZero() || detail.Timestamp.Before(cutoff)) {
			continue
		}
		if clientAPISelectorMatchesDetail(clientAPI, detail) {
			result.add(api, model, detail, pricer)
		}
	}
}

func (s *RequestStatistics) mergeRangeBlock(result *rangeSummaryAccumulator, block *rangeAggregateBlock, api, model string, read func(int) RequestDetail, cutoff time.Time, clientAPI string, pricer *queryDetailPricer) {
	if !cutoff.IsZero() && block.maximum.Before(cutoff) {
		return
	}
	complete := cutoff.IsZero() || (block.allNonZero && !block.minimum.Before(cutoff))
	if !complete || len(clientAPI) > 4096 {
		s.scanRangeSummary(result, api, model, block.from, block.end, read, cutoff, clientAPI, pricer)
		return
	}
	key := rangeAggregateKey{block: block.identity, clientAPI: clientAPI}
	if cached, ok := s.rangeAggregates.entries[key]; ok {
		s.rangeAggregates.hits++
		result.merge(cached.data)
		return
	}
	s.rangeAggregates.misses++
	if clientAPI == "" && block.summaryBytes > 0 && !s.rangeAggregates.canRetain(block.summaryBytes+128) {
		s.scanRangeSummary(result, api, model, block.from, block.end, read, cutoff, clientAPI, pricer)
		return
	}
	data := newRangeSummaryAccumulator()
	s.scanRangeSummary(data, api, model, block.from, block.end, read, time.Time{}, clientAPI, pricer)
	estimated := data.estimatedBytes()
	if clientAPI == "" {
		block.summaryBytes = estimated
	}
	s.rangeAggregates.retainEntry(key, rangeAggregateEntry{data: data}, estimated)
	result.merge(data)
}

// Conservative retained-size accounting includes map overhead, key bytes,
// all nested provider counters, and strings retained from request metadata.
// It is a cache admission budget, not a process-RSS estimate.
func (a *rangeSummaryAccumulator) estimatedBytes() int64 {
	n := int64(unsafe.Sizeof(*a)) + 7*256 + int64(len(a.days))*96
	providers := func(values map[string]*ModelProviderStat) int64 {
		size := int64(0)
		if values != nil {
			size = 256
		}
		for key, value := range values {
			size += 96 + int64(unsafe.Sizeof(*value)) + int64(len(key)+len(value.Provider))
		}
		return size
	}
	for key, api := range a.apiAgg {
		n += 352 + int64(unsafe.Sizeof(*api)) + int64(len(key))
		for model, value := range api.models {
			n += 96 + int64(unsafe.Sizeof(*value)) + int64(len(model)) + providers(value.providerStats)
		}
	}
	for key, value := range a.modelAgg {
		n += 96 + int64(unsafe.Sizeof(*value)) + int64(len(key)+len(value.Model)) + providers(value.providerStats)
	}
	for key, value := range a.sourceAgg {
		n += 96 + int64(unsafe.Sizeof(*value)) + int64(len(key)+len(value.stat.Source)+len(value.stat.Provider))
	}
	for key, value := range a.credentialAgg {
		n += 96 + int64(unsafe.Sizeof(*value)) + int64(len(key)+len(value.AuthIndex))
	}
	for key, value := range a.clientAPIAgg {
		n += 352 + int64(unsafe.Sizeof(key)+unsafe.Sizeof(*value)) + int64(len(value.stat.APIKey)+len(value.stat.APIKeyHash))
		for model, item := range value.models {
			n += 96 + int64(unsafe.Sizeof(*item)) + int64(len(model)+len(item.Model)) + providers(item.providerStats)
			n += int64(cap(item.Providers)) * int64(unsafe.Sizeof(ModelProviderStat{}))
			for _, provider := range item.Providers {
				n += int64(len(provider.Provider))
			}
		}
	}
	return n
}
