package coordination

import (
	"strings"
	"testing"
)

func TestContinuationTaskReferences(t *testing.T) {
	const task = "kata://01arz3ndektsv4rrffq69g5fav/01ARZ3NDEKTSV4RRFFQ69G5FAW/sarcasticbird/wrap"
	for _, tc := range []struct {
		name, ref string
		valid     bool
	}{
		{"canonical Kata task", task, true},
		{"GitHub issue", "https://github.com/sarcasticbird/wrap/issues/1", true},
		{"empty", "", false},
		{"noncanonical project", strings.Replace(task, "01arz3ndektsv4rrffq69g5fav", "01ARZ3NDEKTSV4RRFFQ69G5FAV", 1), false},
		{"query", task + "?override=1", false},
		{"fragment", task + "#other", false},
		{"missing repository", "kata://01arz3ndektsv4rrffq69g5fav/01ARZ3NDEKTSV4RRFFQ69G5FAW", false},
		{"invalid issue", strings.Replace(task, "01ARZ3NDEKTSV4RRFFQ69G5FAW", "not-an-issue", 1), false},
		{"path traversal", strings.Replace(task, "sarcasticbird/wrap", "../wrap", 1), false},
		{"foreign host", "https://example.com/sarcasticbird/wrap/issues/1", false},
		{"pull request", "https://github.com/sarcasticbird/wrap/pull/1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := TaskContinuationTarget{JobID: strings.Repeat("a", 64), TaskURL: tc.ref, SessionID: "ses_saved", MessageID: "msg_saved", InstanceID: "worker-instance", Generation: 1}
			if got := ValidateContinuationTarget(target) == nil; got != tc.valid {
				t.Fatalf("valid=%v, want %v", got, tc.valid)
			}
		})
	}
}
