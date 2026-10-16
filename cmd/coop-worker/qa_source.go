package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sarcasticbird/coop/internal/worker"
	"golang.org/x/sys/unix"
)

func qaSourceDigest(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("qa-source-digest", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	archive := flags.String("archive", "", "absolute path to a source archive")
	if flags.Parse(args) != nil || !filepath.IsAbs(*archive) || len(flags.Args()) != 0 {
		return errors.New("qa-source-digest requires --archive /absolute/source.tar.gz")
	}
	fd, err := unix.Open(*archive, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), *archive)
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > worker.MaxAPIPage {
		return errors.New("QA source archive must be a regular file within the compressed size limit")
	}
	digest, err := worker.QASourceDigest(f)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, digest)
	return err
}
