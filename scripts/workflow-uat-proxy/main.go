// workflow-uat-proxy bounds native turns in disposable acceptance guests only.
// It forwards no requests off guest loopback and never logs credentials/bodies.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func reserve(path, intent string, limit int) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	var turns []string
	raw, err := io.ReadAll(io.LimitReader(f, 8193))
	if err != nil || len(raw) > 8192 {
		return fmt.Errorf("invalid budget journal")
	}
	if len(raw) > 0 && json.Unmarshal(raw, &turns) != nil {
		return fmt.Errorf("invalid budget journal")
	}
	for _, saved := range turns {
		if saved == intent {
			return fmt.Errorf("duplicate native intent refused")
		}
	}
	if len(turns) >= limit {
		return fmt.Errorf("UAT turn budget exhausted")
	}
	turns = append(turns, intent)
	raw, err = json.Marshal(turns)
	if err != nil {
		return err
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err = f.Truncate(0); err != nil {
		return err
	}
	if _, err = f.Write(raw); err != nil {
		return err
	}
	return f.Sync()
}

func main() {
	counter := flag.String("counter", "", "private durable turn journal")
	limit := flag.Int("limit", 0, "hard native-turn cap")
	flag.Parse()
	if !strings.HasPrefix(*counter, "/var/lib/coop/") || *limit < 1 || *limit > 4 {
		log.Fatal("invalid disposable UAT budget")
	}
	u, _ := url.Parse("http://127.0.0.1:4097")
	proxy := httputil.NewSingleHostReverseProxy(u)
	proxy.ErrorLog = log.New(io.Discard, "", 0)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/session/") && strings.HasSuffix(r.URL.Path, "/prompt_async") {
			raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
			if err != nil {
				http.Error(w, "invalid native request", http.StatusRequestEntityTooLarge)
				return
			}
			var body struct {
				MessageID string `json:"messageID"`
			}
			if json.Unmarshal(raw, &body) != nil || !strings.HasPrefix(body.MessageID, "msg_") {
				http.Error(w, "missing native intent", http.StatusBadRequest)
				return
			}
			if err := reserve(*counter, r.URL.Path+"/"+body.MessageID, *limit); err != nil {
				http.Error(w, "UAT budget/intent held", http.StatusConflict)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		proxy.ServeHTTP(w, r)
	})
	server := http.Server{Addr: "127.0.0.1:4096", Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(server.ListenAndServe())
}
