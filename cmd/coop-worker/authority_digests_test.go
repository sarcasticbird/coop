package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/worker"
)

func TestNativeAuthorityDigestsReadOnly(t *testing.T) {
	input := filepath.Join(t.TempDir(), "pending.json")
	if err := os.WriteFile(input, []byte(`{"kind":"tag-only","tag":"v0.5.0"}`), 0600); err != nil {
		t.Fatal(err)
	}
	archive := "../../internal/worker/testdata/retained_pr9/source.tar.gz"
	for _, args := range [][]string{{"--input", input}, {"--archive", archive, "--payload-schema", c.TagOnlyGHSweepPayloadV1}, {"--input", input, "--archive", archive, "--payload-schema", c.TagOnlyGHSweepPayloadV1}} {
		var out bytes.Buffer
		if err := nativeCommand(append([]string{"authority-digests"}, args...), &out); err != nil {
			t.Fatal(err)
		}
		var got worker.NativeAuthorityDigestResult
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if args[0] == "--input" && len(got.InputSHA) != 64 {
			t.Fatal(got)
		}
		if len(args) > 2 && (len(got.PayloadSHA) != 64 || got.SourceBundleSHA != "9e4e061ebafbf3d2f94c9d46561dd55f00ad9dc902665a8dc850b616c9fda4c0") {
			t.Fatal(got)
		}
	}
	before, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := nativeAuthorityDigestsCommand([]string{"--input", input}, &out); err != nil {
		t.Fatal(err)
	}
	var result worker.NativeAuthorityDigestResult
	if json.Unmarshal(out.Bytes(), &result) != nil {
		t.Fatal("invalid JSON")
	}
	expected, err := worker.NativeAuthorityDigests(&c.HandoffInput{Kind: "tag-only", Tag: "v0.5.0"}, nil, "")
	if err != nil || result != expected {
		t.Fatal("CLI did not use native canonical schema", err)
	}
	after, err := os.ReadFile(input)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("digest command wrote its input")
	}
}

func TestNativeAuthorityDigestsRejectsInvalidInput(t *testing.T) {
	for _, content := range []string{`{"unknown":true}`, `{"kind":"tag-only"} {}`, `{"kind":"deliver","kind":"tag-only"}`, `null`, `[]`, `not-json`} {
		file := filepath.Join(t.TempDir(), "bad.json")
		if err := os.WriteFile(file, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if err := nativeAuthorityDigestsCommand([]string{"--input", file}, &bytes.Buffer{}); err == nil {
			t.Fatal("invalid schema accepted", content)
		}
	}
	for _, args := range [][]string{nil, {"--archive", "missing"}, {"--payload-schema", c.TagOnlyGHSweepPayloadV1}, {"--archive", "missing", "--payload-schema", "arbitrary"}, {"--config", "unused"}} {
		if err := nativeAuthorityDigestsCommand(args, &bytes.Buffer{}); err == nil {
			t.Fatal("invalid flags accepted", args)
		}
	}
	file := filepath.Join(t.TempDir(), "public.json")
	if err := os.WriteFile(file, []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := nativeAuthorityDigestsCommand([]string{"--input", file}, &bytes.Buffer{}); err == nil {
		t.Fatal("non-private authority input accepted")
	}
}
