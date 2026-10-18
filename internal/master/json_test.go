package master

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStrictMasterJSON(t *testing.T) {
	for _, body := range []string{`{"body":"a","body":"b"}`, `{"body":"a","BODY":"b"}`, "{\"body\":\"\xff\"}", `{"unknown":1}`, `{} {}`} {
		var out struct {
			Body string `json:"body"`
		}
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		if err := decode(httptest.NewRecorder(), r, &out); err == nil {
			t.Errorf("ambiguous JSON accepted: %q", body)
		}
	}
}
