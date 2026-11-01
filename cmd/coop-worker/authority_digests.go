package main

import (
	"encoding/json"
	"flag"
	"io"
	"os"

	c "github.com/sarcasticbird/coop/internal/coordination"
	"github.com/sarcasticbird/coop/internal/worker"
)

func nativeAuthorityDigestsCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("native authority-digests", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	input := flags.String("input", "", "private native HandoffInput JSON (pending reports allowed)")
	archive := flags.String("archive", "", "exact gzip source archive")
	schema := flags.String("payload-schema", "", "fixed coop-go-version-v1 or gh-sweep-version-v1 contract")
	if flags.Parse(args) != nil || len(flags.Args()) != 0 || *input == "" && *archive == "" || *archive == "" && *schema != "" || *archive != "" && !c.SupportedTagOnlyPayloadSchema(*schema) {
		return c.ErrInvalid
	}
	var in *c.HandoffInput
	if *input != "" {
		value, err := worker.LoadNativeHandoffInput(*input)
		if err != nil {
			return err
		}
		in = &value
	}
	var reader io.Reader
	if *archive != "" {
		file, err := os.Open(*archive)
		if err != nil {
			return err
		}
		defer func() { _ = file.Close() }()
		reader = file
	}
	result, err := worker.NativeAuthorityDigests(in, reader, *schema)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(result)
}
