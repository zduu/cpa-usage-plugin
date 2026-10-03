package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type quotaHostAuth struct {
	ID          string `json:"id"`
	AuthIndex   string `json:"auth_index"`
	Provider    string `json:"provider"`
	RuntimeOnly bool   `json:"runtime_only"`
	AccountType string `json:"account_type"`
}

var resolveQuotaHostAuth = callQuotaHostAuth

func handleQuotaObservations(body []byte) ([]byte, error) {
	if len(body) > 256*1024 {
		return dashboardExportJobJSON(http.StatusRequestEntityTooLarge, map[string]string{"error": "quota observation batch too large"})
	}
	var batch struct {
		Version      int                 `json:"version"`
		Observations []quotaSignalsInput `json:"observations"`
	}
	if json.Unmarshal(body, &batch) != nil || batch.Version != 1 || len(batch.Observations) > 100 {
		return dashboardExportJobJSON(http.StatusBadRequest, map[string]string{"error": "invalid quota observation batch"})
	}
	accepted, skipped, rejected := 0, 0, 0
	now := time.Now()
	var persist []persistedDetail
	for _, input := range batch.Observations {
		input.Provider = strings.ToLower(strings.TrimSpace(input.Provider))
		if !quotaProvider(input.Provider) || input.AuthIndex == "" || input.AuthID == "" || len(input.AuthID) > 512 || len(input.AuthIndex) > 512 || len(input.Signals) > 64 || input.ObservedAt.IsZero() || input.ObservedAt.After(now.Add(5*time.Minute)) {
			rejected++
			continue
		}
		auth, err := resolveQuotaHostAuth(input.AuthIndex)
		if err != nil || auth.RuntimeOnly || !quotaEligible(RequestDetail{Provider: auth.Provider, AuthID: auth.ID, AuthType: auth.AccountType}) || auth.Provider != input.Provider || auth.ID != input.AuthID || auth.AuthIndex != input.AuthIndex {
			rejected++
			continue
		}
		observations := parseQuotaSignals(input)
		if len(observations) == 0 {
			rejected++
			continue
		}
		stats.mu.Lock()
		changed := false
		for _, o := range observations {
			o.CollectionStartedAt = stats.ensureQuotaLocked().StartedAt
			if stats.applyQuotaObservationLocked(o) {
				changed = true
				if stats.storageEnabled {
					persist = append(persist, persistedDetail{Kind: "quota", QuotaObservations: []quotaObservation{o}})
				}
			}
		}
		stats.pruneQuotaLocked(now)
		stats.mu.Unlock()
		if changed {
			accepted++
		} else {
			skipped++
		}
	}
	for _, row := range persist {
		stats.enqueueStorageDetail(row)
	}
	version := stats.DashboardVersion()
	return dashboardExportJobJSON(http.StatusOK, struct {
		Accepted int    `json:"accepted"`
		Skipped  int    `json:"skipped"`
		Rejected int    `json:"rejected"`
		Version  uint64 `json:"quota_version"`
	}{accepted, skipped, rejected, version})
}
