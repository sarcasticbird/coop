package worker

import (
	"bytes"
	"io"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/credential"
)

type NativeAuthorityDigestResult struct {
	InputSHA        string `json:"input_sha256,omitempty"`
	PayloadSHA      string `json:"payload_sha256,omitempty"`
	SourceBundleSHA string `json:"source_bundle_sha256,omitempty"`
}

// LoadNativeHandoffInput uses the same private-file protection as worker config.
// Strict shape decoding is required, but pending report IDs need not validate.
func LoadNativeHandoffInput(path string) (c.HandoffInput, error) {
	var in c.HandoffInput
	raw, err := credential.ReadPrivateFile(path)
	if err != nil {
		return in, err
	}
	if len(raw) > c.MaxJSON {
		return in, c.ErrSize
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return in, c.ErrInvalid
	}
	if err = c.DecodeJSON(raw, &in); err != nil {
		return in, err
	}
	return in, nil
}

// NativeAuthorityDigests is local/read-only and grants no authority. The input
// hash uses the very same native serialization as source/version proof checks.
// Payload calculation normalizes a source version; tag matching remains a
// separate mandatory check during VerifyTagOnlyPayload and native execution.
func NativeAuthorityDigests(in *c.HandoffInput, archive io.Reader, schema string) (NativeAuthorityDigestResult, error) {
	var out NativeAuthorityDigestResult
	if in == nil && archive == nil || archive == nil && schema != "" || archive != nil && !c.SupportedTagOnlyPayloadSchema(schema) {
		return out, c.ErrInvalid
	}
	if in != nil {
		out.InputSHA = tagOnlyInputSHA(*in)
	}
	if archive != nil {
		raw, err := io.ReadAll(io.LimitReader(archive, MaxSourceArchive+1))
		if err != nil {
			return NativeAuthorityDigestResult{}, err
		}
		if len(raw) > MaxSourceArchive {
			return NativeAuthorityDigestResult{}, c.ErrSize
		}
		out.SourceBundleSHA, err = sourceBundleSHA(raw)
		if err != nil {
			return NativeAuthorityDigestResult{}, err
		}
		out.PayloadSHA, err = tagOnlyPayloadSHA(raw, schema, "", false)
		if err != nil {
			return NativeAuthorityDigestResult{}, err
		}
	}
	return out, nil
}
