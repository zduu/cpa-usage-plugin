package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

const requestMetadataTTL = 30 * time.Minute
const maxRequestMetadata = 4096

var requestMetadata = newRequestMetadataCache()

// The stock host does not include a request ID in usage.handle. Keep only
// bounded request metadata, and enrich only when all matching execution
// intervals agree. Never create usage records from these callbacks.
type requestMetadataObservation struct {
	endExclusive                                  bool
	id, authID, authIndex, model, alias, endpoint string
	stream                                        bool
	start, end                                    time.Time
}

type requestMetadataCache struct {
	mu          sync.Mutex
	byAuth      map[string][]*requestMetadataObservation
	byID        map[string][]*requestMetadataObservation
	count       int
	nextCleanup time.Time
	unsafeUntil time.Time
}

func newRequestMetadataCache() *requestMetadataCache {
	return &requestMetadataCache{
		byAuth: make(map[string][]*requestMetadataObservation),
		byID:   make(map[string][]*requestMetadataObservation),
	}
}

// Deliberately omit body and headers: prompts and client credentials are not
// needed or retained. Field names match the stock CPA request callback ABI.
type requestMetadataRequest struct {
	RequestID      string
	Model          string
	RequestedModel string
	Stream         bool
	CompletedAt    time.Time
	Metadata       map[string]any
}

func handleRequestMetadata(method string, body []byte) ([]byte, error) {
	if method == "request.intercept_before" {
		return okEnvelopeJSON("{}")
	}
	var req requestMetadataRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("invalid request metadata: %w", err)
	}
	now := time.Now()
	if method == "request.complete" {
		requestMetadata.complete(req.RequestID, req.CompletedAt, now)
	} else {
		requestMetadata.observe(req, now)
	}
	return okEnvelopeJSON("{}")
}

func requestMetadataEndpoint(value string) string {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") {
		return ""
	}
	u, err := url.ParseRequestURI(value)
	if err != nil {
		return ""
	}
	// Preserve escaped path segments, but never retain query credentials.
	return u.EscapedPath()
}

func (c *requestMetadataCache) observe(req requestMetadataRequest, now time.Time) {
	o := &requestMetadataObservation{
		id: strings.TrimSpace(req.RequestID), authID: metadataString(req.Metadata, "selected_auth_id"),
		authIndex: metadataString(req.Metadata, "selected_auth_index"),
		model:     strings.TrimSpace(req.Model), alias: strings.TrimSpace(req.RequestedModel),
		endpoint: requestMetadataEndpoint(metadataString(req.Metadata, "request_path")),
		stream:   req.Stream, start: now,
	}
	if o.id == "" || o.authID == "" || o.model == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanupLocked(now)
	if now.Before(c.unsafeUntil) {
		return
	}
	if c.count >= maxRequestMetadata {
		// Dropping an overlapping candidate could turn ambiguity into a false
		// match. Suspend enrichment for the entire retention window instead.
		clear(c.byAuth)
		clear(c.byID)
		c.count = 0
		c.unsafeUntil = now.Add(requestMetadataTTL)
		return
	}
	// A retry begins a new execution interval for this request. Keep the old
	// interval too, since its native report can be delivered asynchronously.
	for _, previous := range c.byID[o.id] {
		if previous.end.IsZero() {
			previous.end = now
			previous.endExclusive = true
		}
	}
	c.byAuth[o.authID] = append(c.byAuth[o.authID], o)
	c.byID[o.id] = append(c.byID[o.id], o)
	c.count++
}

func (c *requestMetadataCache) complete(id string, end, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanupLocked(now)
	if end.IsZero() || end.After(now) {
		return
	}
	for _, o := range c.byID[strings.TrimSpace(id)] {
		if o.end.IsZero() && !end.Before(o.start) {
			o.end = end
		}
	}
}

func (c *requestMetadataCache) enrich(record UsageRecord, now time.Time) UsageRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanupLocked(now)
	if now.Before(c.unsafeUntil) || record.RequestedAt.IsZero() ||
		record.RequestedAt.Before(now.Add(-requestMetadataTTL)) || record.RequestedAt.After(now) {
		return record
	}
	var matched *requestMetadataObservation
	for _, o := range c.byAuth[strings.TrimSpace(record.AuthID)] {
		if record.RequestedAt.Before(o.start) || (!o.end.IsZero() &&
			(record.RequestedAt.After(o.end) || (o.endExclusive && record.RequestedAt.Equal(o.end)))) {
			continue
		}
		if o.authIndex != "" && record.AuthIndex != "" && o.authIndex != record.AuthIndex {
			continue
		}
		if o.model != record.Model && (o.alias == "" || record.Alias == "" || o.alias != record.Alias) {
			continue
		}
		if o.alias != "" && record.Alias != "" && o.alias != record.Alias {
			continue
		}
		if record.Endpoint != "" && record.Endpoint != o.endpoint {
			continue
		}
		if matched != nil && (matched.endpoint != o.endpoint || matched.stream != o.stream) {
			return record
		}
		matched = o
	}
	if matched != nil {
		if record.Endpoint == "" {
			record.Endpoint = matched.endpoint
		}
		if !record.streamPresent {
			record.Stream = matched.stream
		}
	}
	return record
}

func (c *requestMetadataCache) cleanupLocked(now time.Time) {
	if now.Before(c.nextCleanup) {
		return
	}
	c.nextCleanup = now.Add(time.Minute)
	cutoff := now.Add(-requestMetadataTTL)
	for auth, entries := range c.byAuth {
		kept := entries[:0]
		for _, o := range entries {
			if o.end.IsZero() && o.start.Before(cutoff) {
				// A long-running execution can still create reporters. Losing
				// its metadata must not make another request appear unique.
				clear(c.byAuth)
				clear(c.byID)
				c.count = 0
				c.unsafeUntil = now.Add(requestMetadataTTL)
				return
			}
			if o.end.IsZero() || !o.end.Before(cutoff) {
				kept = append(kept, o)
			}
		}
		clear(entries[len(kept):])
		if len(kept) == 0 {
			delete(c.byAuth, auth)
		} else {
			c.byAuth[auth] = kept
		}
	}
	clear(c.byID)
	c.count = 0
	for _, entries := range c.byAuth {
		for _, o := range entries {
			c.byID[o.id] = append(c.byID[o.id], o)
			c.count++
		}
	}
}
