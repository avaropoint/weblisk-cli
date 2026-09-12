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
