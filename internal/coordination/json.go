package coordination

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

// DecodeJSON rejects ambiguous fields before Go's case-insensitive struct decoder.
func DecodeJSON(raw []byte, out any) error {
	if len(raw) > MaxJSON {
		return ErrSize
	}
	if !utf8.Valid(raw) {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if err := scanJSON(d, 0); err != nil {
		return ErrInvalid
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrInvalid
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return ErrInvalid
	}
	return nil
}
func foldJSONKey(key string) string {
	var b strings.Builder
	for _, r := range key {
		first := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			if next < first {
				first = next
			}
		}
		b.WriteRune(first)
	}
	return b.String()
}
func scanJSON(d *json.Decoder, depth int) error {
	if depth > 64 {
		return ErrInvalid
	}
	t, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			s, ok := key.(string)
			if !ok {
				return ErrInvalid
			}
			k := foldJSONKey(s)
			if seen[k] {
				return ErrInvalid
			}
			seen[k] = true
			if err = scanJSON(d, depth+1); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return ErrInvalid
		}
	case '[':
		for d.More() {
			if err = scanJSON(d, depth+1); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}
