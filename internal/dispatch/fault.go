package dispatch

// What went wrong at the provider, as a fact rather than a sentence.
//
// # The bug this exists to make impossible
//
// Retry was decided by searching the error's text for words. The permanent
// list was checked first and contained "permission", because a permissions
// error is not worth retrying. The error's text was the provider's entire JSON
// result envelope, and every such envelope contains
//
//	"permission_denials":[]
//
// So EVERY failure from the Claude Code provider matched "permission" and was
// classified permanent — including the five "529 Overloaded. This is a
// server-side issue, usually temporary — try again in a moment" that killed five
// consecutive forty-minute tenant builds. Seven attempts of backoff, an outer
// supervisor, a documented policy: all of it inert, because the first list to
// match won and it matched an empty array.
//
// The lesson is not "fix the word list". It is that a status code is a fact and
// a serialized envelope is prose that happens to contain one. Providers report
// their faults structurally — an HTTP status, a terminal reason, a subtype —
// and those are what decide. Text matching survives only as a last resort for
// providers that give us nothing better, and then it reads the human message
// ALONE, never the envelope that carries it.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// FaultClass is what should be done about a failure.
type FaultClass int

const (
	// FaultUnknown means the provider did not say enough to decide. The caller
	// falls back to reading the human message. It is deliberately the zero
	// value: a fault nobody classified must not silently become "retry
	// forever" or "give up".
	FaultUnknown FaultClass = iota
	// FaultTransient will plausibly succeed if tried again shortly.
	FaultTransient
	// FaultPermanent returns the same answer however many times it is asked.
	FaultPermanent
)

// ProviderFault is a failure reported by a model provider, keeping the fields
// that decide what to do about it apart from the text that describes it.
type ProviderFault struct {
	// Provider names who failed, for the message.
	Provider string
	// Status is the upstream HTTP status, 0 when there was none. This is the
	// single most reliable signal and is read before anything else.
	Status int
	// TerminalReason and Subtype are the Claude Code envelope's own account of
	// how the turn ended — "api_error", "aborted_streaming",
	// "error_during_execution". A stream that aborts mid-flight carries no
	// status at all, so these are how it is recognised.
	TerminalReason string
	Subtype        string
	// Message is the sentence written for a person. It is the ONLY text ever
	// matched against, and it is what a human is shown.
	Message string
	// Raw is the envelope, kept for diagnostics and never classified on. It is
	// excluded from Error() deliberately: putting it there is what let an
	// envelope field decide a retry.
	Raw string
}

func (f *ProviderFault) Error() string {
	var b strings.Builder
	if f.Provider != "" {
		b.WriteString(f.Provider)
		b.WriteString(": ")
	}
	if f.Status != 0 {
		fmt.Fprintf(&b, "HTTP %d", f.Status)
		if f.Message != "" {
			b.WriteString(" — ")
		}
	}
	if f.Message != "" {
		b.WriteString(f.Message)
	}
	if b.Len() == 0 {
		return "provider failed without saying why"
	}
	return b.String()
}

// retryableStatus is the set of upstream statuses that are transient by
// definition rather than by hope.
//
// 529 is Anthropic's overload signal and the one that killed the builds. 500 is
// included because a provider's own unhandled error is not something the caller
// can fix by changing the request; 501 and 505 are not, which is why this is a
// set and not a >= 500 test.
var retryableStatus = map[int]bool{
	408: true, // request timeout
	409: true, // conflict — concurrent-request limits present as this
	425: true, // too early
	429: true, // rate limited (a session limit is caught before this, below)
	500: true,
	502: true,
	503: true,
	504: true,
	529: true, // overloaded
}

// transientTerminal are ways a turn can end that are the provider's fault and
// not the request's.
//
// A stream that stops without a stop reason arrives with zero output tokens and
// no status code — the same prompt succeeds on the next attempt. It killed two
// builds before it was recognised.
var transientTerminal = map[string]bool{
	"aborted_streaming":      true,
	"error_during_execution": true,
	"api_error":              false, // carries a status; classified by that
}

// Class decides what to do about the fault, reading the fields the provider
// filled in rather than the words it chose.
func (f *ProviderFault) Class() FaultClass {
	// A session or quota limit is measured in hours and arrives as a 429, which
	// is otherwise the most retryable status there is. So it is settled first,
	// from the human message, before the status table is consulted. Retrying it
	// would replace a clear "resets 11:50pm" with a long silence.
	if permanentMessage(f.Message) {
		return FaultPermanent
	}
	if f.Status != 0 {
		if retryableStatus[f.Status] {
			return FaultTransient
		}
		// Any other status the provider named is the request's problem: a bad
		// key, a missing model, a payload too large. Asking again changes
		// nothing and hides a fault that needs a person.
		return FaultPermanent
	}
	if transientTerminal[f.TerminalReason] || transientTerminal[f.Subtype] {
		return FaultTransient
	}
	// Nothing structural to go on. The message is all there is.
	return FaultUnknown
}

// permanentMessage recognises the failures that will still be failing after any
// backoff a build can afford.
//
// Matched against the human sentence only. The previous version matched the
// whole envelope, where "permission_denials" made every failure permanent.
func permanentMessage(msg string) bool {
	s := strings.ToLower(msg)
	for _, permanent := range []string{
		"session limit", "usage limit", "quota", "insufficient_quota",
		"credit balance", "invalid api key", "invalid_api_key",
		"authentication", "unauthorized", "permission denied",
		"not authorized", "model not found",
	} {
		if strings.Contains(s, permanent) {
			return true
		}
	}
	return false
}

// transientMessage is the last resort, for a provider that reports a failure as
// nothing but a sentence.
func transientMessage(msg string) bool {
	s := strings.ToLower(msg)
	for _, retryable := range []string{
		"529", "overloaded", "rate limit", "too many requests",
		"502", "503", "504", "bad gateway", "service unavailable",
		"connection reset", "connection refused", "broken pipe",
		"timeout", "timed out", "temporarily", "try again",
		"aborted_streaming", "error_during_execution",
		"stream ended", "stream error", "unexpected eof", "eof",
		"no such host", "network is unreachable",
	} {
		if strings.Contains(s, retryable) {
			return true
		}
	}
	return false
}

// claudeCodeEnvelope is the part of `claude --output-format json` that says how
// a turn ended. Only the deciding fields are named; the rest is kept as Raw.
type claudeCodeEnvelope struct {
	IsError        bool   `json:"is_error"`
	Result         string `json:"result"`
	Subtype        string `json:"subtype"`
	StopReason     string `json:"stop_reason"`
	TerminalReason string `json:"terminal_reason"`
	APIErrorStatus int    `json:"api_error_status"`
}

// faultFromClaudeCode reads a Claude Code result envelope into a fault.
//
// Returns nil when the text is not an envelope, so a caller can fall back
// rather than invent a classification from a parse failure.
// faultFromCLI reads a coding-agent CLI envelope into a fault.
//
// Claude Code and Grok print different JSON on failure. A classifier that only
// understood one of them would swallow the other's error as "not an envelope"
// and fall through to a less useful subprocess exit string.
func faultFromCLI(provider, raw string) *ProviderFault {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "{") {
		return nil
	}
	var generic map[string]any
	if json.Unmarshal([]byte(raw), &generic) != nil {
		return nil
	}
	if t, _ := generic["type"].(string); t == "error" {
		msg, _ := generic["message"].(string)
		if msg == "" {
			msg = raw
		}
		return &ProviderFault{Provider: provider, Message: msg, Raw: raw}
	}
	return faultFromClaudeCode(provider, raw)
}

func faultFromClaudeCode(provider, raw string) *ProviderFault {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "{") {
		return nil
	}
	var env claudeCodeEnvelope
	if json.Unmarshal([]byte(raw), &env) != nil {
		return nil
	}
	msg := strings.TrimSpace(env.Result)
	if msg == "" {
		msg = strings.TrimSpace(env.Subtype)
	}
	return &ProviderFault{
		Provider:       provider,
		Status:         env.APIErrorStatus,
		TerminalReason: env.TerminalReason,
		Subtype:        env.Subtype,
		Message:        msg,
		Raw:            raw,
	}
}

// httpFault builds a fault from an HTTP provider's response.
//
// The body is kept as Raw and summarised into Message, so a body that happens
// to contain the word "permission" cannot decide a retry — which is exactly how
// the original bug worked.
func httpFault(provider string, status int, body []byte) *ProviderFault {
	return &ProviderFault{
		Provider: provider,
		Status:   status,
		Message:  errorMessageFromBody(body),
		Raw:      string(body),
	}
}

// errorMessageFromBody pulls the human sentence out of an error body, in the
// two shapes every provider we speak to uses.
func errorMessageFromBody(body []byte) string {
	var wrapped struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &wrapped) == nil {
		if m := strings.TrimSpace(wrapped.Error.Message); m != "" {
			return m
		}
		if m := strings.TrimSpace(wrapped.Message); m != "" {
			return m
		}
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

// FaultOf finds the provider fault inside an error, if there is one.
func FaultOf(err error) *ProviderFault {
	var f *ProviderFault
	if errors.As(err, &f) {
		return f
	}
	return nil
}
