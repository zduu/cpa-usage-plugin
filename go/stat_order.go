package main

import (
	"cmp"
	"slices"
)

// Map traversal is intentionally unspecified. Break equal-count ties by the
// stable dimension identity so a refresh does not randomly reorder the page.
func sortDashboardModelStats(stats []ModelStat) {
	slices.SortStableFunc(stats, func(a, b ModelStat) int {
		if order := cmp.Compare(b.TotalRequests, a.TotalRequests); order != 0 {
			return order
		}
		return cmp.Compare(a.Model, b.Model)
	})
}

func sortDashboardSourceStats(stats []SourceStat) {
	slices.SortStableFunc(stats, func(a, b SourceStat) int {
		if order := cmp.Compare(b.TotalRequests, a.TotalRequests); order != 0 {
			return order
		}
		return cmp.Compare(a.Source, b.Source)
	})
}

func sortDashboardCredentialStats(stats []CredentialStat) {
	slices.SortStableFunc(stats, func(a, b CredentialStat) int {
		if order := cmp.Compare(b.TotalRequests, a.TotalRequests); order != 0 {
			return order
		}
		return cmp.Compare(a.AuthIndex, b.AuthIndex)
	})
}

func sortAPIDetailErrorStats(stats []APIDetailErrorStat) {
	slices.SortStableFunc(stats, func(a, b APIDetailErrorStat) int {
		if order := cmp.Compare(b.Count, a.Count); order != 0 {
			return order
		}
		if order := cmp.Compare(a.StatusCode, b.StatusCode); order != 0 {
			return order
		}
		return cmp.Compare(a.Failure, b.Failure)
	})
}
