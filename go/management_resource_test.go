package main

import (
	"net/http"
	"testing"
)

func TestManagementRejectsResourceDataAliases(t *testing.T) {
	// A host may still have a resource registration cached from an older
	// plugin. Never trust a supplied header to authenticate that public path.
	for _, path := range []string{
		"usage", "usage/export", "usage/import", "usage/export-jobs", "usage/export-download",
		"dashboard-data", "dashboard-summary", "dashboard-events", "dashboard-api-detail",
		"dashboard-events-export", "dashboard-events-export-jobs", "dashboard-events-export-download",
		"model-prices", "health",
	} {
		for _, method := range []string{"GET", "POST", "PUT", "DELETE"} {
			for _, headers := range []map[string][]string{nil, {"Authorization": {"Bearer synthetic-wrong-key"}}} {
				request := ManagementRequest{Method: method, Path: "/v0/resource/plugins/" + pluginID + "/" + path, Headers: headers}
				response := decodeManagementResponse(t, invokeManagement(t, request), nil)
				if response.StatusCode != http.StatusNotFound {
					t.Fatalf("resource alias bypassed management routing: %s %s status=%d", method, path, response.StatusCode)
				}
			}
		}
	}
}

func TestManagementResourceDashboardStillLoadsWithoutCredentials(t *testing.T) {
	response := decodeManagementResponse(t, invokeManagement(t, ManagementRequest{
		Method: "GET", Path: "/v0/resource/plugins/" + pluginID + "/dashboard",
	}), nil)
	if response.StatusCode != http.StatusOK || len(response.Body) == 0 {
		t.Fatal("static dashboard bootstrap became unavailable")
	}
}
