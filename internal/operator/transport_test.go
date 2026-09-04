package operator

// A credential must not cross a network in clear text.
//
// architecture/client specifies this relationship as `api-server`, whose
// binding is an mTLS client certificate and whose trust level is `trusted`
// BECAUSE of it. We have neither, so until we do, the transport itself has to
// carry the guarantee.

import (
	"errors"
	"strings"
	"testing"
)

func TestLoopbackOverHTTPIsAllowed(t *testing.T) {
	// The documented default. Refusing it would protect nothing and make the
	// ordinary case impossible.
	for _, addr := range []string{
		"http://localhost:9800",
		"http://127.0.0.1:9800",
		"http://[::1]:9800",
		"http://127.0.0.53:9800", // also loopback, and does not start with 127.0.0.1
		"http://hub.localhost:9800",
	} {
		if err := CheckCredentialTransport(addr); err != nil {
			t.Errorf("%s was refused: %v", addr, err)
		}
	}
}

func TestARemoteHubOverHTTPIsRefused(t *testing.T) {
	for _, addr := range []string{
		"http://hub.example.com:9800",
		"http://10.0.0.5:9800", // private, but still a network hop
		"http://192.168.1.9:9800",
	} {
		err := CheckCredentialTransport(addr)
		if err == nil {
			t.Fatalf("%s was accepted; a credential would cross a network in clear text", addr)
		}
		var insecure *ErrInsecureTransport
		if !errors.As(err, &insecure) {
			t.Errorf("%s: refused for the wrong reason: %v", addr, err)
		}
		if !strings.Contains(err.Error(), "https") {
			t.Errorf("%s: the refusal does not say what to do instead: %v", addr, err)
		}
	}
}

func TestHTTPSIsAllowedAnywhere(t *testing.T) {
	for _, addr := range []string{
		"https://hub.example.com",
		"https://10.0.0.5:9800",
		"https://localhost:9800",
	} {
		if err := CheckCredentialTransport(addr); err != nil {
			t.Errorf("%s was refused: %v", addr, err)
		}
	}
}

func TestAnUnusableAddressIsRefused(t *testing.T) {
	for _, addr := range []string{"", "hub.example.com:9800", "ftp://hub", "ws://hub"} {
		if err := CheckCredentialTransport(addr); err == nil {
			t.Errorf("%q was accepted as a hub address", addr)
		}
	}
}

// The wiring, not the helper. Every path that produces or presents a credential
// must ask — a check nothing calls is the failure mode this codebase keeps
// finding.
func TestEveryCredentialPathChecksTheTransport(t *testing.T) {
	remote := "http://hub.example.com:9800"

	if _, _, err := RequestToken(remote, "op"); err == nil {
		t.Error("RequestToken did not check the transport")
	} else if !strings.Contains(err.Error(), "plain HTTP") {
		t.Errorf("RequestToken refused for another reason: %v", err)
	}

	if err := RegisterWith(remote, "op", "a-passphrase-long-enough"); err == nil {
		t.Error("RegisterWith did not check the transport")
	} else if !strings.Contains(err.Error(), "plain HTTP") {
		t.Errorf("RegisterWith refused for another reason: %v", err)
	}

	res, err := Connect(remote, "op", "a-passphrase-long-enough")
	if err == nil {
		t.Error("Connect did not check the transport")
	}
	if res.State != UnreachableState {
		t.Errorf("state = %q, want unreachable", res.State)
	}
	if len(res.Steps) == 0 {
		t.Error("the refusal offers the operator no next step")
	}
}
