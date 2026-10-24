package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConversationOwnerCLIPaths(t *testing.T) {
	dir := t.TempDir()
	var path, method string
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-conversation-token" || r.Header.Get("X-Coop-Expected-Master-ID") != "fixture-master" {
			t.Error("missing authenticated identity")
		}
		path, method = r.URL.RequestURI(), r.Method
		if r.Method == "POST" {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
		}
		w.Header().Set("X-Coop-Master-ID", "fixture-master")
		_ = json.NewEncoder(w).Encode(map[string]any{"master_id": "fixture-master", "restore_epoch": 1, "data": map[string]any{}})
	}))
	defer server.Close()
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte("fixture-conversation-token"), 0600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "client.toml")
	if err := os.WriteFile(config, []byte("url = \""+server.URL+"\"\nmaster_id = \"fixture-master\"\ntoken_file = \""+token+"\"\nallow_loopback_http = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(dir, "observed.json")
	if err := os.WriteFile(input, []byte(`{"observed":{"restore_epoch":2,"accepted_epoch":1,"instance_uid":"native","project_uid":"project","after_event_id":12},"reason":"reviewed backup","request_id":"recover"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		args         []string
		path, method string
	}{
		{[]string{"cursor"}, "/api/v1/conversation-cursor", "GET"},
		{[]string{"legacy", "thread-one", "--after", "14"}, "/api/v1/threads/thread-one/legacy-messages?after=14", "GET"},
		{[]string{"map", "--thread", "thread-one", "--instance", "native", "--project", "project", "--issue", "issue", "--after-event", "12", "--request-id", "map"}, "/api/v1/conversation-sources", "POST"},
		{[]string{"reaccept", "--input", input}, "/api/v1/conversation-cursor/reaccept", "POST"},
	} {
		t.Run(strings.Join(test.args[:1], ""), func(t *testing.T) {
			root := root()
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs(append([]string{"--master-config", config, "master", "conversation"}, test.args...))
			if err := root.Execute(); err != nil {
				t.Fatal(err, out.String())
			}
			if path != test.path || method != test.method {
				t.Fatal(path, method)
			}
			if test.args[0] == "map" && body["after_event_id"] != float64(12) {
				t.Fatal(body)
			}
		})
	}
}
