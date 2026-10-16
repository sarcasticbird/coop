package hostcontrol

import (
	"context"
	"io"
	"strconv"
	"strings"
)

type receiptKey struct{}

const guestReceiptProgram = `coop_receipt_nonce=$1
shift
"$@"
coop_guest_exit=$?
printf '\nCOOP_EXEC_%s:%d\n' "$coop_receipt_nonce" "$coop_guest_exit" >&2
exit 0`

type receiptWriter struct {
	dst  io.Writer
	tail []byte
}

func (w *receiptWriter) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) >= 128 {
		if _, e := w.dst.Write(w.tail); e != nil {
			return 0, e
		}
		if _, e := w.dst.Write(p[:len(p)-128]); e != nil {
			return 0, e
		}
		w.tail = append(w.tail[:0], p[len(p)-128:]...)
		return n, nil
	}
	w.tail = append(w.tail, p...)
	if len(w.tail) > 128 {
		extra := len(w.tail) - 128
		if _, e := w.dst.Write(w.tail[:extra]); e != nil {
			return 0, e
		}
		copy(w.tail, w.tail[extra:])
		w.tail = w.tail[:128]
	}
	return n, nil
}
func (w *receiptWriter) finish(nonce string) (*int, error) {
	prefix := "\nCOOP_EXEC_" + nonce + ":"
	tail := string(w.tail)
	idx := strings.LastIndex(tail, prefix)
	if idx >= 0 && strings.HasSuffix(tail, "\n") {
		value := tail[idx+len(prefix) : len(tail)-1]
		code, e := strconv.Atoi(value)
		if e == nil && code >= 0 && code <= 255 && strconv.Itoa(code) == value {
			_, e = w.dst.Write(w.tail[:idx])
			return &code, e
		}
	}
	_, e := w.dst.Write(w.tail)
	return nil, e
}
func (c *Control) Exec(ctx context.Context, b Binding, in Mutation, stdout, stderr io.Writer) (Outcome, error) {
	nonce, ok := ctx.Value(receiptKey{}).(string)
	if !ok || len(nonce) != 32 {
		return Outcome{Completed: true, Category: "missing_dispatch_receipt"}, fail("missing_dispatch_receipt")
	}
	argv := append([]string{"/bin/sh", "-c", guestReceiptProgram, "coop-exec", nonce}, in.Argv...)
	cwd := in.Cwd
	if cwd == "" {
		cwd = b.Spec.Workdir
	}
	if b.Agent != nil {
		argv = b.Agent.GuestArgv(argv)
	} else if b.Interactive != nil {
		var e error
		cwd, argv, e = b.Interactive.GuestCommand(cwd, argv)
		if e != nil {
			return Outcome{Completed: true, Category: "invalid_guest_cwd"}, e
		}
	} else {
		return Outcome{Completed: true, Category: "unsupported_workspace"}, fail("unsupported_workspace")
	}
	decoder := &receiptWriter{dst: stderr}
	err := b.Runner.Exec(ctx, b.RuntimeName, cwd, argv, strings.NewReader(string(in.Stdin)), stdout, decoder)
	code, decodeErr := decoder.finish(nonce)
	if err != nil || decodeErr != nil || code == nil {
		category := "transport_outcome_unknown"
		if ctx.Err() != nil {
			category = "interrupted_outcome_unknown"
		}
		return Outcome{Category: category}, err
	}
	return Outcome{ExitCode: code, Completed: true, TerminationProven: true}, nil
}
