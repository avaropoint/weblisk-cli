package dispatch

import (
	"strings"
	"testing"
)

// A blueprint that DISCUSSES a section must not lose that section.
//
// ExtractChecklist did strings.Index over the whole document, so a sentence
// containing the words "## Verification Checklist" matched before the section
// did. A paragraph explaining that the checklist stays in prose truncated the
// search at the next heading and read ZERO assertions — 24 vanished from a
// component's acceptance criteria because somebody wrote documentation about
// them.
//
// A heading is a line that starts with it. A mention is prose.
func TestAMentionOfAHeadingIsNotTheHeading(t *testing.T) {
	blueprint := "# Something\n\n" +
		"## Declaration\n\n" +
		"The `## Verification Checklist` stays in prose, here and in every blueprint.\n\n" +
		"## Dependencies\n\nrequires: []\n\n" +
		"## Verification Checklist\n" +
		"- [ ] The first real assertion\n" +
		"- [ ] The second real assertion\n"

	items := ExtractChecklist("x.md", blueprint)
	if len(items) != 2 {
		t.Fatalf("read %d assertions, want 2 — a mention of the heading was taken for the heading", len(items))
	}
	if items[0].Text != "The first real assertion" {
		t.Errorf("read the wrong section: %q", items[0].Text)
	}
}

// headingIndex finds a heading and nothing else.
func TestHeadingIndexMatchesOnlyAHeading(t *testing.T) {
	doc := "intro mentioning ## Endpoints inline\n\n## Endpoints\n\nreal content\n"
	i := headingIndex(doc, "## Endpoints")
	if i < 0 {
		t.Fatal("the heading was not found")
	}
	if !strings.HasPrefix(doc[i:], "## Endpoints\n\nreal content") {
		t.Errorf("matched the inline mention, not the heading: %q", doc[i:i+30])
	}
	// A document that only mentions it has no such section.
	if got := headingIndex("a line about ## Endpoints and nothing else\n", "## Endpoints"); got != -1 {
		t.Errorf("a mention was reported as a heading at %d", got)
	}
	// A heading at the very start counts.
	if got := headingIndex("## Endpoints\n\nx\n", "## Endpoints"); got != 0 {
		t.Errorf("a heading at position 0 was missed: %d", got)
	}
}
