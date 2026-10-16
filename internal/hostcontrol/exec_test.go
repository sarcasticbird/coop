package hostcontrol

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestExecReceiptBoundariesAndForgery(t *testing.T) {
	for _, chunk := range []int{1, 7, 128, 4096} {
		for _, suffix := range []string{"\nCOOP_EXEC_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa:17\n", "\nCOOP_EXEC_wrong:0\n", "\nCOOP_EXEC_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa:999\n"} {
			var out bytes.Buffer
			w := receiptWriter{dst: &out}
			body := strings.Repeat("z", 8193)
			raw := body + suffix
			for len(raw) > 0 {
				n := min(chunk, len(raw))
				if _, e := w.Write([]byte(raw[:n])); e != nil {
					t.Fatal(e)
				}
				if len(w.tail) > 128 {
					t.Fatal("receipt buffer unbounded")
				}
				raw = raw[n:]
			}
			code, e := w.finish(strings.Repeat("a", 32))
			if e != nil {
				t.Fatal(e)
			}
			valid := strings.HasSuffix(suffix, ":17\n")
			if valid {
				if code == nil || *code != 17 || out.String() != body {
					t.Fatal("valid receipt changed output")
				}
			} else if code != nil || out.String() != body+suffix {
				t.Fatal("forged receipt accepted or output lost")
			}
		}
	}
	w := receiptWriter{dst: io.Discard}
	if _, e := w.Write([]byte(strings.Repeat("x", 3<<20))); e != nil || len(w.tail) > 128 {
		t.Fatal("large receipt write unbounded")
	}
}
