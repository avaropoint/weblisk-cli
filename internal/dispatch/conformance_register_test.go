package dispatch

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// wlt builds a token with the given header, for testing the check itself.
func wlt(t *testing.T, alg, typ string) string {
	t.Helper()
	h, err := json.Marshal(map[string]string{"alg": alg, "typ": typ})
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(h) + ".cGF5bG9hZA.c2ln"
}

// A conformant token must pass.
//
// The first version of this check asserted strings.HasPrefix(tok, "WLT") and so
// rejected EVERY correct token — a conformance assertion invented from the name
// of a thing rather than read from its specification. It failed the
// implementations that were right, and it refused a build to do it. This test
// exists so that cannot come back.
func TestAConformantWLTPasses(t *testing.T) {
	if fault := wltFault(wlt(t, "ML-DSA-65", "WLT")); fault != "" {
		t.Errorf("a conformant token was refused: %s", fault)
	}
}

// The real fault this caught: a generated orchestrator issued a lowercase alg
// from a lowercase constant and verified against the same one, so it was
// self-consistent and would refuse a conformant peer while being refused by it.
func TestALowercaseAlgIsRefused(t *testing.T) {
	fault := wltFault(wlt(t, "ml-dsa-65", "WLT"))
	if fault == "" {
		t.Fatal(`"ml-dsa-65" was accepted; protocol/identity requires exactly "ML-DSA-65"`)
	}
	if !strings.Contains(fault, "ml-dsa-65") || !strings.Contains(fault, "ML-DSA-65") {
		t.Errorf("the fault must show both what was found and what is required: %s", fault)
	}
}

// Every other way a token can fail to be a WLT, each named apart so the message
// says which one — "not WLT format" alone sends the reader looking anywhere.
func TestEachWLTFaultIsNamed(t *testing.T) {
	for _, tc := range []struct{ name, token, want string }{
		{"empty", "", "no token"},
		{"two parts", "a.b", "part"},
		{"four parts", "a.b.c.d", "part"},
		{"empty segment", "a..c", "empty"},
		{"padded base64", "YQ==.b.c", "padding"},
		{"header not base64", "!!!.b.c", "base64url"},
		{"header not json", base64.RawURLEncoding.EncodeToString([]byte("nope")) + ".b.c", "not JSON"},
		{"wrong typ", wlt(t, "ML-DSA-65", "JWT"), "typ"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fault := wltFault(tc.token)
			if fault == "" {
				t.Fatalf("%q was accepted as a WLT", tc.token)
			}
			if !strings.Contains(fault, tc.want) {
				t.Errorf("fault %q does not mention %q", fault, tc.want)
			}
		})
	}
}

// A JWT is not a WLT. protocol/identity is explicit that the typ field exists
// to prevent cross-system use, so accepting one would defeat the field.
func TestAJWTIsNotAWLT(t *testing.T) {
	if wltFault(wlt(t, "RS256", "JWT")) == "" {
		t.Error("a plain JWT was accepted as a WLT")
	}
}
