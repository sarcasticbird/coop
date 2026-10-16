package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func registrationScope(t *testing.T, raw string) Registration {
	t.Helper()
	r := testRegistration()
	var i Installation
	if err := json.Unmarshal([]byte(raw), &i); err != nil {
		t.Fatal(err)
	}
	r.Installations["code"] = i
	return r
}

func TestRegistrationSelectedRepositories(t *testing.T) {
	for _, tc := range []struct {
		scope string
		valid bool
	}{
		{`{"id":10,"repositories":"selected","repository_ids":[101,202],"permissions":{"contents":"write"}}`, true},
		{`{"id":10,"repositories":"selected","permissions":{"contents":"write"}}`, false},
		{`{"id":10,"repositories":"selected","repository_ids":[101,101],"permissions":{"contents":"write"}}`, false},
		{`{"id":10,"repositories":"selected","repository_ids":[0],"permissions":{"contents":"write"}}`, false},
		{`{"id":10,"repositories":"selected","repository_ids":[-1],"permissions":{"contents":"write"}}`, false},
		{`{"id":10,"repositories":"all","repository_ids":[101],"permissions":{"contents":"write"}}`, false},
	} {
		err := registrationScope(t, tc.scope).Validate()
		if (err == nil) != tc.valid {
			t.Fatalf("valid=%v scope=%s: %v", tc.valid, tc.scope, err)
		}
	}
}

func TestMintSelectedRepositories(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	now := time.Now().UTC()
	for _, problem := range []string{"", "large", "installation-all", "selection-all", "missing", "extra", "duplicate", "wrong-owner", "projects-read"} {
		t.Run(problem, func(t *testing.T) {
			ids := []int64{101, 202}
			if problem == "large" {
				ids = make([]int64, 500)
				for i := range ids {
					ids[i] = int64(i + 1)
				}
			}
			tokenCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch req.URL.Path {
				case "/app":
					_, _ = fmt.Fprint(w, `{"id":1,"slug":"example","owner":{"login":"team"}}`)
				case "/app/installations":
					selection := "selected"
					if problem == "installation-all" {
						selection = "all"
					}
					_, _ = fmt.Fprintf(w, `[{"id":10,"app_id":1,"account":{"login":"code"},"repository_selection":%q,"permissions":{"contents":"write","metadata":"read"}},{"id":20,"app_id":1,"account":{"login":"team"},"repository_selection":"all","permissions":{"organization_projects":"write","metadata":"read"}}]`, selection)
				case "/app/installations/10/access_tokens":
					tokenCalls++
					var body struct {
						IDs         []int64           `json:"repository_ids"`
						Permissions map[string]string `json:"permissions"`
					}
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if !reflect.DeepEqual(body.IDs, ids) || body.Permissions["contents"] != "write" {
						t.Errorf("unexpected scope request: %+v", body)
					}
					selection := "selected"
					repos := `[{"id":202,"owner":{"login":"code"}},{"id":101,"owner":{"login":"code"}}]`
					switch problem {
					case "large":
						rows := make([]map[string]any, 0, len(ids))
						for _, id := range ids {
							rows = append(rows, map[string]any{"id": id, "owner": map[string]string{"login": "code"}, "description": strings.Repeat("x", 2200)})
						}
						raw, err := json.Marshal(rows)
						if err != nil {
							t.Error(err)
						}
						repos = string(raw)
					case "selection-all":
						selection = "all"
					case "missing":
						repos = `[{"id":101,"owner":{"login":"code"}}]`
					case "extra":
						repos = `[{"id":101,"owner":{"login":"code"}},{"id":202,"owner":{"login":"code"}},{"id":303,"owner":{"login":"code"}}]`
					case "duplicate":
						repos = `[{"id":101,"owner":{"login":"code"}},{"id":101,"owner":{"login":"code"}}]`
					case "wrong-owner":
						repos = `[{"id":101,"owner":{"login":"other"}},{"id":202,"owner":{"login":"code"}}]`
					}
					_, _ = fmt.Fprintf(w, `{"token":"fixture-code","expires_at":%q,"permissions":{"contents":"write","metadata":"read"},"repository_selection":%q,"repositories":%s}`, now.Add(time.Hour).Format(time.RFC3339), selection, repos)
				case "/app/installations/20/access_tokens":
					var body struct {
						Permissions map[string]string `json:"permissions"`
						IDs         []int64           `json:"repository_ids"`
					}
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if body.Permissions["organization_projects"] != "write" || len(body.IDs) != 0 {
						t.Errorf("planner scope changed: %+v", body)
					}
					access := "write"
					if problem == "projects-read" {
						access = "read"
					}
					_, _ = fmt.Fprintf(w, `{"token":"fixture-team","expires_at":%q,"permissions":{"organization_projects":%q,"metadata":"read"}}`, now.Add(time.Hour).Format(time.RFC3339), access)
				default:
					t.Errorf("unexpected endpoint %s", req.URL.Path)
					http.Error(w, "unexpected", 500)
				}
			}))
			defer server.Close()
			r := registrationScope(t, `{"id":10,"repositories":"selected","repository_ids":[101,202],"permissions":{"contents":"write","metadata":"read"}}`)
			i := r.Installations["code"]
			i.RepositoryIDs = ids
			r.Installations["code"] = i
			client := Client{HTTP: server.Client(), BaseURL: server.URL, Now: func() time.Time { return now }, ReadKey: func(string) ([]byte, error) { return keyPEM, nil }}
			bundle, err := client.Mint(context.Background(), r)
			if problem != "" && problem != "large" {
				if err == nil {
					t.Fatal("unsafe scoped mint succeeded")
				}
				if problem == "installation-all" && tokenCalls != 0 {
					t.Fatal("minted token before rejecting installation")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tokenCalls != 1 || len(bundle.Status().Installations) != 2 {
				t.Fatal("scoped mint did not preserve both owners")
			}
		})
	}
}
