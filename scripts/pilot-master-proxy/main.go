// pilot-master-proxy is a disposable acceptance fixture, not a deployment tool.
// It lets an isolated guest reach a synthetic host master through loopback.
package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

func main() {
	upstream := flag.String("upstream", "", "isolated pilot master URL; no production credentials")
	flag.Parse()
	u, err := url.Parse(*upstream)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil {
		log.Fatal("invalid pilot upstream")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsPrivate() && !ip.IsLoopback() {
		log.Fatal("pilot upstream must be a literal private/loopback address")
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	server := &http.Server{Addr: "127.0.0.1:17744", Handler: proxy, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(server.ListenAndServe())
}
