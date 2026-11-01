package worker

import (
	"encoding/json"
	"os"
	"testing"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

const pr9Assignment = "49d021b8e3bbb8ed034ff3abc8a90d066b7ada6098c64a2f8ef01cfa022f1384"
const pr9Scope = "d61fa88d444ab3c5b662149aa47f8ca6935f196367f5d8c4687dc908c382b9fb"
const pr9SourcePolicy = "05bf02a92869ae26d8d8f5bdcab01523703ca824b592b314a79ea993d2a525eb"
const pr9Head = "6a3efb78904ea8e1ff47af2d85b4e772eec190c0"
const pr9Base = "de9b32cace62e9a97b3ed86428fc17bc62338993"
const pr9Artifact = "9e4e061ebafbf3d2f94c9d46561dd55f00ad9dc902665a8dc850b616c9fda4c0"
const pr9Task = "kata://01m69m973pwdtsmg3gyrqm6r74/01M69NJWQXXBT2PQWX307ZFPFH/sarcasticbird/gh-sweep"
const pr9URL = "https://github.com/sarcasticbird/gh-sweep/pull/9"

type pr9RecordedRead struct {
	Endpoint string
	Input    json.RawMessage
	Body     []byte
}

// This schema describes external private input, not a repository fixture. Never
// add original task/goal/report bodies or encoded captures to public testdata.
type pr9FixtureData struct {
	Task         c.ProviderTask
	Authority    c.GoalAuthority
	SourceReport c.Message
	PR           PR
	Reads        []pr9RecordedRead
}

func TestRetainedPR9PublicArchiveDigest(t *testing.T) {
	archive, err := os.ReadFile("testdata/retained_pr9/source.tar.gz")
	if err != nil {
		t.Fatal("public source archive unavailable")
	}
	sha, err := sourceBundleSHA(archive)
	if err != nil || sha != pr9Artifact {
		t.Fatal("public immutable source archive digest mismatch")
	}
}

func TestRetainedPR9PublicFixtureHasNoPrivateCapture(t *testing.T) {
	entries, err := os.ReadDir("testdata/retained_pr9")
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"README.md": true, "source.tar.gz": true}
	for _, entry := range entries {
		if entry.IsDir() || !allowed[entry.Name()] {
			t.Fatal("unapproved fixture entry: private captures must remain outside the repository")
		}
		delete(allowed, entry.Name())
	}
	if len(allowed) != 0 {
		t.Fatal("public fixture incomplete")
	}
}
