package worker

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"regexp"
	"sort"
	"strings"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

const tagOnlyVersionPath = "cmd/coop/main.go"

var tagOnlyVersionLine = regexp.MustCompile(`(?m)^var version = "(dev|v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*))"\r?$`)

var ghSweepVersionHeader = regexp.MustCompile(`\A#!/usr/bin/env bash\nset -euo pipefail\n\nVERSION="((0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*))"\n`)
var ghSweepVersionAssignment = regexp.MustCompile(`(?:^|[^A-Za-z0-9_])VERSION[ \t]*\+?=`)

func normalizeGHSweepVersion(content []byte, tag string, versioned bool) ([]byte, error) {
	m := ghSweepVersionHeader.FindSubmatchIndex(content)
	// Requiring the assignment immediately after this exact header rules out
	// comments, heredocs, functions and conditional scopes before it. Reject
	// even quoted/commented later assignment tokens rather than parsing Bash.
	if m == nil || len(ghSweepVersionAssignment.FindAllIndex(content, -1)) != 1 {
		return nil, c.ErrInvalid
	}
	if versioned && (tag == "" || !strings.HasPrefix(tag, "v") || string(content[m[2]:m[3]]) != strings.TrimPrefix(tag, "v")) {
		return nil, c.ErrConflict
	}
	return append(append(append([]byte{}, content[:m[2]]...), []byte("COOP-TAG-ONLY-VERSION")...), content[m[3]:]...), nil
}

// tagOnlyPayloadSHA has no caller-defined exclusions. Even the fixed version
// file retains its mode and all bytes except its single verified declaration.
// Other files, including scripts, libraries, symlinks and build inputs, use the
// exact source-bundle-v1 path/mode/type/link/content contract.
func tagOnlyPayloadSHA(raw []byte, schema, tag string, versioned bool) (string, error) {
	if !c.SupportedTagOnlyPayloadSchema(schema) {
		return "", c.ErrInvalid
	}
	if _, err := sourceBundleSHA(raw); err != nil {
		return "", err
	}
	z, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	defer func() { _ = z.Close() }()
	t := tar.NewReader(z)
	entries := map[string]string{}
	found := false
	for {
		h, err := t.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if h.Typeflag == tar.TypeDir || h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		name := strings.SplitN(h.Name, "/", 2)[1]
		content, err := io.ReadAll(io.LimitReader(t, h.Size+1))
		if err != nil || int64(len(content)) != h.Size {
			return "", c.ErrInvalid
		}
		if schema == c.TagOnlyGHSweepPayloadV1 && name == "gh-sweep" {
			if h.Typeflag != tar.TypeReg || h.Linkname != "" || h.Mode&0111 == 0 {
				return "", c.ErrInvalid
			}
			content, err = normalizeGHSweepVersion(content, tag, versioned)
			if err != nil {
				return "", err
			}
			found = true
		} else if schema == c.TagOnlyPayloadV1 && name == tagOnlyVersionPath {
			if h.Typeflag != tar.TypeReg || h.Linkname != "" {
				return "", c.ErrInvalid
			}
			matches := tagOnlyVersionLine.FindAllSubmatchIndex(content, -1)
			if len(matches) != 1 {
				return "", c.ErrInvalid
			}
			m := matches[0]
			positions := token.NewFileSet()
			file, err := parser.ParseFile(positions, tagOnlyVersionPath, content, 0)
			if err != nil || file.Name.Name != "main" {
				return "", c.ErrInvalid
			}
			declaration := false
			for _, node := range file.Decls {
				decl, ok := node.(*ast.GenDecl)
				if !ok || decl.Tok != token.VAR {
					continue
				}
				for _, spec := range decl.Specs {
					value, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for _, name := range value.Names {
						if name.Name != "version" {
							continue
						}
						if declaration || len(value.Names) != 1 || len(value.Values) != 1 {
							return "", c.ErrInvalid
						}
						literal, ok := value.Values[0].(*ast.BasicLit)
						if !ok || literal.Kind != token.STRING || positions.Position(literal.Pos()).Offset != m[2]-1 || positions.Position(literal.End()).Offset != m[3]+1 {
							return "", c.ErrInvalid
						}
						declaration = true
					}
				}
			}
			if !declaration {
				return "", c.ErrInvalid
			}
			if versioned && string(content[m[2]:m[3]]) != tag {
				return "", c.ErrConflict
			}
			// Replace the literal only; comments, imports, code, newline style,
			// permissions and every other source byte remain in the digest.
			content = append(append(append([]byte{}, content[:m[2]]...), []byte("COOP-TAG-ONLY-VERSION")...), content[m[3]:]...)
			found = true
		}
		sum := sha256.Sum256(content)
		entries[name] = fmt.Sprintf("%s\x00%d\x00%d\x00%s\x00%x", name, h.Mode, h.Typeflag, h.Linkname, sum)
	}
	if !found {
		return "", c.ErrInvalid
	}
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	hash := sha256.New()
	for _, key := range keys {
		_, _ = hash.Write([]byte(entries[key] + "\x00"))
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// VerifyTagOnlyPayload verifies the original reviewed source, its delivered
// integration, and both reviewed and integrated version sources. No filesystem
// extraction, external execution, network access or arbitrary ignore list exists.
func VerifyTagOnlyPayload(in c.HandoffInput, source, integrated, versionHead, versionIntegrated []byte) error {
	r := in.TagOnly
	if c.ValidateHandoff(in) != nil || r.VersionPRURL == "" {
		return c.ErrInvalid
	}
	for _, check := range []struct {
		raw      []byte
		expected string
	}{{source, r.SourceBundleSHA}, {versionHead, r.VersionBundleSHA}} {
		got, err := sourceBundleSHA(check.raw)
		if err != nil {
			return err
		}
		if got != check.expected {
			return c.ErrConflict
		}
	}
	for i, raw := range [][]byte{source, integrated, versionHead, versionIntegrated} {
		got, err := tagOnlyPayloadSHA(raw, r.PayloadSchema, in.Tag, i >= 2)
		if err != nil {
			return err
		}
		if got != r.PayloadSHA {
			return c.ErrConflict
		}
	}
	// The pre-version integrated tree is already exact, but bind its full
	// archive too. This also catches an inconsistent archive provider.
	a, err := sourceBundleSHA(source)
	if err != nil {
		return err
	}
	b, err := sourceBundleSHA(integrated)
	if err != nil || a != b {
		return c.ErrConflict
	}
	a, err = sourceBundleSHA(versionHead)
	if err != nil {
		return err
	}
	b, err = sourceBundleSHA(versionIntegrated)
	if err != nil || a != b {
		return c.ErrConflict
	}
	return nil
}
