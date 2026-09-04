package dispatch

// The failures in testdata/faults are not invented. They are the exact result
// envelopes printed by the five tenant builds that died to provider
// instability, copied out of the run logs. Every one of them was classified
// permanent by the retry policy and thrown away.
//
// They are fixtures rather than string literals because that is the point: the
// classifier is asked about the real thing, in the shape the provider actually
// sends it, including every field nobody thought about.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func loadEnvelope(t *testing.T, name string) *ProviderFault {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "faults", name))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	f := faultFromClaudeCode("claude code", string(b))
	if f == nil {
		t.Fatalf("%s did not parse as a Claude Code envelope", name)
	}
	return f
}

// The regression itself. Every one of these envelopes contains
// "permission_denials":[], which is what made the old classifier call an
// overload permanent.
func TestRealOverloadsAreRetried(t *testing.T) {
	for _, name := range []string{"envelope2.json", "envelope3.json"} {
		f := loadEnvelope(t, name)
		if f.Status != 529 {
			t.Fatalf("%s: expected status 529, got %d", name, f.Status)
		}
		if f.Class() != FaultTransient {
			t.Fatalf("%s: a 529 Overloaded must be transient, got class %v", name, f.Class())
		}
		if !isTransient(f) {
			t.Fatalf("%s: isTransient said no — this is the bug that killed five builds", name)
		}
	}
}

// A session limit arrives as a 429, which is otherwise the most retryable
// status there is. Retrying it replaces "resets 11:50pm" with a long silence.
func TestSessionLimitIsNotRetried(t *testing.T) {
	f := loadEnvelope(t, "envelope1.json")
	if f.Status != 429 {
		t.Fatalf("expected 429, got %d", f.Status)
	}
	if f.Class() != FaultPermanent {
		t.Fatalf("a session limit must be permanent, got %v", f.Class())
	}
	if isTransient(f) {
		t.Fatal("a session limit was retried")
	}
}

// The envelope must never be what gets matched. If Error() carries the raw
// JSON, any field in it can decide a retry — which is precisely how the
// original fault worked.
func TestErrorTextExcludesTheEnvelope(t *testing.T) {
	f := loadEnvelope(t, "envelope2.json")
	if got := f.Error(); strings.Contains(got, "permission_denials") || strings.Contains(got, "is_error") {
		t.Fatalf("the error text carries envelope fields, so they can be matched on: %q", got)
	}
	if f.Raw == "" {
		t.Fatal("the envelope should still be kept for diagnostics")
	}
}

// A stream that aborts carries no status at all — it is recognised by how the
// turn ended. Two builds died on this.
func TestAbortedStreamIsTransient(t *testing.T) {
	f := &ProviderFault{
		Provider:       "claude code",
		TerminalReason: "aborted_streaming",
		Subtype:        "error_during_execution",
		Message:        "",
	}
	if f.Class() != FaultTransient {
		t.Fatalf("an aborted stream must be transient, got %v", f.Class())
	}
}

// A status the provider named that is not on the retryable list is the
// request's problem. Asking again changes nothing.
func TestClientFaultsAreNotRetried(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 413, 422} {
		f := &ProviderFault{Status: status, Message: "no"}
		if f.Class() != FaultPermanent {
			t.Fatalf("HTTP %d should be permanent, got %v", status, f.Class())
		}
	}
}

// An HTTP provider's error body must not be able to decide a retry either.
func TestHTTPBodyDoesNotDecide(t *testing.T) {
	// A body that mentions permission, on a status that is plainly transient.
	body := []byte(`{"error":{"message":"upstream busy","type":"overloaded_error"},"permission_denials":[]}`)
	f := httpFault("provider", 529, body)
	if f.Class() != FaultTransient {
		t.Fatalf("a 529 must be transient whatever the body says, got %v", f.Class())
	}
	if f.Message != "upstream busy" {
		t.Fatalf("expected the human message, got %q", f.Message)
	}
}

// A provider that reports nothing but a sentence still gets a decision.
func TestBareSentenceFallback(t *testing.T) {
	if !isTransient(errors.New("dial tcp: connection refused")) {
		t.Fatal("a refused connection should be retried")
	}
	if isTransient(errors.New("invalid api key")) {
		t.Fatal("a bad key should not be retried")
	}
}

// The supervisor asks the same classifier, so a fixed classifier must make the
// outer resume fire too. Before the fix this loop returned on the first error.
func TestSupervisorResumesOnRealOverload(t *testing.T) {
	f := loadEnvelope(t, "envelope2.json")
	calls := 0
	err := supervise(func(attempt int) error {
		calls++
		if calls < 3 {
			return f
		}
		return nil
	}, nil, func(time.Duration) {})
	if err != nil {
		t.Fatalf("supervisor gave up: %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 attempts, got %d", calls)
	}
}

// stubProvider fails a fixed number of times with a real captured fault, then
// succeeds. It exists to prove the DECORATOR retries, not just that the
// classifier would have said yes — the classifier said yes in the old tests
// too, because they used hand-written strings that happened to omit the field
// that broke it.
type stubProvider struct {
	failures int
	calls    int
	fault    error
}

func (s *stubProvider) Chat([]Message) (string, error) {
	s.calls++
	if s.calls <= s.failures {
		return "", s.fault
	}
	return "ok", nil
}

func TestTheDecoratorActuallyRetriesARealOverload(t *testing.T) {
	f := loadEnvelope(t, "envelope2.json")
	inner := &stubProvider{failures: 3, fault: f}
	out, err := withTransientRetryNoSleep(func() (string, error) {
		return inner.Chat(nil)
	})
	if err != nil {
		t.Fatalf("retry gave up on a real 529: %v", err)
	}
	if out != "ok" || inner.calls != 4 {
		t.Fatalf("expected 4 calls returning ok, got %d calls / %q", inner.calls, out)
	}
}

func TestTheDecoratorStopsOnARealSessionLimit(t *testing.T) {
	f := loadEnvelope(t, "envelope1.json")
	inner := &stubProvider{failures: 99, fault: f}
	if _, err := withTransientRetryNoSleep(func() (string, error) {
		return inner.Chat(nil)
	}); err == nil {
		t.Fatal("expected the session limit to be returned")
	}
	if inner.calls != 1 {
		t.Fatalf("a session limit was asked %d times; it should be asked once", inner.calls)
	}
}

// The fault has to survive the trip from the provider to the supervisor, or the
// supervisor never resumes and the whole two-layer story is decoration again.
//
// Generation wraps with %w and the layers above return the error unchanged. A
// single %v or %s anywhere on that path erases the status code and the
// supervisor falls back to reading prose — which is the failure this whole
// change exists to remove. This asserts the wrapping, not the classifier.
func TestTheFaultSurvivesTheErrorChain(t *testing.T) {
	f := loadEnvelope(t, "envelope2.json")

	// Exactly how generate.go wraps a provider failure.
	wrapped := fmt.Errorf("generating %s: %w", "internal/orchestrator/auth.go", error(f))

	got := FaultOf(wrapped)
	if got == nil {
		t.Fatal("the fault did not survive wrapping — the supervisor cannot see the status")
	}
	if got.Status != 529 {
		t.Fatalf("status lost in the chain: %d", got.Status)
	}
	if !isTransient(wrapped) {
		t.Fatal("a wrapped 529 is not transient — the supervisor would not resume")
	}

	// And through a second layer, since ComponentInit and ServerInit sit above.
	twice := fmt.Errorf("server: %w", wrapped)
	if !isTransient(twice) {
		t.Fatal("the fault did not survive two layers of wrapping")
	}
}
