package main

import (
	"reflect"
	"testing"
	"time"
)

func TestDashboardEqualCountOrderingSurvivesCacheRebuild(t *testing.T) {
	s := NewRequestStatistics()
	now := time.Now()
	for _, name := range []string{"zeta", "beta", "alpha", "mu"} {
		s.recordDetailLocked("api", name, RequestDetail{
			Model: name, Source: name, AuthIndex: name, APIKey: name, APIKeyHash: name,
			Timestamp: now, Failed: true, StatusCode: 503, Failure: name,
			Tokens: TokenStats{InputTokens: 10},
		}, requestDedupKey{}, now, false)
	}
	expected := []string{"alpha", "beta", "mu", "zeta"}
	for pass := 0; pass < 40; pass++ {
		s.invalidateSummaryLocked()
		for _, window := range []string{"all", "24h"} {
			summary := s.SummaryWithoutDetailsForRangeAt(window, now)
			models, sources, credentials, clients := []string{}, []string{}, []string{}, []string{}
			for _, value := range summary.ModelStats {
				models = append(models, value.Model)
			}
			for _, value := range summary.SourceStats {
				sources = append(sources, value.Source)
			}
			for _, value := range summary.CredentialStats {
				credentials = append(credentials, value.AuthIndex)
			}
			for _, value := range summary.ClientAPIStats {
				clients = append(clients, value.APIKey)
			}
			for kind, got := range map[string][]string{"models": models, "sources": sources, "credentials": credentials, "clients": clients} {
				if !reflect.DeepEqual(got, expected) {
					t.Fatalf("pass=%d range=%s %s order=%v, want %v", pass, window, kind, got, expected)
				}
			}
			api := s.QueryAPIDetailAt("api", window, 2, 2, now)
			models, sources = models[:0], sources[:0]
			for _, value := range api.ModelStats {
				models = append(models, value.Model)
			}
			for _, value := range api.SourceStats {
				sources = append(sources, value.Source)
			}
			if !reflect.DeepEqual(models, expected) || !reflect.DeepEqual(sources, expected) {
				t.Fatalf("API detail ordering changed: models=%v sources=%v", models, sources)
			}
			if len(api.ErrorStats) != 2 || api.ErrorStats[0].Failure != "alpha" || api.ErrorStats[1].Failure != "beta" {
				t.Fatalf("equal-count error selection changes: %+v", api.ErrorStats)
			}
		}
	}
}
