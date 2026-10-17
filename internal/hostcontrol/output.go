package hostcontrol

import (
	"encoding/base64"
	"unicode/utf8"
)

func (l *Ledger) SaveOutput(id, stream string, b []byte, total int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.records[id]
	if !ok || (stream != "stdout" && stream != "stderr") || r.Pruned || total < int64(len(b)) {
		return fail("invalid_output")
	}
	if len(b) > l.limits.OutputBytes {
		b = b[:l.limits.OutputBytes]
	}
	if e := writeAt(l.dir, id+"."+stream, b); e != nil {
		return e
	}
	s := streamRecord{Total: total, Retained: len(b)}
	if stream == "stdout" {
		r.Stdout = s
	} else {
		r.Stderr = s
	}
	r.Revision++
	if e := l.persist(r); e != nil {
		return e
	}
	l.records[id] = r
	return nil
}
func (l *Ledger) Page(caller Caller, id, stream string, offset int64, limit int) (OutputPage, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.records[id]
	if !ok || r.CallerID != caller.ID {
		return OutputPage{}, fail("operation_not_found")
	}
	if r.Pruned {
		return OutputPage{}, fail("output_pruned")
	}
	if (stream != "stdout" && stream != "stderr") || offset < 0 || limit < 4 || limit > 64<<10 {
		return OutputPage{}, fail("invalid_output_page")
	}
	stats := r.Stdout
	if stream == "stderr" {
		stats = r.Stderr
	}
	var b []byte
	if stats.Retained > 0 {
		var e error
		b, e = readAt(l.dir, id+"."+stream, int64(l.limits.OutputBytes))
		if e != nil || len(b) != stats.Retained {
			return OutputPage{}, fail("output_unavailable")
		}
	}
	if offset > int64(len(b)) {
		return OutputPage{}, fail("invalid_output_offset")
	}
	end := min(int64(len(b)), offset+int64(limit))
	encoding := "utf8"
	if utf8.Valid(b) {
		if offset < int64(len(b)) && !utf8.RuneStart(b[offset]) {
			return OutputPage{}, fail("invalid_output_offset")
		}
		for end < int64(len(b)) && !utf8.RuneStart(b[end]) {
			end--
		}
	} else {
		encoding = "base64"
	}
	data := string(b[offset:end])
	if encoding == "base64" {
		data = base64.StdEncoding.EncodeToString(b[offset:end])
	}
	return OutputPage{Encoding: encoding, Data: data, Offset: offset, NextOffset: end, TotalBytes: stats.Total, DroppedBytes: stats.Total - int64(stats.Retained), EOF: end == int64(len(b)), Truncated: stats.Total > int64(stats.Retained)}, nil
}
