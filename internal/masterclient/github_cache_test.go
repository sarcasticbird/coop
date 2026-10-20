package masterclient

import (
	"context"
	"errors"
	"fmt"
	c "github.com/sarcasticbird/coop/internal/coordination"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGitHubCacheClientSnapshotPinning(t *testing.T) {
	for _, valid := range []bool{true, false} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/identity" && valid {
				w.Header().Set("X-Coop-Master-ID", "master")
				_, _ = fmt.Fprint(w, `{"master_id":"master","restore_epoch":1,"data":{"restore_epoch":1}}`)
				return
			}
			http.NotFound(w, r)
		}))
		cl, e := New(ClientConfig{URL: srv.URL, MasterID: "master", Token: "test-only-token", AllowHTTP: true})
		if e != nil {
			t.Fatal(e)
		}
		_, e = cl.GitHubCacheCapabilities(context.Background(), "coop")
		if valid && !errors.Is(e, c.ErrUnsupported) {
			t.Fatal(e)
		}
		if !valid && !errors.Is(e, c.ErrConflict) {
			t.Fatal("impersonator", e)
		}
		srv.Close()
	}
}
