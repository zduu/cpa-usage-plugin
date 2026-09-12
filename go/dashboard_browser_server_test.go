package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Opt-in loopback bridge for an actual browser, backed by the production
// handlers and embedded page. No mock API responses, external CPA, user data,
// model-price workers or upstream requests are used. ABI loading is tested by
// tests/stock-host separately; this bridge does not emulate CPA authentication.
func TestDashboardBrowserServer(t *testing.T) {
	if os.Getenv("CPA_BROWSER_TEST") != "1" {
		t.Skip("set CPA_BROWSER_TEST=1 via browser-dashboard.cjs")
	}
	previous, previousJobs := stats, dashboardExportJobs
	stats = NewRequestStatistics()
	dashboardExportJobs = newDashboardExportJobManager()
	defer func() {
		dashboardExportJobs.close()
		stats.Close()
		stats, dashboardExportJobs = previous, previousJobs
	}()
	stats.maxDetailsPerModel = 12000
	stats.dedupWindow = 0
	stats.priceStoragePath = filepath.Join(t.TempDir(), "prices.json")
	now := time.Now()
	models := []string{"browser-a", "browser-b", "中文🙂"}
	for i := 0; i < 12000; i++ {
		tokens := int64(100 + i)
		if i == 0 {
			tokens = 9007199254740993
		}
		d := RequestDetail{Model: models[i%3], Provider: "browser", Timestamp: now.Add(time.Duration(i-12000) * time.Second), APIKey: "client***", APIKeyHash: fmt.Sprint(i % 2), Source: "browser-source", AuthIndex: "browser-auth", Tokens: TokenStats{InputTokens: tokens, OutputTokens: 3, TotalTokens: tokens + 3}, LatencyMs: 100, TTFTMs: 20, Endpoint: "/v1/messages", Stream: true}
		stats.recordDetailLocked("browser-api", d.Model, d, requestDedupKey{}, now, false)
	}
	done := make(chan struct{})
	var once sync.Once
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__stop" && r.Method == http.MethodPost {
			once.Do(func() { close(done) })
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/favicon.ico" {
			w.WriteHeader(204)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		raw, err := handleManagement(mustMarshal(ManagementRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Headers: r.Header, Body: body}))
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil || !env.OK {
			http.Error(w, string(raw), 500)
			return
		}
		var response ManagementResponse
		if err := json.Unmarshal(env.Result, &response); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		for key, values := range response.Headers {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		if strings.HasSuffix(r.URL.Path, "/dashboard") {
			w.Header().Set("Cache-Control", "no-store")
		}
		w.WriteHeader(response.StatusCode)
		_, _ = w.Write(response.Body)
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	fmt.Printf("CPA_BROWSER_URL=http://%s/v0/management/plugins/usage-dashboard-zduu/dashboard\n", listener.Addr())
	select {
	case <-done:
	case <-time.After(2 * time.Minute):
		t.Fatal("browser verification did not finish")
	}
}
