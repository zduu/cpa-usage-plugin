#!/usr/bin/env python3
"""Load the candidate in an isolated, unmodified local CPA v8 SDK and HTTP server."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import time

GO_FIXTURE = r'''
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"
)

const key = "quota-isolated-fixture-key"
const prefix = "/v0/management/plugins/usage-dashboard-zduu"

func require(ok bool, message string) {
	if !ok {
		panic(message)
	}
}
func main() {
	root, phase := os.Args[1], os.Args[2]
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hash, _ := bcrypt.GenerateFromPassword([]byte(key), bcrypt.MinCost)
	enabled := true
	var raw yaml.Node
	yaml.Unmarshal([]byte(fmt.Sprintf("enabled: true\nstorage_enabled: true\nstorage_path: %s\nprice_storage_path: %s\nexchange_rate_enabled: false\nmax_details_per_model: 1\n", filepath.Join(root, "data", "usage.jsonl"), filepath.Join(root, "prices.json"))), &raw)
	cfg := &config.Config{Host: "127.0.0.1", AuthDir: filepath.Join(root, "auth"), RemoteManagement: config.RemoteManagement{SecretKey: string(hash), AllowRemote: true, DisableControlPanel: true, DisableAutoUpdatePanel: true}, Plugins: config.PluginsConfig{Enabled: true, Dir: filepath.Join(root, "plugins"), Configs: map[string]config.PluginInstanceConfig{"usage-dashboard-zduu": {Enabled: &enabled, Raw: *raw.Content[0]}}}}
	manager := auth.NewManager(nil, nil, nil)
	now := time.Now().UTC().Truncate(time.Second)
	if phase == "restore" {
		b, _ := os.ReadFile(filepath.Join(root, "anchor.json"))
		json.Unmarshal(b, &now)
	} else {
		b, _ := json.Marshal(now)
		os.WriteFile(filepath.Join(root, "anchor.json"), b, 0600)
	}
	identities := map[string]*auth.Auth{}
	for _, provider := range []string{"claude", "codex", "devin"} {
		id := provider + "-fixture.json"
		path := filepath.Join(cfg.AuthDir, id)
		os.MkdirAll(cfg.AuthDir, 0700)
		os.WriteFile(path, []byte(`{"type":"`+provider+`"}`), 0600)
		a := &auth.Auth{ID: id, FileName: id, Provider: provider, Status: auth.StatusActive, Attributes: map[string]string{"path": path, "auth_kind": "oauth"}, Metadata: map[string]any{"type": provider, "auth_kind": "oauth"}}
		if provider == "devin" {
			a.Quota = auth.QuotaState{ObservedAt: now, Signals: map[string]string{"weekly_quota_remaining_percent": "60%", "weekly_quota_reset_at": now.Add(time.Hour).Format(time.RFC3339)}}
		}
		registered, err := manager.Register(ctx, a)
		if err != nil {
			panic(err)
		}
		identities[provider] = registered
	}
	host := pluginhost.New()
	host.SetAuthManager(manager)
	host.ApplyConfig(ctx, cfg)
	defer host.ShutdownAll()
	require(host.PluginLoaded("usage-dashboard-zduu"), "candidate was not loaded")
	server := api.NewServer(cfg, manager, nil, filepath.Join(root, "config.yaml"), api.WithPluginHost(host))
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	request := func(method, path string, body any, authorized bool) (int, map[string]any, http.Header) {
		var b []byte
		if body != nil {
			b, _ = json.Marshal(body)
		}
		req, _ := http.NewRequest(method, httpServer.URL+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		if authorized {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			panic(err)
		}
		defer resp.Body.Close()
		payload, _ := io.ReadAll(resp.Body)
		result := map[string]any{}
		if len(payload) > 0 {
			json.Unmarshal(payload, &result)
		}
		return resp.StatusCode, result, resp.Header
	}
	if phase != "restore" {
		for model, price := range map[string]float64{"old-model": 30, "current-a": 20, "current-b": 5} {
			status, _, _ := request("PUT", prefix+"/model-prices", map[string]any{"model": model, "price": map[string]float64{"prompt": price}}, true)
			require(status == 200, "price setup failed")
		}
		usage.StartDefault(ctx)
		host.RegisterUsagePlugins()
		defer usage.StopDefault()
		publish := func(provider, model, id string, at time.Time, headers http.Header) {
			a := identities[provider]
			usage.PublishRecord(ctx, usage.Record{RequestID: id, Provider: provider, AuthID: a.ID, AuthIndex: a.Index, AuthType: "oauth", Source: provider + "-fixture", Model: model, RequestedAt: at, Latency: time.Second, Detail: usage.Detail{InputTokens: 1000000, TotalTokens: 1000000}, ResponseHeaders: headers})
		}
		start := now.Add(-30 * time.Minute)
		claudeHeaders := func(reset time.Time, used string) http.Header {
			return http.Header{"Anthropic-Ratelimit-Unified-5h-Utilization": {used}, "Anthropic-Ratelimit-Unified-5h-Reset": {fmt.Sprint(reset.Unix())}}
		}
		publish("claude", "old-model", "old", start.Add(-time.Hour), claudeHeaders(start, "0.6"))
		publish("claude", "current-a", "current-a", now.Add(-5*time.Minute), claudeHeaders(start.Add(5*time.Hour), "0.3"))
		publish("claude", "current-b", "current-b", now.Add(-time.Minute), claudeHeaders(start.Add(5*time.Hour), "0.4"))
		publish("codex", "weekly-model", "weekly", now.Add(-time.Minute), http.Header{"X-Codex-Primary-Used-Percent": {"10"}, "X-Codex-Primary-Window-Minutes": {"10080"}, "X-Codex-Primary-Reset-At": {fmt.Sprint(now.Add(time.Hour).Unix())}})
		publish("devin", "devin-model", "devin", now.Add(-time.Minute), nil)
	}
	var summary map[string]any
	for i := 0; i < 500; i++ {
		status, value, _ := request("GET", prefix+"/dashboard-summary", nil, true)
		if status == 200 {
			summary = value
			if u, ok := value["usage"].(map[string]any); ok && u["total_requests"] == float64(5) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	require(summary != nil, "summary unavailable")
	require(summary["usage"].(map[string]any)["total_requests"] == float64(5), "request totals changed")
	status, credentials, _ := request("GET", "/v8/management/credentials", nil, true)
	require(status == 200, "v8 credentials unavailable")
	if phase == "seed" {
		var observations []any
		for _, entry := range credentials["files"].([]any) {
			f := entry.(map[string]any)
			q := f["quota"].(map[string]any)
			if q["observed_at"] != nil {
				observations = append(observations, map[string]any{"provider": f["provider"], "auth_index": f["auth_index"], "auth_id": f["id"], "observed_at": q["observed_at"], "signals": q["signals"]})
			}
		}
		status, result, _ := request("POST", prefix+"/dashboard-quota-observations", map[string]any{"version": 1, "observations": observations}, true)
		require(status == 200 && result["accepted"] == float64(1), "live host auth bridge rejected the credential observation")
	}
	apis := summary["usage"].(map[string]any)["apis"].(map[string]any)
	keys := map[string]string{}
	for name := range apis {
		status, detail, _ := request("GET", prefix+"/dashboard-api-detail?api="+urlEscape(name), nil, true)
		require(status == 200, "detail unavailable")
		if phase == "browser" && detail["credential_quota_cycles"] == nil {
			keys["devin"] = name
			continue
		}
		for _, c := range detail["credential_quota_cycles"].([]any) {
			credential := c.(map[string]any)
			provider := credential["provider"].(string)
			keys[provider] = name
			groups := credential["groups"].([]any)
			if provider == "codex" {
				require(len(groups) == 1 && groups[0].(map[string]any)["window_seconds"] == float64(604800), "invented a Codex 5h quota")
			}
			if provider == "claude" {
				g := groups[0].(map[string]any)
				old := g["previous"].(map[string]any)
				current := g["current"].(map[string]any)
				require(old["summary"].(map[string]any)["estimated_cost"] == float64(30), "previous actual cost changed")
				require(current["summary"].(map[string]any)["estimated_cost"] == float64(25), "current actual cost changed")
				if old["used_percent"] == float64(100) {
					require(old["actual_total_usd"] == float64(30) && old["estimated_total_usd"] == nil, "full previous amount changed")
				} else {
					require(old["estimated_total_usd"] == float64(50), "previous estimate must equal recorded cost / used fraction")
				}
				if current["used_percent"] == float64(100) {
					require(current["actual_total_usd"] == float64(25) && current["estimated_total_usd"] == nil && current["estimated_remaining_usd"] == float64(0), "full current amount changed")
				} else {
					require(current["estimated_total_usd"] == float64(62.5) && current["estimated_remaining_usd"] == float64(37.5), "current estimate must equal recorded cost / used fraction")
				}
			}
		}
	}
	status, _, _ = request("GET", prefix+"/dashboard-api-detail?api="+urlEscape(keys["claude"]), nil, false)
	require(status == 401, "anonymous management data accepted")
	status, _, _ = request("POST", "/v0/resource/plugins/usage-dashboard-zduu/dashboard-quota-observations", map[string]any{}, false)
	require(status == 404, "anonymous resource data accepted")
	if phase == "restore" {
		host.ShutdownAll()
		host.ApplyConfig(ctx, cfg)
		server.RefreshPluginManagementRoutes()
		require(host.PluginLoaded("usage-dashboard-zduu"), "plugin did not reload after shutdown")
		status, reloaded, _ := request("GET", prefix+"/dashboard-summary", nil, true)
		require(status == 200 && reloaded["usage"].(map[string]any)["total_requests"] == float64(5), "reload changed request totals")
		status, job, _ := request("POST", prefix+"/usage/export-jobs", nil, true)
		require(status == 202, "export manager did not restart after reload")
		id := job["id"].(string)
		for i := 0; i < 500; i++ {
			status, job, _ = request("GET", prefix+"/usage/export-jobs?id="+urlEscape(id), nil, true)
			require(status == 200, "reload export status failed")
			if job["status"] == "succeeded" {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		require(job["status"] == "succeeded", "reload export did not finish")
		status, _, _ = request("DELETE", prefix+"/usage/export-jobs?id="+urlEscape(id), nil, true)
		require(status == 200, "reload export cleanup failed")
	}
	metadata := map[string]any{"base": httpServer.URL, "key": key, "apis": keys, "phase": phase}
	b, _ := json.Marshal(metadata)
	os.WriteFile(filepath.Join(root, "ready.json"), b, 0600)
	fmt.Println("QUOTA_V8_READY")
	if phase == "browser" {
		for i := 0; i < 3000; i++ {
			if _, err := os.Stat(filepath.Join(root, "stop")); err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	fmt.Println("PASS: CPA v8 native SDK, credential bridge, HTTP auth, current/previous models, weekly-only Codex, persisted restart")
}
func urlEscape(s string) string {
	var b bytes.Buffer
	for _, v := range []byte(s) {
		if (v >= 'a' && v <= 'z') || (v >= 'A' && v <= 'Z') || (v >= '0' && v <= '9') {
			b.WriteByte(v)
		} else {
			fmt.Fprintf(&b, "%%%02X", v)
		}
	}
	return b.String()
}
'''

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--cpa", type=Path, required=True)
    parser.add_argument("--plugin", type=Path, required=True)
    parser.add_argument("--browser", action="store_true")
    parser.add_argument("--restarts", type=int, default=1)
    args = parser.parse_args()
    args.cpa = args.cpa.resolve()
    args.plugin = args.plugin.resolve()
    if args.restarts < 1:
        parser.error("--restarts must be positive")
    root = Path(__file__).resolve().parents[1]
    result = root / "docs/validation/quota-cycle-details-v8"
    result.mkdir(parents=True, exist_ok=True)
    manifest_path = result / ("browser-manifest.json" if args.browser else "http-manifest.json")
    manifest_path.unlink(missing_ok=True)
    with tempfile.TemporaryDirectory(prefix="cpa-quota-v8-") as data:
        fixture = Path(tempfile.mkdtemp(prefix=".quota-fixture-", dir=args.cpa))
        try:
            (fixture / "main.go").write_text(GO_FIXTURE)
            (Path(data) / "plugins").mkdir()
            shutil.copyfile(args.plugin, Path(data) / ("plugins/usage-dashboard-zduu" + args.plugin.suffix))
            env = dict(os.environ, GOTOOLCHAIN="go1.26.6")
            executable = fixture / "quota-fixture"
            with (result / "build.log").open("w") as log:
                subprocess.run(["go", "build", "-o", str(executable), str(fixture / "main.go")],
                               cwd=args.cpa, env=env, stdout=log, stderr=subprocess.STDOUT, check=True)
            phases = ["browser" if args.browser else "seed"] + ["restore"] * args.restarts
            for phase_index, phase in enumerate(phases):
                ready = Path(data) / "ready.json"
                ready.unlink(missing_ok=True)
                log_name = phase if phase_index < 2 else f"restore-{phase_index}"
                with (result / (log_name + ".log")).open("w") as log:
                    process = subprocess.Popen([str(executable), data, phase], cwd=args.cpa, env=env, stdout=log, stderr=subprocess.STDOUT)
                    try:
                        if phase == "browser":
                            deadline = time.monotonic() + 180
                            while not ready.exists() and process.poll() is None and time.monotonic() < deadline:
                                time.sleep(.2)
                            if not ready.exists():
                                raise RuntimeError("CPA v8 fixture did not become ready; see " + str(result / (log_name + ".log")))
                            subprocess.run(["node", str(root / "scripts/browser-quota-v8.cjs"), str(ready), str(result)], env=env, check=True)
                            (Path(data) / "stop").touch()
                        if process.wait(timeout=180) != 0:
                            raise RuntimeError("CPA v8 validation failed; see " + str(result / (log_name + ".log")))
                    finally:
                        if process.poll() is None:
                            process.terminate()
                            process.wait(timeout=10)
                print("PASS:", phase)
        finally:
            shutil.rmtree(fixture)
    manifest = {
        "passed": True,
        "browser": args.browser,
        "plugin_sha256": hashlib.sha256(args.plugin.read_bytes()).hexdigest(),
        "plugin_revision": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip(),
        "working_tree_modified": bool(subprocess.check_output(["git", "status", "--porcelain"], cwd=root, text=True).strip()),
        "cpa_revision": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=args.cpa, text=True).strip(),
        "toolchain": subprocess.check_output(["go", "version"], env=env, text=True).strip(),
        "phases": phases,
    }
    manifest_path.write_text(json.dumps(manifest, indent=2) + "\n")


if __name__ == "__main__":
    main()
