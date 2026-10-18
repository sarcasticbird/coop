package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigVersionEnvelope(t *testing.T) {
	c := configFixture(t)
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	document := string(raw)
	path := filepath.Join(filepath.Dir(c.StateDir), "versioned-config.json")
	for name, body := range map[string]string{
		"v1 team null":                document[:len(document)-1] + `,"team":null}`,
		"v1 master null":              document[:len(document)-1] + `,"master":null}`,
		"v1 case variant master null": document[:len(document)-1] + `,"Master":null}`,
		"v1 case variant team null":   document[:len(document)-1] + `,"Team":null}`,
		"v2 then v1":                  strings.Replace(document, `"version":1`, `"version":2,"version":1`, 1),
		"v2 then v1 with null team":   strings.Replace(document[:len(document)-1]+`,"team":null}`, `"version":1`, `"version":2,"version":1`, 1),
		"case variant versions":       strings.Replace(document, `"version":1`, `"version":2,"Version":1`, 1),
		"duplicate v1":                strings.Replace(document, `"version":1`, `"version":1,"version":1`, 1),
		"v1 then v2":                  strings.Replace(document, `"version":1`, `"version":1,"version":2`, 1),
		"missing version":             strings.Replace(document, `"version":1,`, ``, 1),
		"null version":                strings.Replace(document, `"version":1`, `"version":null`, 1),
		"unknown version":             strings.Replace(document, `"version":1`, `"version":3`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfig(path); err == nil {
				t.Fatal("ambiguous version or v2-only field accepted")
			}
		})
	}
	// The envelope must not change historical v1 handling of ordinary fields.
	for _, body := range []string{document, strings.Replace(document, `"name":"builder"`, `"name":"old","name":"builder"`, 1)} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := LoadConfig(path)
		if err != nil || got.Version != 1 || got.Name != c.Name {
			t.Fatalf("v1 positive control: config=%+v err=%v", got, err)
		}
	}
}
