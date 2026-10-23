package coordination

import "testing"

func TestKataTaskReferencePinsImmutableIDsAndRepository(t *testing.T) {
	ref, err := KataTaskReference("01M5MCPEHHP4C98MZSHJEE3M8D", "01M5MFMZF2MPV5MHF84G9RVGA8", "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	p, issue, repo, err := ParseKataTaskReference(ref)
	if err != nil || p != "01M5MCPEHHP4C98MZSHJEE3M8D" || issue != "01M5MFMZF2MPV5MHF84G9RVGA8" || repo != "owner/repo" {
		t.Fatal(ref, p, issue, repo, err)
	}
	for _, bad := range []string{ref + "?x=1", ref + "#fragment", "kata://user@host/id/owner/repo", "kata://project/short/owner/repo", ref + "/extra"} {
		if _, _, _, e := ParseKataTaskReference(bad); e == nil {
			t.Fatal("bad task identity accepted", bad)
		}
	}
}
