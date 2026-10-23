package coordination

import "testing"

func TestDiagnosticReferenceAcceptsCanonicalKataOnly(t *testing.T) {
	const ref = "kata://01m5mcpehhp4c98mzshjee3m8d/01M5MFMZF2MPV5MHF84G9RVGA8/owner/repo"
	if !DiagnosticReference(ref) {
		t.Fatal("canonical Kata diagnostic rejected")
	}
	for _, bad := range []string{ref + "?token=secret", ref + "/extra", "kata://01M5MCPEHHP4C98MZSHJEE3M8D/01M5MFMZF2MPV5MHF84G9RVGA8/owner/repo", "https://github.com/owner/repo/issues/1?x=1", "https://github.com/owner/repo/issues/1#x"} {
		if DiagnosticReference(bad) {
			t.Fatalf("noncanonical diagnostic accepted: %s", bad)
		}
	}
	if !DiagnosticReference("") || !DiagnosticReference("https://github.com/owner/repo/pull/2") {
		t.Fatal("legacy GitHub diagnostic changed")
	}
}
