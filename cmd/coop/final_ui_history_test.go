package main

import (
	"encoding/json"
	"fmt"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/hostrouter"
	"github.com/sarcasticbird/coop/internal/taskstore"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFinalReviewNativeEntryWithValidHistory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	project, issue := "01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	var comments []map[string]any
	for i := 0; i < 9; i++ {
		comments = append(comments, map[string]any{"uid": fmt.Sprintf("01M5MSA8FZCCD7DWMTP6RW%04d", i), "body": strings.Repeat("x", c.MaxText), "author": "human"})
	}
	raw, err := json.Marshal(map[string]any{"issue": map[string]string{"uid": issue, "project_uid": project}, "comments": comments})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "show.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "kata")
	script := fmt.Sprintf("#!/bin/sh\ncase \"$*\" in\n*'daemon locate'*) echo '{\"kind\":\"local\",\"source\":\"local_default\",\"network\":\"unix\",\"address\":\"unix:///tmp/kata.sock\"}';;\n*'projects show'*) echo '{\"project\":{\"uid\":\"%s\"}}';;\n*'show '*' --inbound coop-inspect-'*) echo '{\"issue\":{\"uid\":\"%s\",\"project_uid\":\"%s\"}}';;\n*'show '*) cat show.json;;\nesac\n", project, issue, project)
	if err = os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := taskstore.Config{Provider: "kata", Binary: binary, Home: dir, ProjectName: "fixture", ProjectUID: project, PolicySHA: strings.Repeat("a", 64)}
	req := hostrouter.WorkUIRequest{MasterID: "master-a", ProjectUID: project, IssueUID: issue}
	if _, err = nativeWorkUICommand(t.Context(), req, "master-a", cfg); err != nil {
		t.Fatalf("valid issue history (%d bytes, 9 MaxText comments) prevents native TUI entry: %v", len(raw), err)
	}
}
