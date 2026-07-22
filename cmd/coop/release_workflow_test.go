package main

import (
	"os"
	"strings"
	"testing"
)

func TestReleaseWorkflowCreatesDistBeforeBuild(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(data)
	mkdirIndex := strings.Index(workflow, "mkdir -p dist")
	buildIndex := strings.Index(workflow, "-o dist/coop")
	if mkdirIndex < 0 || buildIndex < 0 || mkdirIndex > buildIndex {
		t.Fatalf("release workflow must create dist before building dist/coop")
	}
}
