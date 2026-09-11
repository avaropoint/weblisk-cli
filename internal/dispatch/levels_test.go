package dispatch

// Dependency levels, which are what a concurrent driver would schedule on.

import (
	"fmt"
	"testing"
)

func TestLevelsGroupIndependentFiles(t *testing.T) {
	p := &Plan{Files: []PlannedFile{
		{Path: "a.go"},
		{Path: "b.go"},
		{Path: "c.go", DependsOn: []string{"a.go"}},
		{Path: "d.go", DependsOn: []string{"a.go", "b.go"}},
		{Path: "e.go", DependsOn: []string{"c.go", "d.go"}},
	}}
	lv := p.Levels()
	if len(lv) != 3 {
		t.Fatalf("levels = %d, want 3: %v", len(lv), levelPaths(lv))
	}
	want := [][]string{{"a.go", "b.go"}, {"c.go", "d.go"}, {"e.go"}}
	got := levelPaths(lv)
	for i := range want {
		if fmt.Sprint(got[i]) != fmt.Sprint(want[i]) {
			t.Errorf("level %d = %v, want %v", i, got[i], want[i])
		}
	}
}

// Depth is the LONGEST path to a root. A file placed at its shortest depth
// would sit beside something it depends on, which is the one thing a level is
// supposed to rule out.
func TestLevelsUseTheLongestPath(t *testing.T) {
	p := &Plan{Files: []PlannedFile{
		{Path: "a.go"},
		{Path: "b.go", DependsOn: []string{"a.go"}},
		{Path: "c.go", DependsOn: []string{"b.go"}},
		// Depends on a root AND on the deepest file. Shortest path says level 1.
		{Path: "d.go", DependsOn: []string{"a.go", "c.go"}},
	}}
	lv := p.Levels()
	if len(lv) != 4 {
		t.Fatalf("levels = %v, want four", levelPaths(lv))
	}
	if got := levelPaths(lv)[3]; fmt.Sprint(got) != "[d.go]" {
		t.Errorf("last level = %v, want [d.go]", got)
	}
	// The invariant that matters: nothing shares a level with its dependency.
	at := map[string]int{}
	for i, files := range lv {
		for _, f := range files {
			at[f.Path] = i
		}
	}
	for _, f := range p.Files {
		for _, d := range f.DependsOn {
			if at[d] >= at[f.Path] {
				t.Errorf("%s (level %d) shares or precedes its dependency %s (level %d)",
					f.Path, at[f.Path], d, at[d])
			}
		}
	}
}

// A dependency on something outside the plan is not a dependency. planOrder
// ignores those, and the levels must agree or the two disagree about order.
func TestLevelsIgnoreDependenciesOutsideThePlan(t *testing.T) {
	p := &Plan{Files: []PlannedFile{
		{Path: "a.go", DependsOn: []string{"go.mod", "internal/protocol/types.go"}},
	}}
	lv := p.Levels()
	if len(lv) != 1 || len(lv[0]) != 1 {
		t.Fatalf("levels = %v, want one file at level 0", levelPaths(lv))
	}
}

// A cyclic plan has no levels, and says so rather than returning a wrong answer.
func TestACyclicPlanHasNoLevels(t *testing.T) {
	p := &Plan{Files: []PlannedFile{
		{Path: "a.go", DependsOn: []string{"b.go"}},
		{Path: "b.go", DependsOn: []string{"a.go"}},
	}}
	if lv := p.Levels(); lv != nil {
		t.Errorf("a cycle produced levels %v", levelPaths(lv))
	}
}

// Every file appears exactly once, or a driver would skip or duplicate work.
func TestEveryFileAppearsInExactlyOneLevel(t *testing.T) {
	p := &Plan{Files: []PlannedFile{
		{Path: "a.go"}, {Path: "b.go", DependsOn: []string{"a.go"}},
		{Path: "c.go", DependsOn: []string{"a.go"}}, {Path: "d.go", DependsOn: []string{"b.go", "c.go"}},
	}}
	seen := map[string]int{}
	for _, files := range p.Levels() {
		for _, f := range files {
			seen[f.Path]++
		}
	}
	if len(seen) != len(p.Files) {
		t.Errorf("%d files in levels, plan has %d", len(seen), len(p.Files))
	}
	for path, n := range seen {
		if n != 1 {
			t.Errorf("%s appears %d times", path, n)
		}
	}
}

func levelPaths(lv [][]PlannedFile) [][]string {
	out := make([][]string, len(lv))
	for i, files := range lv {
		for _, f := range files {
			out[i] = append(out[i], f.Path)
		}
	}
	return out
}
