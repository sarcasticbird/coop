package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitHubCacheStatusReadsOnly(t *testing.T) {
	reads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/github-cache/status" {
			t.Error("status attempted effect", r.Method, r.URL.Path)
		}
		reads++
		w.Header().Set("X-Coop-Master-ID", "master")
		_ = json.NewEncoder(w).Encode(map[string]any{"master_id": "master", "restore_epoch": 1, "data": c.GitHubCacheStatus{Version: 1, Mode: "off"}})
	}))
	defer srv.Close()
	cfg := filepath.Join(t.TempDir(), "client.toml")
	t.Setenv("CACHE_TEST_TOKEN", "test-token-for-cache")
	if e := os.WriteFile(cfg, fmt.Appendf(nil, "url=%q\nmaster_id='master'\ntoken_env='CACHE_TEST_TOKEN'\nallow_loopback_http=true\n", srv.URL), 0600); e != nil {
		t.Fatal(e)
	}
	cmd := root()
	found, _, e := cmd.Find([]string{"cache", "status"})
	if e != nil || found.Name() != "status" {
		t.Fatal("cache status command is not registered")
	}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--master-config", cfg, "cache", "status", "--engagement", "coop"})
	if e := cmd.Execute(); e != nil {
		t.Fatal(e)
	}
	if reads != 1 || !strings.Contains(out.String(), `"mode":"off"`) || strings.Contains(out.String(), "test-token") {
		t.Fatal(out.String(), reads)
	}
}
