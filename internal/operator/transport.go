package operator

// What a credential may be carried over.
//
// # The gap this closes
//
// Studio and the CLI present an operator's signed challenge — and then hold the
// token it returns — against an address somebody typed. Neither asserted
// anything about the transport, so a hub named as `http://hub.example.com:9800`
// received a signature and returned a bearer token in clear text across a
// network.
//
// architecture/client specifies the relationship this is: `api-server`, whose
// binding is an mTLS client certificate and whose trust level is `trusted`
// BECAUSE of that certificate. We have neither the certificate nor the mutual
// verification, so we are not that client yet and must not behave as though we
// were.
//
// # Why loopback is the exception and not a compromise
//
// A hub on 127.0.0.1 is reached without a packet leaving the machine, so there
// is no network position from which to observe the exchange. That is a
// different situation from an unencrypted hop, not a lenient reading of the
// same one — and it is the documented default (`http://localhost:9800`), so
// refusing it would make the ordinary case impossible while protecting nothing.
//
// Everything else must be TLS. This is deliberately a refusal rather than a
// warning: a warning on a credential path is read once and then never again.

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ErrInsecureTransport is returned when a credential would cross a network in
// clear text.
type ErrInsecureTransport struct {
	Address string
	Host    string
}

func (e *ErrInsecureTransport) Error() string {
	return fmt.Sprintf("refusing to send an operator credential to %s over plain HTTP.\n"+
		"  %s is not this machine, so the signature and the token it returns would\n"+
		"  cross a network in clear text. Use https://, or reach the hub over a\n"+
		"  tunnel that terminates locally.", e.Address, e.Host)
}

// CheckCredentialTransport refuses to carry a credential over a transport that
// cannot protect it.
//
// Applied to the token request and to every authenticated call — NOT to
// GET /v1/health, which carries no credential and is how an operator finds out
// whether a hub is there at all.
func CheckCredentialTransport(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("not a usable hub address: %w", err)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		// Permitted only when the hub is this machine.
	case "":
		return fmt.Errorf("the hub address needs a scheme: %s", raw)
	default:
		return fmt.Errorf("unsupported scheme %q for a hub address", u.Scheme)
	}
	host := u.Hostname()
	if isLocalHost(host) {
		return nil
	}
	return &ErrInsecureTransport{Address: raw, Host: host}
}

// isLocalHost reports whether an address names this machine.
//
// By name and by literal address. "localhost" is checked as a name because it
// is the documented default and resolving it would make the check depend on a
// resolver an attacker may influence; IP literals are checked with the stdlib's
// own loopback test rather than by string prefix, because 127.0.0.1,
// 127.0.0.53 and ::1 are all loopback and only one of them starts with "127.0.0.1".
func isLocalHost(host string) bool {
	h := strings.ToLower(strings.Trim(host, "[]"))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
