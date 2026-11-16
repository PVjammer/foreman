// fakeforge serves the fake GitHub/GitLab API from internal/forge/fake for the landing
// scenario (scripts/landing-scenario.sh). It is a test tool: no real forge is contacted.
//
//	fakeforge -addr 127.0.0.1:0 -token T -user me -addr-file /tmp/x/addr
package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"os"

	"github.com/PVjammer/foreman/internal/forge/fake"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "listen address")
	token := flag.String("token", "fake-token", "token the API accepts")
	user := flag.String("user", "scenario", "login /user returns")
	addrFile := flag.String("addr-file", "", "write the base URL here once listening")
	flag.Parse()
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	base := "http://" + ln.Addr().String()
	if *addrFile != "" {
		if err := os.WriteFile(*addrFile, []byte(base), 0o644); err != nil {
			log.Fatal(err)
		}
	}
	log.Printf("fake forge on %s", base)
	log.Fatal(http.Serve(ln, fake.New(*token, *user)))
}
