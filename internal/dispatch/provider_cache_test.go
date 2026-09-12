package dispatch

// Prompt caching on the hosted Anthropic path.
//
// A per-file prompt is ~385 KB and two files in a run share 99.81% of it byte
// for byte. Without a breakpoint every call re-processes that prefix at full
// price. This is the one provider whose API caches a prefix; the local CLIs
// never reach this code.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The prefix is one cacheable block and the tail is another; nothing is lost.
func TestThePrefixIsMarkedCacheableAndTheTailIsNot(t *testing.T) {
	m := Message{Role: "user", Content: "PREFIX|SUFFIX", CacheBoundary: 6}
	blocks := anthropicBlocks(m)
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2", len(blocks))
	}
	if blocks[0].Text != "PREFIX" || blocks[0].CacheControl == nil || blocks[0].CacheControl.Type != "ephemeral" {
		t.Errorf("prefix block = %+v, want PREFIX with an ephemeral breakpoint", blocks[0])
	}
	if blocks[1].Text != "|SUFFIX" || blocks[1].CacheControl != nil {
		t.Errorf("tail block = %+v, want |SUFFIX with no breakpoint — a breakpoint on the "+
			"varying tail would never hit", blocks[1])
	}
	if blocks[0].Text+blocks[1].Text != m.Content {
		t.Error("splitting changed the content")
	}
	// No boundary, or a nonsensical one, sends the content whole and unmarked.
	for _, b := range []int{0, -1, len(m.Content), len(m.Content) + 10} {
		got := anthropicBlocks(Message{Content: m.Content, CacheBoundary: b})
		if len(got) != 1 || got[0].Text != m.Content || got[0].CacheControl != nil {
			t.Errorf("boundary %d: blocks = %+v, want the whole content in one unmarked block", b, got)
		}
	}
}

// What actually goes over the wire: the system prompt is a breakpoint, the user
// prefix is a breakpoint, the user tail is not.
func TestTheRequestCarriesBreakpointsWhereTheCacheCanUseThem(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}]}`))
	}))
	defer srv.Close()

	p := &AnthropicProvider{BaseURL: srv.URL, APIKey: "k", Model: "m"}
	prefix := strings.Repeat("spec ", 50)
	_, err := p.Chat([]Message{
		{Role: "system", Content: "you write files"},
		{Role: "user", Content: prefix + "ASK", CacheBoundary: len(prefix)},
	})
	if err != nil {
		t.Fatal(err)
	}
	sys, _ := body["system"].([]any)
	if len(sys) != 1 || sys[0].(map[string]any)["cache_control"] == nil {
		t.Errorf("the system prompt is not a breakpoint: %v", body["system"])
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages = %v", body["messages"])
	}
	content, _ := msgs[0].(map[string]any)["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("user content blocks = %d, want 2: %v", len(content), content)
	}
	if content[0].(map[string]any)["cache_control"] == nil {
		t.Error("the user prefix is not a breakpoint")
	}
	if content[1].(map[string]any)["cache_control"] != nil {
		t.Error("the user tail is a breakpoint, which can never hit")
	}
	if content[1].(map[string]any)["text"] != "ASK" {
		t.Errorf("tail text = %v, want ASK", content[1].(map[string]any)["text"])
	}
}

// The boundary filePrompt reports is where two files' prompts stop being
// identical — before the ownership block, which is filtered to the caller's
// package.
func TestTheFilePromptBoundaryIsWhereFilesDiverge(t *testing.T) {
	plan := &Plan{Root: ".", Module: "acme", Files: []PlannedFile{
		{Path: "internal/x/a.go", Purpose: "first", Declares: []string{"Alpha"}},
		{Path: "internal/x/b.go", Purpose: "second", Declares: []string{"Beta"}},
	}}
	bps := map[string]string{"protocol/types.md": strings.Repeat("TYPE SPECIFICATION\n", 50)}
	order := []string{"protocol/types.md"}
	a, ba := filePromptParts(plan.Files[0], plan, "go", bps, order, "PLATFORM GUIDE", nil, nil, nil, nil, nil)
	b, bb := filePromptParts(plan.Files[1], plan, "go", bps, order, "PLATFORM GUIDE", nil, nil, nil, nil, nil)
	if ba <= 0 || ba != bb {
		t.Fatalf("boundaries = %d and %d, want equal and positive", ba, bb)
	}
	if a[:ba] != b[:bb] {
		t.Error("the text before the boundary differs between two files — a cache would miss")
	}
	if !strings.Contains(a[ba:], "SYMBOLS OWNED BY OTHER FILES") {
		t.Error("the ownership block is before the boundary, and it varies per file")
	}
	if strings.Contains(a[:ba], "Generate exactly one file") {
		t.Error("the per-file ask is inside the cacheable prefix")
	}
	// filePrompt and filePromptParts agree, so the cache key is unaffected.
	if filePrompt(plan.Files[0], plan, "go", bps, order, "PLATFORM GUIDE", nil, nil, nil, nil, nil) != a {
		t.Error("filePrompt and filePromptParts render differently")
	}
}

// The optimisation must never take the provider down with it.
//
// This is the one change on this path that could not be exercised against the
// API on the machine it was written on. If the request shape is refused, the
// provider retries once without breakpoints and stays off for the process —
// one extra call, not a build that cannot run.
func TestARefusedBreakpointDegradesRatherThanFails(t *testing.T) {
	anthropicCacheRejected.Store(false)
	t.Cleanup(func() { anthropicCacheRejected.Store(false) })
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var b map[string]any
		_ = json.Unmarshal(raw, &b)
		bodies = append(bodies, b)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(raw), "cache_control") {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"messages.0.content.0.cache_control: Extra inputs are not permitted"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}]}`))
	}))
	defer srv.Close()

	p := &AnthropicProvider{BaseURL: srv.URL, APIKey: "k", Model: "m"}
	out, err := p.Chat([]Message{{Role: "system", Content: "s"}, {Role: "user", Content: "PREFIX-TAIL", CacheBoundary: 6}})
	if err != nil {
		t.Fatalf("a refused breakpoint failed the provider: %v", err)
	}
	if out != "ok" {
		t.Errorf("answer = %q", out)
	}
	if len(bodies) != 2 {
		t.Fatalf("made %d requests, want 2 (one refused, one clean)", len(bodies))
	}
	if strings.Contains(fmtJSON(bodies[1]), "cache_control") {
		t.Error("the retry still carried breakpoints")
	}
	// And it stays off: a third call sends no breakpoints and is not refused.
	if _, err := p.Chat([]Message{{Role: "user", Content: "PREFIX-TAIL", CacheBoundary: 6}}); err != nil {
		t.Fatalf("a later call was refused again: %v", err)
	}
	if len(bodies) != 3 || strings.Contains(fmtJSON(bodies[2]), "cache_control") {
		t.Errorf("breakpoints came back after being refused (requests=%d)", len(bodies))
	}
}

// An operator can turn breakpoints off without a rebuild.
func TestPromptCachingCanBeSwitchedOff(t *testing.T) {
	anthropicCacheRejected.Store(false)
	t.Setenv("WL_AI_PROMPT_CACHE", "0")
	body, err := buildAnthropicRequest("m", []Message{
		{Role: "system", Content: "s"}, {Role: "user", Content: "PREFIX-TAIL", CacheBoundary: 6},
	}, promptCacheEnabled())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "cache_control") {
		t.Error("WL_AI_PROMPT_CACHE=0 still sent breakpoints")
	}
}

// An empty message is dropped rather than sent as an empty text block, which
// the API rejects.
func TestAnEmptyMessageIsNotSentAsAnEmptyBlock(t *testing.T) {
	if got := anthropicBlocks(Message{Role: "user", Content: ""}); len(got) != 0 {
		t.Errorf("empty content produced blocks %+v", got)
	}
	body, err := buildAnthropicRequest("m", []Message{{Role: "system", Content: ""}, {Role: "user", Content: ""}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `"text":""`) {
		t.Errorf("an empty text block was sent: %s", body)
	}
}

func fmtJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
