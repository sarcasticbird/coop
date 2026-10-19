package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func TestTeamJSONAndWatchReadOnly(t *testing.T) {
	for _, watch := range []bool{false, true} {
		t.Run(fmt.Sprint("watch=", watch), func(t *testing.T) {
			var reads atomic.Int32
			requested := make(chan struct{}, 1)
			rows := []c.Presence{{Placement: c.Placement{AgentID: "hawk", HostID: "rack-mini", InstanceID: "worker", Generation: 1}, Alias: "Hawk", State: "busy", Reason: "native_work stage=implement task=fixture-task"}}
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/api/v1/team" {
					t.Errorf("not read-only Team: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(400)
					return
				}
				if r.Header.Get("Authorization") != "Bearer local-fixture-secret" || r.Header.Get("X-Coop-Expected-Master-ID") != "fixture-master" {
					t.Error("missing existing master authentication")
				}
				reads.Add(1)
				w.Header().Set("X-Coop-Master-ID", "fixture-master")
				_ = json.NewEncoder(w).Encode(map[string]any{"master_id": "fixture-master", "restore_epoch": 1, "data": rows})
				select {
				case requested <- struct{}{}:
				default:
				}
			}))
			defer ts.Close()
			cfg := filepath.Join(t.TempDir(), "client.toml")
			t.Setenv("COOP_TEAM_TEST_TOKEN", "local-fixture-secret")
			if err := os.WriteFile(cfg, fmt.Appendf(nil, "url = %q\nmaster_id = %q\ntoken_env = %q\nallow_loopback_http = true\n", ts.URL, "fixture-master", "COOP_TEAM_TEST_TOKEN"), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := root()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			args := []string{"--master-config", cfg, "team"}
			if watch {
				args = append(args, "--watch")
				reader, writer := io.Pipe()
				defer func() { _ = reader.Close() }()
				defer func() { _ = writer.Close() }()
				cmd.SetIn(reader)
				go func() {
					select {
					case <-requested:
						_, _ = writer.Write([]byte("q"))
					case <-time.After(5 * time.Second):
						_ = writer.Close()
					}
				}()
			}
			cmd.SetArgs(args)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if reads.Load() != 1 {
				t.Fatalf("unexpected reads: %d", reads.Load())
			}
			if strings.Contains(out.String(), "local-fixture-secret") {
				t.Fatal("credential in output")
			}
			if !watch {
				want, _ := json.Marshal(rows)
				if out.String() != string(want)+"\n" {
					t.Fatalf("JSON compatibility: %s", out.String())
				}
			}
		})
	}
}
