package dispatch

// The plan: the model's own file structure, validated against the blueprints.
//
// A generator does not impose a layout. It states what must exist — the types
// the protocol enumerates, the endpoints it defines — and asks the model how it
// intends to arrange them. The plan is then checked for completeness before a
// single file is generated, so a structure that could never satisfy the
// blueprints costs one call rather than twenty.
//
// See architecture/generation.md, "The plan".

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// PlannedFile is one file the model intends to write.
type PlannedFile struct {
	Path      string   `json:"path"`
	Purpose   string   `json:"purpose"`
	Declares  []string `json:"declares"`
	Serves    []string `json:"serves"`
	DependsOn []string `json:"depends_on"`
}

// Plan is the model's proposed structure for one target.
type Plan struct {
	Target string `json:"target"`
	Root   string `json:"root"`
	// Prepare resolves dependencies before the build — "go mod tidy" and its
	// equivalents. Writing source cannot produce a lockfile, and a build without
	// one fails naming a source file that has nothing wrong with it.
	Prepare string        `json:"prepare"`
	Build   string        `json:"build"`
	Files   []PlannedFile `json:"files"`
}

const planSystemPrompt = `You plan an implementation before writing it.

Given blueprints, you decide how to arrange the implementation into files. The
structure is YOUR decision — choose the arrangement you would actually write.

Output rules, which are absolute:
- Output ONLY a single JSON object. No prose, no explanation, no code fences.
- The first character of your response is {.

Shape:
{
  "target": "orchestrator",
  "root": "server",
  "prepare": "<the dependency-resolution command from the platform blueprint, if it has one>",
  "build": "<the build command from the platform blueprint>",
  "files": [
    {
      "path": "relative/path.ext",
      "purpose": "what this file is for",
      "declares": ["every top-level symbol this file will define"],
      "serves": ["METHOD /path", "..."],
      "depends_on": ["files that must be written before this one"]
    }
  ]
}

"declares" means TOP-LEVEL symbols only — types, functions, methods, constants
and package-level variables. Do NOT list struct fields, local variables, or
anything nested inside another declaration. Name a method as (Type).Method.

Requirements on the plan:
- EVERY type listed in the requirements must be declared by exactly one file.
- EVERY endpoint listed in the requirements must be served by exactly one file.
- No symbol may be declared by two files.
- depends_on must be acyclic and may only name files in this plan.
- Order files so dependencies come before the files that use them. An entry point
  depends on what it constructs, so it comes last.`

var reJSONObject = regexp.MustCompile(`(?s)\{.*\}`)

// ParsePlan reads a plan from a model response, tolerating a wrapping fence.
func ParsePlan(raw string) (*Plan, error) {
	body := stripFence(strings.TrimSpace(raw))
	// A model that adds a sentence before the JSON is common enough to recover
	// from, and the object is unambiguous.
	if m := reJSONObject.FindString(body); m != "" {
		body = m
	}
	var p Plan
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		return nil, fmt.Errorf("the plan was not valid JSON: %w", err)
	}
	if len(p.Files) == 0 {
		return nil, fmt.Errorf("the plan declares no files")
	}
	return &p, nil
}

// ValidatePlan checks a plan against what the blueprints require.
//
// Returns the gaps, so the model can be asked for a revision naming exactly what
// is missing rather than being told to try again.
func ValidatePlan(p *Plan, req *Requirements) []string {
	var gaps []string

	declaredBy := map[string][]string{}
	servedBy := map[string][]string{}
	paths := map[string]bool{}
	for _, f := range p.Files {
		if strings.TrimSpace(f.Path) == "" {
			gaps = append(gaps, "a file has no path")
			continue
		}
		if _, err := safeGeneratedPath("/plan-check", f.Path); err != nil {
			gaps = append(gaps, fmt.Sprintf("%q is not a safe relative path", f.Path))
		}
		if paths[f.Path] {
			gaps = append(gaps, fmt.Sprintf("%q is listed twice", f.Path))
		}
		paths[f.Path] = true
		if strings.TrimSpace(f.Purpose) == "" {
			gaps = append(gaps, fmt.Sprintf("%q has no purpose", f.Path))
		}
		for _, d := range f.Declares {
			declaredBy[d] = append(declaredBy[d], f.Path)
		}
		for _, e := range f.Serves {
			servedBy[strings.Join(strings.Fields(e), " ")] = append(servedBy[e], f.Path)
		}
	}

	// Every required type is placed somewhere.
	var missingTypes []string
	for _, t := range req.Types {
		if len(declaredBy[t]) == 0 {
			missingTypes = append(missingTypes, t)
		}
	}
	if len(missingTypes) > 0 {
		gaps = append(gaps, fmt.Sprintf("no file declares these types the protocol defines: %s",
			strings.Join(missingTypes, ", ")))
	}

	// Every required endpoint is served.
	var missingEndpoints []string
	for _, e := range req.Endpoints {
		if len(servedBy[e]) == 0 {
			missingEndpoints = append(missingEndpoints, e)
		}
	}
	if len(missingEndpoints) > 0 {
		gaps = append(gaps, fmt.Sprintf("no file serves these endpoints: %s",
			strings.Join(missingEndpoints, ", ")))
	}

	// No symbol declared twice — the coherence failure, caught before generating.
	for sym, files := range declaredBy {
		if len(files) > 1 {
			gaps = append(gaps, fmt.Sprintf("%s is declared by more than one file: %s",
				sym, strings.Join(files, ", ")))
		}
	}

	// depends_on names real files and does not cycle.
	for _, f := range p.Files {
		for _, d := range f.DependsOn {
			if !paths[d] {
				gaps = append(gaps, fmt.Sprintf("%q depends on %q, which is not in the plan", f.Path, d))
			}
		}
	}
	if _, err := planOrder(p); err != nil {
		gaps = append(gaps, err.Error())
	}
	return gaps
}

// planOrder returns the files in dependency order.
//
// The first real run of this pipeline failed precisely here: an entry point was
// generated second and called a constructor written fifth, so no amount of
// declaration-passing could have helped — the symbol did not exist yet.
func planOrder(p *Plan) ([]PlannedFile, error) {
	byPath := map[string]PlannedFile{}
	for _, f := range p.Files {
		byPath[f.Path] = f
	}
	var out []PlannedFile
	state := map[string]int{} // 0 unvisited, 1 in progress, 2 done
	var visit func(path string, trail []string) error
	visit = func(path string, trail []string) error {
		switch state[path] {
		case 2:
			return nil
		case 1:
			return fmt.Errorf("depends_on has a cycle: %s", strings.Join(append(trail, path), " -> "))
		}
		state[path] = 1
		f := byPath[path]
		for _, d := range f.DependsOn {
			if _, ok := byPath[d]; !ok {
				continue
			}
			if err := visit(d, append(trail, path)); err != nil {
				return err
			}
		}
		state[path] = 2
		out = append(out, f)
		return nil
	}
	for _, f := range p.Files {
		if err := visit(f.Path, nil); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Order returns the plan's files in dependency order.
func (p *Plan) Order() []PlannedFile {
	ordered, err := planOrder(p)
	if err != nil {
		return p.Files
	}
	return ordered
}

// planPrompt asks for a plan.
func planPrompt(req *Requirements, target, platform, specs, platBP string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Plan a %s implementation for the %s platform.\n\n", target, platform)
	b.WriteString("REQUIREMENTS — every one of these must be placed in your plan.\n\n")
	if len(req.Types) > 0 {
		fmt.Fprintf(&b, "Types the protocol defines (%d). Every one must be declared by exactly one file:\n%s\n\n",
			len(req.Types), strings.Join(req.Types, ", "))
	}
	if len(req.Endpoints) > 0 {
		fmt.Fprintf(&b, "Endpoints this target must serve (%d):\n  %s\n\n",
			len(req.Endpoints), strings.Join(req.Endpoints, "\n  "))
	}
	if c := FormatChecklist(req.Checklist); c != "" {
		b.WriteString(c)
		b.WriteString("\n")
	}
	b.WriteString("\n--- PLATFORM BLUEPRINT ---\n")
	b.WriteString(platBP)
	b.WriteString("\n\n--- PROTOCOL AND ARCHITECTURE BLUEPRINTS ---\n")
	b.WriteString(specs)
	return b.String()
}

// MakePlan asks the model for a structure and validates it, re-planning with the
// specific gaps when it does not satisfy the blueprints.
func MakePlan(provider Provider, req *Requirements, target, platform, specs, platBP string,
	onProgress ProgressFunc) (*Plan, error) {
	if onProgress == nil {
		onProgress = func(Progress) {}
	}
	var lastGaps []string
	for attempt := 1; attempt <= maxFileAttempts; attempt++ {
		status := "planning"
		if attempt > 1 {
			status = "replanning"
		}
		detail := ""
		if len(lastGaps) > 0 {
			detail = lastGaps[0]
		}
		onProgress(Progress{Path: "plan", Status: status, Attempt: attempt, Detail: detail})

		prompt := planPrompt(req, target, platform, specs, platBP)
		if len(lastGaps) > 0 {
			prompt = "Your previous plan was rejected:\n  - " + strings.Join(lastGaps, "\n  - ") +
				"\n\nProduce a corrected plan.\n\n" + prompt
		}
		raw, err := provider.Chat([]Message{
			{Role: "system", Content: planSystemPrompt},
			{Role: "user", Content: prompt},
		})
		if err != nil {
			return nil, fmt.Errorf("planning: %w", err)
		}
		plan, perr := ParsePlan(raw)
		if perr != nil {
			lastGaps = []string{perr.Error()}
			continue
		}
		if gaps := ValidatePlan(plan, req); len(gaps) > 0 {
			lastGaps = gaps
			continue
		}
		return plan, nil
	}
	return nil, fmt.Errorf("no valid plan after %d attempts; last gaps: %s",
		maxFileAttempts, strings.Join(lastGaps, "; "))
}
