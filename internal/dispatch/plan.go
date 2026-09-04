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
	"path/filepath"
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
	// Module is the import path prefix for this project's own packages.
	//
	// Not asked of the model: it is the tenant's name, which the platform
	// blueprint already states ("module <tenant>"), and a fact two files must
	// agree on is not something to have guessed twice.
	Module string `json:"-"`
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
  "root": ".",
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

"root" is the directory the target is generated into, relative to the project
root — and the project root IS the tenant. Everything a tenant owns is scoped to
that one directory, so unless the platform blueprint says otherwise, "root" is
".": the module, its binaries and its packages sit directly under the tenant.

"declares" means TOP-LEVEL symbols only — types, functions, methods, constants
and package-level variables. Do NOT list struct fields, local variables, or
anything nested inside another declaration. Name a method as (Type).Method.

Requirements on the plan:
- EVERY type listed in the requirements must be declared by exactly one file.
- EVERY endpoint listed in the requirements must be served by exactly one file.
- No symbol may be declared by two files.
- depends_on must be acyclic and may only name files in this plan.
- Order files so dependencies come before the files that use them. An entry point
  depends on what it constructs, so it comes last.

Naming. Read this twice; it decides whether the plan can be applied at all.

You do NOT choose names. The blueprints declare them and the requirements below
list them. Use each declared name EXACTLY as written — do not shorten it, drop
its noun, add a qualifier, expand an abbreviation or substitute a synonym.

- Endpoints arrive with a declared operation. The platform blueprint states how
  an operation is spelled in this language — path constant, handler, request
  type, response type — and that mapping is the only permitted spelling.
- Store operations arrive named. GetAgent is not Get. AppendObservation is not
  Add. ClaimNamespace is not Claim.
- A required type keeps the exact spelling of the type table.
- Where the tenant ALREADY declares a name that satisfies a requirement, keep
  it. A plan may only change what the blueprints changed.

If a name you need is in none of this, say so in "purpose" and use the closest
declared name — do NOT invent one. A missing name is a gap in the blueprint and
it will be fixed there.

Renaming a symbol nothing asked you to rename makes every file that used the
old name stale, and they will all be regenerated. Assume the tenant listed
below is correct unless a requirement contradicts it.`

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
func ValidatePlan(p *Plan, req *Requirements, st *TenantState) []string {
	var gaps []string

	// Ownership, enforced rather than requested.
	//
	// The prompt already tells the model which files belong to another component.
	// A plan that ignores it must still be rejected here — an instruction the
	// model may decline is not a guard, and the file it declines about is a
	// running component's entry point.
	if st != nil {
		for _, f := range p.Files {
			clean := filepath.Clean(f.Path)
			if owner, taken := st.Owned[clean]; taken {
				gaps = append(gaps, fmt.Sprintf("%q belongs to the %s component — import it, do not re-plan it", f.Path, owner))
			}
			if clean == "go.mod" && st.Module != "" {
				gaps = append(gaps, fmt.Sprintf("go.mod already exists and declares module %s — do not plan it", st.Module))
			}
			if dir := filepath.ToSlash(filepath.Dir(clean)); strings.HasPrefix(dir, "cmd/") && dir != "cmd/"+p.Target {
				gaps = append(gaps, fmt.Sprintf("%q is in another component's command directory — your entry point is cmd/%s/main.go", f.Path, p.Target))
			}
		}
	}

	declaredBy := map[string][]string{} // package-qualified — collisions
	placedAt := map[string][]string{}   // bare name — placement
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
			// Two indexes, because two questions are being asked.
			//
			// "Is this type placed anywhere?" is answered by the bare name: a
			// required type satisfies the requirement wherever it lives.
			//
			// "Is this symbol declared twice?" is answered package-qualified. A
			// symbol collides only within ONE package, and platforms/go now
			// specifies a module with cmd/ and internal/ packages — so
			// observability.NewRegistry and orchestrator.NewRegistry are different
			// symbols, and a plan naming both was rejected as incoherent when it
			// was correct.
			placedAt[d] = append(placedAt[d], f.Path)
			// Also under the bare name, so a declared operation is found
			// whether the plan writes it as "GetAgent" or as
			// "(*Registry).GetAgent". A store operation is nearly always a
			// method, and looking it up only under the plan's exact spelling
			// would report every one of them missing.
			if bare := bareSymbol(d); bare != d {
				placedAt[bare] = append(placedAt[bare], f.Path)
			}
			key := planPackage(f.Path) + "." + d
			declaredBy[key] = append(declaredBy[key], f.Path)
		}
		for _, e := range f.Serves {
			servedBy[strings.Join(strings.Fields(e), " ")] = append(servedBy[e], f.Path)
		}
	}

	// Every required type is placed somewhere.
	var missingTypes []string
	for _, t := range req.Types {
		if len(placedAt[t]) == 0 {
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

	// Every store operation the blueprint declares for this component is
	// declared by some file, under the declared NAME.
	//
	// Validated rather than requested. The plan prompt asks for these names and
	// a plan that renames one is not a worse plan — it is a plan that cannot be
	// applied to the existing tenant, because every caller is written against
	// the name the blueprint states. This is the check that turns
	// schemas/common's "a declared name is binding" into something the pipeline
	// enforces instead of hoping for.
	var missingOps []string
	for _, op := range req.Operations {
		if len(placedAt[op]) == 0 {
			missingOps = append(missingOps, op)
		}
	}
	if len(missingOps) > 0 {
		gaps = append(gaps, fmt.Sprintf(
			"architecture/storage declares these operations for this component and no file declares them "+
				"under that name (they are binding — do not shorten, requalify or rename): %s",
			strings.Join(missingOps, ", ")))
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
func planPrompt(req *Requirements, target, platform, specs, platBP string, st *TenantState) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Plan a %s implementation for the %s platform.\n\n", target, platform)
	// Before the requirements, because it changes what satisfying them means: a
	// type the tenant already declares is satisfied by importing it.
	if ts := st.FormatTenantState(target); ts != "" {
		b.WriteString(ts)
	}
	b.WriteString("REQUIREMENTS — every one of these must be placed in your plan.\n\n")
	if bd := FormatBindings(req.Bindings); bd != "" {
		// The blueprint's declared consumption, with fields. Not every type the
		// protocol defines — see bindings.go for what demanding all 54 cost.
		b.WriteString(bd)
		b.WriteString("\nEvery type above must be declared by exactly one file.\n\n")
	} else if len(req.Types) > 0 {
		fmt.Fprintf(&b, "Types this component consumes (%d). Every one must be declared by exactly one file:\n%s\n\n",
			len(req.Types), strings.Join(req.Types, ", "))
	}
	// Endpoints with their DECLARED operation, so the plan spells its symbols
	// from the blueprint's name rather than from the path. Falls back to the
	// wire list for a blueprint written before the Operation column existed.
	if len(req.EndpointOps) > 0 {
		fmt.Fprintf(&b, "Endpoints this target must serve (%d). The operation is the DECLARED\n"+
			"NAME — spell every symbol for this endpoint from it, using the platform\n"+
			"blueprint's mapping. Do not name anything from the path:\n", len(req.EndpointOps))
		for _, e := range req.EndpointOps {
			fmt.Fprintf(&b, "  %-7s %-38s operation: %s\n", e.Method, e.Path, e.Operation)
		}
		b.WriteString("\n")
	} else if len(req.Endpoints) > 0 {
		fmt.Fprintf(&b, "Endpoints this target must serve (%d):\n  %s\n\n",
			len(req.Endpoints), strings.Join(req.Endpoints, "\n  "))
	}
	if len(req.Operations) > 0 {
		fmt.Fprintf(&b, "Store operations this component owns (%d), named as architecture/storage\n"+
			"declares them. Use these names EXACTLY — they are what every caller is\n"+
			"written against:\n  %s\n\n",
			len(req.Operations), wrapList(req.Operations, 70, "  "))
	}
	if len(req.UnnamedEndpoints) > 0 {
		// Stated, never guessed. schemas/architecture requires the Operation
		// column; a row without one is a gap in the blueprint, and filling it
		// in here is how a pipeline becomes the specification.
		fmt.Fprintf(&b, "These endpoints have NO declared operation in their blueprint (%d).\n"+
			"Name them from the nearest declared vocabulary and say in \"purpose\" that\n"+
			"the blueprint does not name them:\n  %s\n\n",
			len(req.UnnamedEndpoints), strings.Join(req.UnnamedEndpoints, "\n  "))
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
	st *TenantState, onProgress ProgressFunc) (*Plan, error) {
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

		prompt := planPrompt(req, target, platform, specs, platBP, st)
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
		// The target is the CALLER's, not the model's. It was being set after
		// MakePlan returned, so validation read whatever the model had put in the
		// JSON — "orchestrator" — and told a content build its entry point was
		// cmd/orchestrator/main.go. The model complied, and the ownership check
		// then rejected the file the same guard had just demanded. A guard reading
		// a value the thing it guards supplied can enforce the bug.
		plan.Target = target
		if gaps := ValidatePlan(plan, req, st); len(gaps) > 0 {
			lastGaps = gaps
			continue
		}
		return plan, nil
	}
	return nil, fmt.Errorf("no valid plan after %d attempts; last gaps: %s",
		maxFileAttempts, strings.Join(lastGaps, "; "))
}

// planPackage is the package a planned file will belong to, from its directory.
//
// At plan time the code does not exist, so the directory is the only signal —
// and it is a good one: Go's convention is that a package's name matches the
// directory holding it, and the plan's own paths are what generation will write.
// A file at the plan root belongs to the root package, whatever it is called.
func planPackage(path string) string {
	dir := filepath.Dir(filepath.Clean(path))
	if dir == "." || dir == string(filepath.Separator) {
		return ""
	}
	return filepath.Base(dir)
}
