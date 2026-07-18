package webauth

import (
	"net"
	"strings"
)

// IsLoopbackAddr reports whether a listen address in the `--addr` flag's form
// ("127.0.0.1:7777", "localhost:7777", ":7777", "0.0.0.0:7777") binds only the
// loopback interface. It is the predicate behind `auth.mode: auto`: a loopback
// bind keeps the historical trusted-localhost no-auth posture, anything else
// requires login. An empty host (":7777") or wildcard host binds every
// interface and is therefore NOT loopback; an unparseable address is treated
// as non-loopback so a config typo fails toward requiring auth, never toward
// serving open.
func IsLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// No port — classify the bare host (e.g. "localhost" without a port).
		host = addr
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return false // ":7777" binds all interfaces
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
