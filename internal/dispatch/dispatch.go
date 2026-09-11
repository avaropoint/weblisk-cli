package dispatch

// Loads blueprints, constructs prompts, sends to the user's configured
// AI model, parses the response into code files, and writes them to disk.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// GeneratedFile represents a single file extracted from AI output.
type GeneratedFile struct {
	Path    string // relative file path
	Content string // file content
	Lang    string // language (go, js, toml, etc.)
}

// ServerInit generates orchestrator code using the AI model.
func ServerInit(root, platform string) error {
	return SupervisedComponentInit(root, Orchestrator(), platform)
}

// SupervisedComponentInit runs a component build and resumes it across provider
// outages.
//
// Generation is cache-backed per file, so a run that died at file 32 has banked
// 31 and resuming costs only what remains. Five separate tenant builds died to
// provider instability and each one needed a person to notice and retype the
// command; this is that person.
func SupervisedComponentInit(root string, c Component, platform string) error {
	err := Supervise(
		func(attempt int) error {
			if attempt > 1 {
				fmt.Printf("  Resuming (attempt %d) — completed files are reused from cache\n\n", attempt)
			}
			return ComponentInit(root, c, platform)
		},
		func(attempt int, wait time.Duration, err error) {
			fmt.Printf("\n  [wait] the AI provider is failing; resuming in %s (attempt %d)\n        %s\n\n",
				wait.Round(time.Second), attempt+1, firstLine([]string{err.Error()}))
		},
	)
	// A quota or session limit is the one provider failure no amount of waiting
	// inside a build can absorb — it is measured in hours, not seconds. It is
	// not retried, and the operator is told the thing they actually need to
	// know: nothing generated so far has been lost.
	if f := FaultOf(err); f != nil && f.Class() == FaultPermanent && permanentMessage(f.Message) {
		fmt.Printf("\n  Nothing generated so far is lost — every completed file is cached.\n"+
			"  When the limit clears, run this and only what remains is generated:\n"+
			"    %s\n\n", ResumeCommand(c))
	}
	return err
}

// ComponentInit generates one component from the blueprints that declare it.
//
// The pipeline is not the orchestrator's — it is the same for every component
// whose architecture blueprint states a declaration: one resolved graph, a plan
// validated against it, per-file generation, build-and-repair, and conformance.
// The target names which blueprint is the root and which assertions are this
// component's; nothing else in the loop varies. A second component built by a
// second pipeline would drift from the first, and the divergence would show up
// as a component that passes its own checks and fails the system's.
func ComponentInit(root string, c Component, platform string) error {
	if bad := c.Valid(); bad != "" {
		return fmt.Errorf("cannot generate: %s", bad)
	}
	// kind chooses the blueprint and the assertions; key identifies THIS
	// instance's plan, manifest and records. See component.go on why keying
	// both by kind alone let one agent's rebuild delete another's files.
	target, key := c.Kind, c.Key()
	// And WHERE its files go, read from the platform blueprint rather than
	// assumed. See layout.go: this is what the first switch to this pipeline
	// was missing, and it is why the switch was reverted.
	self := LayoutOf(c, platform)
	provider, err := RequireProvider()
	if err != nil {
		return err
	}

	// One resolution, before anything reads it. The plan, the checklist and the
	// per-file prompts all come from THIS graph — see ResolveGraph for what went
	// wrong when they each had their own list.
	graph, err := ResolveGraph(root, c, platform)
	if err != nil {
		return fmt.Errorf("resolving blueprints: %w", err)
	}
	// The corpus against its own schemas, BEFORE planning.
	//
	// Reported here because a schema fault in a blueprint this component reads
	// is a fault in what the model is about to be told, and finding it after
	// forty minutes of generation is finding it too late.
	//
	// Scoped to the blueprints THIS component reads: a missing `## Security`
	// section in an agent blueprint the orchestrator never sees is a real fault
	// and somebody else's. `weblisk validate` reports the whole corpus.
	// ValidateStructure, not ValidateCorpus: a structure rule is answerable from
	// one blueprint and its schema, so it holds against this scoped graph. The
	// relationship rules need the whole corpus and are `weblisk validate`'s.
	if findings := ValidateStructure(graph.Map); len(findings) > 0 {
		fmt.Printf("  [warn] %s in the blueprints this component reads:\n",
			plural(len(findings), "schema finding"))
		fmt.Print(indentBlock(FormatFindings(findings), "  "))
		fmt.Println("         Generation continues — these are faults in the specification,")
		fmt.Println("         and `weblisk validate` reports the whole corpus.")
		fmt.Println()
	}

	// Without the platform blueprint, which planPrompt sends as its own labelled
	// section immediately before this. See JoinedExcept.
	specs := graph.JoinedExcept(PlatformBlueprint(platform))

	// Recorded before anything is generated, so a run that fails part-way still
	// leaves a statement of what it was reading. architecture/cli requires the
	// blueprint version and the provider to be part of a hub's provenance, and
	// they were printed rather than recorded — so the artifact carried no
	// answer to the one question this product exists to answer.
	SetProvenance(provenanceOf(graph, provider))
	// And SAID, not only recorded. Recorded answers "what was this built from"
	// afterwards; said answers "am I about to build from the right thing".
	AnnounceSources(ResolveSources(root))

	platBP, err := LoadBlueprint(root, PlatformBlueprint(platform))
	if err != nil {
		return fmt.Errorf("loading platform blueprint: %w", err)
	}

	// Plan-driven generation. The blueprints state what must exist; the MODEL
	// decides how to arrange it, and the plan is validated against the
	// requirements before a single file is generated. See
	// architecture/generation.md.
	req := GatherRequirements(graph, target)
	if req.DeclarationError != nil {
		return fmt.Errorf("the declaration block is malformed: %w\n"+
			"  Nothing was generated. Fix the declaration, or remove it to fall back\n"+
			"  to the sections it replaces", req.DeclarationError)
	}
	if req.FromDeclaration {
		fmt.Printf("  Requirements read from %s's declaration block\n", target)
	}
	if len(req.DeclarationOmissions) > 0 {
		// Reported, never filled in. A contract completed by the sections it
		// replaced is not a declaration.
		return fmt.Errorf("%s's contract does not declare %s that protocol/spec requires of it: %s\n"+
			"  Add them to the declaration's `serves:` list",
			target, plural(len(req.DeclarationOmissions), "endpoint"),
			strings.Join(req.DeclarationOmissions, ", "))
	}
	if len(req.Types) > 0 || len(req.Endpoints) > 0 {
		fmt.Printf("  Platform: %s\n", platform)
		fmt.Print(graph.Describe())
		if len(graph.Missing) > 0 {
			// Stated, never silent. A declared requirement this installation does
			// not carry is a gap in what the model was given, and the output should
			// be read knowing that.
			fmt.Printf("  [warn] declared requirements not found: %s\n", strings.Join(graph.Missing, ", "))
		}
		fmt.Printf("  Required: %s\n", req.Summary())
		if len(req.UnboundTypes) > 0 {
			// Stated, never supplied. A type the protocol defines that this
			// component's bindings do not claim is a gap in the blueprint's
			// contract, and quietly adding it would hide the gap while putting the
			// tooling's judgement back in charge of what a component needs.
			fmt.Printf("  [note] %d types are defined by the protocol and bound by no contract here.\n"+
				"         They are not required of this component. If one is genuinely needed,\n"+
				"         the blueprint's bindings are where that is declared.\n", len(req.UnboundTypes))
		}
		fmt.Println()

		// Reuse the plan when the requirements have not changed. Without this the
		// model re-plans every run — ten files where it planned twelve — and every
		// per-file cache entry is invalidated by a plan entry nobody changed.
		// What this tenant already contains, before anything is planned into it.
		st := ReadTenantState(root, key, self)

		cache := NewGenerationCache(root)
		pk := planKey(req, key, platform, platBP, planSystemPrompt+st.Shape(), self.FormatLayout()+specs)
		plan := cache.GetPlan(pk)
		if plan != nil {
			fmt.Println("  Plan reused — requirements unchanged since the last run")
		} else {
			var perr error
			plan, perr = MakePlan(provider, req, self, platform, specs, platBP, st, printProgress)
			if perr != nil {
				return perr
			}
			cache.PutPlan(pk, plan)
		}
		// The tenant's name is the module path — platforms/go states it as
		// "module <tenant>". Set here rather than asked of the model, because a
		// fact two files must agree on should not be guessed twice.
		plan.Module = moduleNameFor(root)
		// Set here, not trusted from the model's JSON: the manifest that decides
		// which files a rebuild may delete is keyed by it. Target stays the KIND,
		// which is what the entry-point rule reads.
		plan.Target = target
		plan.Owner = key
		plan.Platform = platform
		// plan.Root is NOT overridden here, on any platform. Setting it to the
		// component's directory looks obviously right and breaks the build, the
		// import prefix, the manifest coordinate space and reconcile's
		// stale-file removal all at once. The component's directories reach the
		// plan as a PREFIX inside its own file paths instead — `self` above is
		// what supplies them, and what the plan was checked against.

		fmt.Printf("\n  Plan accepted: %d files in %s/\n", len(plan.Files), plan.Root)
		for _, f := range plan.Order() {
			fmt.Printf("    %s — %s\n", f.Path, f.Purpose)
		}
		fmt.Println()

		// One writer per target. Two generations sharing a directory produced
		// twenty-one redeclaration errors between two correct plans, which reads
		// exactly like a pipeline fault and is not one.
		release, lerr := AcquireTargetLock(root, plan.Root)
		if lerr != nil {
			return lerr
		}
		defer release()

		// The plan is a complete statement of what the target consists of, not an
		// addition to whatever is already there. A previous run that split the
		// registry differently left routing.go beside a new registry.go, and every
		// symbol in it was declared twice — nine correct files and one leftover,
		// producing a build no repair could fix because no file was wrong.
		if rec, rerr := ReconcileTarget(root, plan, st); rerr != nil {
			return fmt.Errorf("reconciling %s: %w", plan.Root, rerr)
		} else {
			for _, f := range rec.Stale {
				fmt.Printf("  Removed %s — written by a previous run, not in this plan\n", f)
			}
			if len(rec.Foreign) > 0 {
				// Not generation's to remove, and not generation's to hide.
				fmt.Printf("  [note] left in place, not written by generation: %s\n",
					strings.Join(rec.Foreign, ", "))
			}
			for _, f := range rec.Retained {
				fmt.Printf("  Kept %s — this plan was told not to write it\n", f)
			}
			if len(rec.Stale) > 0 || len(rec.Foreign) > 0 || len(rec.Retained) > 0 {
				fmt.Println()
			}
		}

		// architecture/generation: a rebuild is a decision per file and the
		// default answer is keep. Run BEFORE generation, so a refusal stops the
		// run rather than being discovered after files are written.
		decisions := DecideRebuild(root, plan, PriorRecords(root, key), graph.Map)
		if rep := ReportDecisions(decisions); rep != "" {
			fmt.Print(rep)
			fmt.Println()
		}
		var edited, unowned []string
		keep := map[string]bool{}
		for _, d := range decisions {
			switch d.RebuildVerdict {
			case RebuildEdited:
				edited = append(edited, d.Path)
				continue
			case RebuildUnowned:
				unowned = append(unowned, d.Path)
				continue
			}
			if !d.RebuildVerdict.Generates() {
				keep[d.Path] = true
			}
		}
		// Refused, not overwritten. Adopting the change or discarding it is a
		// decision for whoever made it; generation's job is to ask — and to ask
		// the right question, which is not the same one in both cases.
		if len(edited) > 0 || len(unowned) > 0 {
			var b strings.Builder
			fmt.Fprintf(&b, "%d file(s) cannot be safely regenerated. Nothing was written.\n",
				len(edited)+len(unowned))
			if len(edited) > 0 {
				fmt.Fprintf(&b, "\n  Changed since generation wrote them: %s\n"+
					"  Fold the change into the blueprints and delete the file to have it\n"+
					"  generated again, or keep the file and leave it out of the plan.\n",
					strings.Join(edited, ", "))
			}
			if len(unowned) > 0 {
				// A distinct fact and a distinct remedy. Reporting this as an
				// edit sends somebody looking for a change nobody made.
				fmt.Fprintf(&b, "\n  Present with no record of generation writing them: %s\n"+
					"  Generation will not overwrite a file it cannot show it authored.\n"+
					"  If these are a previous run's output, delete them and they will be\n"+
					"  generated again. If they are hand-written, leave them out of the plan.\n",
					strings.Join(unowned, ", "))
			}
			return fmt.Errorf("%s", strings.TrimRight(b.String(), "\n"))
		}

		generationStarted := time.Now()
		files, gerr := GenerateTarget(provider, plan, platform, graph.Map, graph.Order, platBP, root,
			printProgress, req.Checklist, req.Bindings, st, keep, req.EndpointOps)
		if gerr != nil {
			return gerr
		}
		generationTook := time.Since(generationStarted)
		fmt.Printf("\n  [ok] Generated %d files in %s/ (%s)\n\n", len(files), plan.Root,
			generationTook.Round(time.Second))
		// What the provider said about its own quota while doing that work.
		// Reported here because the next steps also need it, and because a run
		// that ends on a limit should have been able to see it coming.
		if note := QuotaNote(); note != "" {
			fmt.Printf("  %s\n\n", note)
		}
		RecordWrittenWith(root, plan, files, graph.Map)

		// Whether running the component found a fault. Held rather than returned
		// at once so the full report is printed first.
		var conformanceFault error

		// Layer 2: build, and feed failures back. Generating blind and reporting
		// success is how eleven files that do not compile get called finished.
		if plan.Build != "" {
			result, repaired, rerr := BuildAndRepair(provider, plan, root, platBP, files, printProgress, req.Checklist, graph.Map,
				func(current []GeneratedFile) ([]ConformanceResult, string, error) {
					bin := builtBinary(root, plan.Build, c)
					if bin == "" {
						return nil, "", nil
					}
					res, out, err := RunConformance(root, bin, target, graph.Map, printProgress)
					if err != nil {
						return res, out, err
					}
					// Interop faults are repairable faults. Feeding them back is the
					// whole point: found by hand they cost eight rounds of patching,
					// and every one of them is a wrong constant or a wrong order in
					// a generated file.
					ires, iout, ierr := RunInterop(root, bin, target, graph.Map, printProgress)
					return append(res, ires...), out + iout, ierr
				})
			if rerr != nil {
				return rerr
			}
			files = repaired
			// Re-record AFTER repair. The manifest is a record of what is ON DISK,
			// and repair rewrites files — so recording only after generation made
			// every repaired file differ from its record, and the next run refused
			// seven of them as hand-edited. A repair is generation's own work; only
			// a change generation did not make is an edit.
			RecordWrittenWith(root, plan, files, graph.Map)
			if !result.OK {
				reportChecklist(EvaluateChecklistAgainst(req.Checklist, files, graph.Map))
				fmt.Printf("  [failed] the implementation does not build\n\n")
				fmt.Println(indentBlock(result.Output, "    "))
				return fmt.Errorf("build failed: %s", plan.Build)
			}
			fmt.Printf("  [ok] builds with %q\n\n", plan.Build)

			// Layer 4's final word. The loop has already run the component and
			// repaired what it could; this is the report of where it ended.
			bin := builtBinary(root, plan.Build, c)
			if bin == "" {
				// Said, not skipped. The build passed, so the absence of a
				// conformance section below is otherwise indistinguishable from
				// a clean one — and this component was never run at all.
				fmt.Printf("  [note] conformance did not run: %q produced no binary this tool could find,\n"+
					"         and there is none at %s. Nothing below reports on running this component.\n\n",
					plan.Build, BinaryPath(c))
			} else {
				results, output, cerr := RunConformance(root, bin, target, graph.Map, nil)
				conformanceFault = reportConformance("Conformance L1 (this component alone)", results, output, cerr)

				// Interoperability, against the real orchestrator of this tenant.
				// A component can satisfy every assertion about itself and be
				// unable to register — eight faults were found that way by hand.
				iresults, ioutput, ierr := RunInterop(root, bin, target, graph.Map, nil)
				if len(iresults) > 0 {
					if ifault := reportConformance("Conformance L4 (against this tenant's orchestrator)",
						iresults, ioutput, ierr); ifault != nil && conformanceFault == nil {
						conformanceFault = ifault
					}
				}
			}
		}

		// What the blueprints say, as read by the model — the authority.
		//
		// Bounded, because this step's failure is a warning and the build
		// continues without it. Unbounded it once spent sixty-five minutes on
		// commentary the build does not depend on; the budget comes from how
		// long generation itself took, since this step reads every generated
		// file and scales with them. See advisory.go.
		advisory, bounded := AdvisoryProvider(provider, generationTook)
		if !bounded {
			fmt.Printf("  [note] this provider cannot be time-bounded, so verification runs unbounded\n")
		}
		if verdicts, verr := SelfVerify(advisory, files, req.Checklist); verr == nil {
			reportVerdicts(verdicts, req.Checklist)
		} else {
			fmt.Printf("  [warn] verification against the assertions could not be read: %v\n\n", verr)
		}
		// What the structural checks think — advice, and useful mainly when it
		// disagrees with the above.
		reportChecklist(EvaluateChecklistAgainst(req.Checklist, files, graph.Map))

		// Reported LAST, after everything a person needs in order to act on it.
		// Returning at the point of detection would have hidden the checklist
		// and the model's own verdicts, which are where the cause usually is.
		if conformanceFault != nil {
			return conformanceFault
		}
		return nil
	}

	// Reached only when the graph states no types and no endpoints for this
	// target. For the orchestrator that means a platform with no manifest, and
	// the single-shot path below still produces something. For any other
	// component it means its blueprint declared no contract, and generating
	// from an empty specification would produce a file nobody can grade.
	if target != "orchestrator" {
		return fmt.Errorf("%s: its blueprint declares no types and no endpoints — "+
			"nothing to build against. Check %s bindings", target, targetBlueprint(target))
	}

	prompt := buildOrchestratorPrompt(specs, platBP, platform)

	fmt.Println("  Generating orchestrator code (no manifest for this platform)...")
	fmt.Printf("  Platform: %s\n", platform)
	fmt.Printf("  Target:   %s\n", root)
	fmt.Println()

	response, err := provider.Chat([]Message{
		{Role: "system", Content: orchestratorSystemPrompt},
		{Role: "user", Content: prompt},
	})
	if err != nil {
		return fmt.Errorf("AI generation failed: %w", err)
	}

	files := parseGeneratedFiles(response)
	if len(files) == 0 {
		return fmt.Errorf("AI returned no code files — try a different model or check the response")
	}

	// The tenant folder is the target. A component's own directory comes from the
	// plan; this fallback path has no plan, so it writes at the tenant root.
	targetDir := root
	written, err := writeGeneratedFiles(targetDir, files)
	if err != nil {
		return err
	}

	fmt.Printf("  [ok] Generated %d files\n", written)
	for _, f := range files {
		fmt.Printf("    %s\n", f.Path)
	}
	fmt.Println()

	return nil
}

// The three named components, generated the way every other component is.
//
// # Why this was reverted once, and what changed
//
// These were switched to SupervisedComponentInit on 2026-09-09 and reverted
// the same day. The switch set plan.Root to the component's directory,
// `agents/<name>`, because that is where the single-shot generator put an
// agent — and everything else in the pipeline assumes plan.Root is ".":
// RunBuild runs at the tenant root, the module path handed to every file
// prompt has no `agents/<name>` segment, and manifests record tenant-root
// paths while ReadTenantState reads them as tenant-root paths too. A real
// `weblisk agent create billing` planned eighteen files at tenant-root paths
// and reasoned about importing the tenant's own packages, because it had been
// told it was part of the tenant module. It was right; the plan.Root override
// was not.
//
// Underneath sat a real disagreement: the single-shot path made an agent a
// SELF-CONTAINED MODULE — its prompt asked for "Build configuration (go.mod or
// package.json)" — and the pipeline refuses to plan a go.mod the tenant
// already declares. That looked like a decision to be made. It is not one:
// platforms/go.md roots one module at the tenant and maps `agents/<name>` to
// cmd/<name> + internal/agents/<name>, while platforms/cloudflare.md gives
// each Worker its own wrangler.toml. The blueprints already answer it, and
// they answer it differently per platform. See layout.go.
//
// So plan.Root stays "." on every platform, and the component's directories
// appear as a PREFIX inside the plan's own paths. Layout is what supplies
// them, and what the plan is checked against.

// AgentCreate generates one agent from the blueprints that declare it.
func AgentCreate(root, name, platform string) error {
	return SupervisedComponentInit(root, Agent(name), platform)
}

// DomainCreate generates one domain controller.
func DomainCreate(root, name, platform string) error {
	return SupervisedComponentInit(root, Domain(name), platform)
}

// GatewayCreate generates the application gateway.
func GatewayCreate(root, platform string) error {
	return SupervisedComponentInit(root, Gateway(), platform)
}

// PatternApply generates a pattern implementation using the AI model.
func PatternApply(root, pattern, resource string) error {
	provider, err := RequireProvider()
	if err != nil {
		return err
	}

	patternBP, err := LoadBlueprint(root, PatternBlueprint(pattern))
	if err != nil {
		return fmt.Errorf("loading pattern blueprint: %w", err)
	}

	prompt := buildPatternPrompt(patternBP, pattern, resource)

	fmt.Printf("  Applying pattern: %s\n", pattern)
	fmt.Printf("  Resource: %s\n", resource)
	fmt.Println()

	response, err := provider.Chat([]Message{
		{Role: "system", Content: patternSystemPrompt},
		{Role: "user", Content: prompt},
	})
	if err != nil {
		return fmt.Errorf("AI generation failed: %w", err)
	}

	files := parseGeneratedFiles(response)
	if len(files) == 0 {
		return fmt.Errorf("AI returned no code files — try a different model or check the response")
	}

	written, err := writeGeneratedFiles(root, files)
	if err != nil {
		return err
	}

	fmt.Printf("  [ok] Applied pattern — %d files generated\n", written)
	for _, f := range files {
		fmt.Printf("    %s\n", f.Path)
	}
	fmt.Println()

	return nil
}

// RequireProvider creates and validates an AI provider.
func RequireProvider() (Provider, error) {
	// Retry is applied by NewProvider itself, so this and every other path that
	// talks to a model gets it without asking. See provider.go.
	provider, err := NewProvider()
	if err != nil {
		// The walk already asked every provider this machine has and reported
		// exactly why each refused. Wrapping that in "configure an AI provider,
		// e.g. WL_AI_PROVIDER=grok" would answer a question the operator did not
		// ask with advice they have already taken.
		if errors.Is(err, ErrNothingReady) {
			return nil, err
		}
		return nil, fmt.Errorf("AI provider required for code generation\n\n"+
			"  Configure an AI provider — run `weblisk providers` to see what this machine has.\n"+
			"  No account needed — runs on this machine:\n"+
			"    WL_AI_PROVIDER=claude-code  (Claude Code CLI, uses its own login)\n"+
			"    WL_AI_PROVIDER=grok         (Grok CLI, uses its own login)\n"+
			"    WL_AI_PROVIDER=ollama       (local Ollama, default http://localhost:11434)\n"+
			"    WL_AI_PROVIDER=local-cli    (any local tool; set WL_AI_COMMAND)\n\n"+
			"  Hosted, requires a vendor key or WL_AI_KEY:\n"+
			"    WL_AI_PROVIDER=xai          (XAI_API_KEY)\n"+
			"    WL_AI_PROVIDER=openai       (OPENAI_API_KEY)\n"+
			"    WL_AI_PROVIDER=anthropic    (ANTHROPIC_API_KEY)\n\n"+
			"  Anything else: WL_AI_BASE_URL (OpenAI-compatible HTTP).\n"+
			"  Set in .env or environment: %w", err)
	}

	// This pre-flight is NOT skipped when the readiness walk has already asked
	// the same backend the same question, and the reason is worth stating
	// because skipping it looks like an obvious saving.
	//
	// It was skipped, briefly. The walk probes a THROWAWAY provider — probeReady
	// builds its own instance and discards it — while the instance returned here
	// is a second, fresh one. `observed`, the model the tool says it actually
	// used, is per-instance and is set only by a real call. So skipping meant the
	// provider handed to ComponentInit had never been asked anything, and
	// SetProvenance(provenanceOf(graph, provider)) recorded an EMPTY model.
	//
	// A build that does not record which model wrote a file cannot answer the
	// question the whole chain exists to answer. That is worth one small call —
	// four tokens of "ok" — on every build. The line below it, naming the model,
	// disappeared too.
	fmt.Println("  Verifying AI provider...")
	_, testErr := provider.Chat([]Message{
		{Role: "user", Content: readinessPrompt},
	})
	if testErr != nil {
		return nil, fmt.Errorf("AI provider not reachable: %w\n\n"+
			"  Check your WL_AI_* configuration", testErr)
	}
	// Which model, in the run's own words rather than in the configuration's.
	// WL_AI_MODEL unset means "the tool's default", and the tool's default is
	// not a constant — so the only trustworthy answer comes from the response.
	if m, ok := Underlying(provider).(interface{ ModelUsed() string }); ok {
		if used := m.ModelUsed(); used != "" {
			fmt.Printf("  [ok] AI provider connected — generating with %s\n", used)
			if os.Getenv("WL_AI_MODEL") == "" {
				fmt.Printf("       (no WL_AI_MODEL set, so this is the tool's default and may change\n" +
					"        between runs — set it to pin what generates this tenant)\n")
			}
			return provider, nil
		}
	}
	fmt.Println("  [ok] AI provider connected")

	return provider, nil
}

// DiscoverProvider checks for available AI providers and returns info.
func DiscoverProvider() string {
	api := os.Getenv("WL_AI_PROVIDER")
	model := os.Getenv("WL_AI_MODEL")

	// The run's own choice outranks the environment, and is usually the ONLY
	// place a choice exists: ChooseProvider pins the backend it walked to
	// without setting WL_AI_PROVIDER. Reading only the environment printed
	// "AI Model: not configured" at the top of a build that had just announced
	// which provider it settled on, two lines above.
	if kind, m, pinned := SelectedProvider(); pinned {
		api = string(kind)
		if model == "" {
			model = m
		}
	}

	if api == "" {
		// Not "not configured": this line is printed BEFORE the command reaches
		// RequireProvider, so at this moment nothing has been chosen yet — which
		// is not the same as nothing being available. A machine with three
		// working CLIs on it read "not configured" and then generated a hub.
		return "settled when generation starts (`weblisk providers` lists this machine's)"
	}

	info := api
	if model != "" {
		info += " (" + model + ")"
	}

	// Already proved, in this process, for this exact backend AND model, by the
	// readiness walk. Reporting it here costs nothing; asking again would be a
	// third call in one build, after ResolveReady's and RequireProvider's.
	// Keyed on the model too, so a --model or WL_AI_MODEL the walk never asked
	// about is not reported ready on the strength of one that it did.
	if AlreadyVerified(ProviderKind(api), model) {
		return info + " [ready]"
	}

	// Not the retrying provider: this answers "what is configured", and a
	// status line that takes six minutes to print is not a status line.
	provider, err := newRawProvider()
	if err != nil {
		return info + " [error: " + err.Error() + "]"
	}

	_, err = provider.Chat([]Message{
		{Role: "user", Content: "Respond with exactly: ok"},
	})
	if err != nil {
		// "unreachable" reads as not installed or misconfigured, and this line
		// is printed at the top of every build. A run that says "unreachable"
		// and then generates thirty-five files has told the operator something
		// false. An overload is the provider being busy, which is a different
		// fact and one the build will ride out.
		if isTransient(err) {
			return info + " [busy — will retry]"
		}
		if f := FaultOf(err); f != nil && f.Message != "" {
			return info + " [" + firstErrorLine(f) + "]"
		}
		return info + " [unreachable]"
	}

	return info + " [ready]"
}

// ProviderStatus returns a JSON-serializable status of the AI provider.
func ProviderStatus() map[string]any {
	status := map[string]any{
		"provider": os.Getenv("WL_AI_PROVIDER"),
		"model":    os.Getenv("WL_AI_MODEL"),
		"base_url": os.Getenv("WL_AI_BASE_URL"),
		"has_key":  os.Getenv("WL_AI_KEY") != "",
	}

	// Reporting configuration must not CHANGE it. newRawProvider walks — and
	// now asks each candidate to answer — when nothing has been chosen, so
	// calling it from a status printer would spend real calls and pin a backend
	// as a side effect of describing one.
	kind, model, pinned := SelectedProvider()
	if !pinned && strings.TrimSpace(os.Getenv("WL_AI_PROVIDER")) == "" {
		status["status"] = "not chosen yet"
		status["detail"] = "no --provider and no WL_AI_PROVIDER; a build settles this with ResolveReady"
		return status
	}
	if pinned {
		status["provider"] = string(kind)
		if status["model"] == "" {
			status["model"] = model
		}
	}

	_, err := newRawProvider()
	if err != nil {
		status["status"] = "error"
		status["error"] = err.Error()
	} else {
		status["status"] = "configured"
	}

	return status
}

// PrintProviderStatus shows the current AI provider configuration.
func PrintProviderStatus() {
	s := ProviderStatus()
	data, _ := json.MarshalIndent(s, "  ", "  ")
	fmt.Printf("  AI Provider:\n  %s\n\n", string(data))
}

// Prompt Construction

const orchestratorSystemPrompt = `You are a code generation agent for the Weblisk framework.
You generate complete, working orchestrator server implementations.

Rules:
- Generate ALL required files for a fully working implementation
- Each file must start with a comment: // filename: <path>
- Use ONLY standard library (no external dependencies)
- Follow the protocol specification EXACTLY
- Include all protocol endpoints, auth, registration, audit
- The code must compile and run immediately
- Do NOT explain the code — just output the files

Output format — for each file:
// filename: <relative-path>
<complete file content>

Separate files with a blank line.`

func buildOrchestratorPrompt(specs, platformBP, platform string) string {
	return fmt.Sprintf(`Generate a complete Weblisk orchestrator implementation.

## Platform
%s

## Specification
%s

## Platform-Specific Guidance
%s

Generate all files needed for a working orchestrator. Include:
- Entry point (main.go or index.js depending on platform)
- Protocol types
- Identity/crypto (ML-DSA-65 keys, tokens, signing)
- Orchestrator server (all endpoints from the spec)
- Helper utilities
- Build configuration (go.mod or package.json)

The implementation must pass protocol verification — every endpoint
must respond exactly as specified.`, platform, specs, platformBP)
}

const patternSystemPrompt = `You are a code generation agent for the Weblisk framework.
You generate implementations of cross-cutting patterns (auth, webhooks,
real-time, etc.) that integrate into existing project code.

Rules:
- Generate files that implement the pattern specification
- Each file must start with a comment: // filename: <path>
- Use ONLY standard library (no external dependencies)
- Follow the pattern specification EXACTLY
- The code must integrate cleanly with the existing project
- Do NOT explain the code — just output the files

Output format — for each file:
// filename: <relative-path>
<complete file content>

Separate files with a blank line.`

func buildPatternPrompt(patternBP, pattern, resource string) string {
	return fmt.Sprintf(`Apply the following pattern to the specified resource.

## Pattern
%s

## Pattern Specification
%s

## Target Resource
%s

Generate all files needed to implement this pattern for the target
resource. Follow the specification exactly — implement all endpoints,
types, and behaviors described.`, pattern, patternBP, resource)
}

// Response Parsing

var reFilenameComment = regexp.MustCompile(`(?m)^//\s*filename:\s*(.+?)\s*$`)
var reCodeBlock = regexp.MustCompile("(?s)```(\\w+)?(?:\\s+(.+?))?\\n(.*?)```")

func parseGeneratedFiles(response string) []GeneratedFile {
	files := parseByFilenameComments(response)
	if len(files) > 0 {
		return files
	}
	return parseByCodeBlocks(response)
}

func parseByFilenameComments(response string) []GeneratedFile {
	matches := reFilenameComment.FindAllStringIndex(response, -1)
	if len(matches) == 0 {
		return nil
	}

	var files []GeneratedFile
	for i, loc := range matches {
		nameMatch := reFilenameComment.FindStringSubmatch(response[loc[0]:loc[1]])
		if len(nameMatch) < 2 {
			continue
		}
		path := strings.TrimSpace(nameMatch[1])

		contentStart := loc[1] + 1
		if contentStart >= len(response) {
			continue
		}
		contentEnd := len(response)
		if i+1 < len(matches) {
			contentEnd = matches[i+1][0]
		}

		content := strings.TrimRight(response[contentStart:contentEnd], "\n ")
		lang := inferLang(path)
		files = append(files, GeneratedFile{Path: path, Content: content, Lang: lang})
	}
	return files
}

func parseByCodeBlocks(response string) []GeneratedFile {
	blockMatches := reCodeBlock.FindAllStringSubmatch(response, -1)
	var files []GeneratedFile
	for _, m := range blockMatches {
		lang := m[1]
		path := strings.TrimSpace(m[2])
		content := m[3]

		if path == "" {
			continue
		}

		files = append(files, GeneratedFile{Path: path, Content: content, Lang: lang})
	}
	return files
}

func inferLang(path string) string {
	switch {
	case strings.HasSuffix(path, ".go"):
		return "go"
	case strings.HasSuffix(path, ".js"):
		return "javascript"
	case strings.HasSuffix(path, ".ts"):
		return "typescript"
	case strings.HasSuffix(path, ".rs"):
		return "rust"
	case strings.HasSuffix(path, ".py"):
		return "python"
	case strings.HasSuffix(path, ".toml"):
		return "toml"
	case strings.HasSuffix(path, ".json"):
		return "json"
	case strings.HasSuffix(path, ".mod"):
		return "go"
	default:
		return ""
	}
}

// File Writing

func writeGeneratedFiles(targetDir string, files []GeneratedFile) (int, error) {
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return 0, fmt.Errorf("creating target directory: %w", err)
	}

	absTarget, err := filepath.Abs(targetDir)
	if err != nil {
		return 0, err
	}

	// Validate EVERY path before writing ANY file. A generation that tried to
	// escape is not a generation to half-apply — leaving some files written and
	// some refused would present as a partially-generated hub nobody can reason
	// about.
	fullPaths := make([]string, len(files))
	for i, f := range files {
		full, perr := safeGeneratedPath(absTarget, f.Path)
		if perr != nil {
			return 0, perr
		}
		fullPaths[i] = full
	}

	written := 0
	for i, f := range files {
		fullPath := fullPaths[i]
		if serr := withinAfterSymlinks(absTarget, fullPath); serr != nil {
			return written, serr
		}

		if dir := filepath.Dir(fullPath); dir != absTarget {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return written, fmt.Errorf("creating directory for %s: %w", f.Path, err)
			}
		}

		if err := os.WriteFile(fullPath, []byte(f.Content+"\n"), 0644); err != nil {
			return written, fmt.Errorf("writing %s: %w", f.Path, err)
		}
		written++
	}
	return written, nil
}

// printProgress renders generation progress on a terminal.
//
// A retry names the reason. "Retrying main.go" tells somebody nothing; "retrying
// main.go — the response began with prose" tells them whether to change model.
func printProgress(p Progress) {
	// Structure first, then prose. A console reads the structure and a person
	// reads the sentence; emitting only the sentence is what left Studio with a
	// spinner and a log it could not interrogate.
	observeProgress(p)
	switch p.Status {
	case "generating":
		fmt.Printf("  [%d/%d] %s\n", p.Step, p.Total, p.Path)
	case "retrying":
		fmt.Printf("  [%d/%d] %s — retry %d: %s\n", p.Step, p.Total, p.Path, p.Attempt, p.Detail)
	case "written":
		fmt.Printf("  [%d/%d] %s [ok]\n", p.Step, p.Total, p.Path)
	case "failed":
		if p.Step == 0 {
			fmt.Printf("  %s [failed] %s\n", p.Path, p.Detail)
			return
		}
		fmt.Printf("  [%d/%d] %s [failed] %s\n", p.Step, p.Total, p.Path, p.Detail)
	case "planning":
		fmt.Println("  Asking the model to plan the implementation...")
	case "replanning":
		fmt.Printf("  Re-planning — %s\n", p.Detail)
	case "preparing":
		fmt.Println("  Resolving dependencies...")
	case "building":
		fmt.Printf("  Building (round %d)...\n", p.Attempt)
	case "progress":
		fmt.Printf("    %s\n", p.Detail)
	case "built":
		fmt.Println("  Build succeeded")
	case "reused":
		fmt.Printf("  [%d/%d] %s [reused]\n", p.Step, p.Total, p.Path)
	case "summary":
		fmt.Printf("\n  %s\n", p.Detail)
	case "repairing":
		fmt.Printf("    repairing %s — %s\n", p.Path, p.Detail)
	}
}

// reportChecklist prints the structural checks — as ADVICE, not as a verdict.
//
// These no longer drive the loop. The blueprints' assertions are sent to the
// model verbatim and it judges its own output against them, because a check
// written in Go is a transcription of a requirement into a second language and
// ten of them were subtly wrong in one session.
//
// They are still printed, for the one thing they are unambiguously good for:
// disagreeing. A structural check that refutes an assertion the model reported as
// satisfied is worth a human's attention, and resolving that disagreement
// silently in favour of either side is how a build comes to be trusted for the
// wrong reason.
func reportChecklist(results []ChecklistResult) {
	if len(results) == 0 {
		return
	}
	verified, failed, necessary, notApplicable, unchecked := ChecklistCounts(results)
	inconclusive := ChecklistInconclusive(results)
	fmt.Printf("  Structural checks (advisory): %d verified, %d refuted, %d inconclusive, %d necessary-conditions-hold, %d not-applicable, %d no check\n",
		verified, failed, inconclusive, necessary, notApplicable, unchecked)
	for _, r := range results {
		if r.Outcome == OutcomeFailed {
			fmt.Printf("    [refuted] %s\n              %s\n", r.Item.Text, r.Detail)
		}
	}
	// Listed apart, and AFTER the refutations, because they are not findings
	// about the component — they are gaps in this tool. One run printed 25 of
	// them as "checks disagree with the specification" when every one meant
	// "the route table is a loop over constants I cannot resolve".
	for _, r := range results {
		if r.Outcome == OutcomeInconclusive {
			fmt.Printf("    [inconclusive] %s\n                   %s\n", r.Item.Text, r.Detail)
		}
	}
	if failed > 0 {
		fmt.Printf("    %d structural check(s) disagree with the specification as read by the model.\n"+
			"    Neither is authoritative here — read both.\n", failed)
	}
	if inconclusive > 0 {
		fmt.Printf("    %d could not be settled either way — this tool could not read the source,\n"+
			"    which says nothing about whether the component is correct.\n", inconclusive)
	}
	fmt.Println()
}

// reportVerdicts prints what the model found against the assertions.
func reportVerdicts(verdicts []Verdict, checklist []ChecklistItem) {
	if len(verdicts) == 0 {
		return
	}
	yes, no, unverifiable, unanswered := VerdictSummary(verdicts, len(checklist))
	fmt.Printf("  Verification against the blueprints' assertions: %d satisfied, %d unmet, %d unverifiable by reading source, %d unanswered\n",
		yes, no, unverifiable, unanswered)
	for _, v := range Violations(verdicts, checklist) {
		fmt.Printf("    [unmet] %s\n            %s", v.Item.Text, v.Evidence)
		if v.File != "" {
			fmt.Printf(" (%s)", v.File)
		}
		fmt.Println()
	}
	if unverifiable > 0 {
		fmt.Printf("    %d assertions cannot be settled by reading source — behaviour over time,\n"+
			"    across a restart, or under load. They need the conformance suite.\n", unverifiable)
	}
	if unanswered > 0 {
		fmt.Printf("    [warn] %d assertions were not judged at all — the verification is incomplete\n", unanswered)
	}
	fmt.Println()
}

// indentBlock indents every line, so build output is visibly subordinate to the
// message that introduced it.
func indentBlock(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

// moduleNameFor is the import-path prefix for a tenant's own packages.
//
// The tenant directory's name, per platforms/go: "module <tenant>". A name Go
// will not accept as a module path is replaced rather than passed through, since
// an unusable module path fails at `go mod tidy` with an error about a file
// nobody wrote.
func moduleNameFor(root string) string {
	name := filepath.Base(filepath.Clean(root))
	if name == "." || name == string(filepath.Separator) || name == "" {
		if wd, err := os.Getwd(); err == nil {
			name = filepath.Base(wd)
		}
	}
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-_")
	if out == "" {
		return "tenant"
	}
	return out
}

// builtBinary reads the output path out of the plan's build command.
//
// `go build -o bin/orchestrator ./cmd/orchestrator` — the -o argument. Taken
// from the command rather than assumed, because the command is the platform
// blueprint's and this should not hold a second opinion about where the binary
// lands.
// builtBinary is the executable a build command produced, or "" if none can be
// found.
//
// The model's own -o first, because that is what the build actually wrote.
// Then bin/<name>, which is where platforms/go.md says binaries go and where
// StartComponent builds to — a build command with no -o at all (`go build
// ./...`) used to return "" here, and the whole conformance and interop layer
// was then skipped in silence. A suite that does not run reads exactly like a
// suite that found nothing.
func builtBinary(root, buildCmd string, c Component) string {
	fields := strings.Fields(buildCmd)
	for i, f := range fields {
		if f == "-o" && i+1 < len(fields) {
			p := filepath.Join(root, fields[i+1])
			if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
				return p
			}
		}
	}
	return FindBinary(root, c)
}

// reportConformance prints what running the component established, and returns
// what it means for the run's outcome.
//
// # Why it returns something now
//
// It printed and returned nothing, and ComponentInit ended `return nil`. So a
// build whose component PANICKED AT STARTUP exited 0. The pipeline detected the
// fault, printed the panic, tried two rounds of repair, said "the component
// does not run" — and then reported success.
//
// That is worse than not checking. Anything reading the exit status — a script,
// CI, Studio, an operator — is told a hub was built, and the hub does not
// start. A check whose result is discarded is indistinguishable from no check
// at all, except that it costs time and looks like diligence.
//
// A test with no harness is NOT a failure and does not count here: "we have not
// verified this" and "this is wrong" are different facts, and conflating them
// would make the unrun count a reason to fail a correct build.
// reportConformance prints one layer's results and returns the fault, if any.
//
// layer names which one. It was hardcoded "L1" and reused for the interop
// results too, so a run printed two sections both headed "Conformance L1" —
// one of them the layer its own caller calls "Layer 4's final word". A report
// that misnames what it ran is a report you cannot act on.
func reportConformance(layer string, results []ConformanceResult, output string, err error) error {
	if err != nil {
		// It never answered. Its own output is the finding.
		fmt.Printf("  [failed] the component does not run\n           %v\n\n", err)
		if t := strings.TrimSpace(output); t != "" {
			fmt.Println(indentBlock(lastLines(t, 12), "    "))
			fmt.Println()
		}
		return fmt.Errorf("the component does not run: %w", err)
	}
	passed, failed, unrun := ConformanceSummary(results)
	fmt.Printf("  %s: %d passed, %d failed, %d unrun\n", layer, passed, failed, unrun)
	for _, r := range results {
		switch {
		case r.Unrun:
			fmt.Printf("    [unrun] %s %s — %s\n", r.ID, r.Name, r.Detail)
		case r.Passed:
			fmt.Printf("    [pass]  %s %s — %s\n", r.ID, r.Name, r.Evidence)
		default:
			fmt.Printf("    [FAIL]  %s %s\n            %s\n", r.ID, r.Name, r.Detail)
		}
	}
	if unrun > 0 {
		fmt.Printf("    %d test(s) have no harness yet — they are NOT passes\n", unrun)
	}
	fmt.Println()
	if failed > 0 {
		return fmt.Errorf("%s", plural(failed, "conformance test")+" failed")
	}
	return nil
}

// lastLines returns the tail of some output, which is where a startup failure
// says what went wrong.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// provenanceOf records what this run is generating from.
func provenanceOf(graph *BlueprintGraph, provider Provider) *Provenance {
	p := &Provenance{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	}
	// The provider this run SETTLED on, not the environment variable — which is
	// empty on the default path, because walking the catalog is how a provider
	// is chosen when nobody named one. So every hub generated without an
	// explicit WL_AI_PROVIDER recorded no provider at all, in the file whose
	// entire job is to say what generated it.
	if kind, _, pinned := SelectedProvider(); pinned {
		p.Provider = string(kind)
	} else {
		p.Provider = os.Getenv("WL_AI_PROVIDER")
	}
	if m, ok := Underlying(provider).(interface{ ModelUsed() string }); ok {
		p.Model = m.ModelUsed()
	}
	if p.Model == "" {
		// The configured value is a second-best answer and is labelled as one
		// by being the only one present: an empty Model means the provider did
		// not say, which is different from nobody having asked.
		p.Model = os.Getenv("WL_AI_MODEL")
	}
	for _, s := range graph.Sources {
		used := 0
		for _, served := range graph.ServedBy {
			if served.Dir == s.Dir {
				used++
			}
		}
		p.Sources = append(p.Sources, ProvenanceSource{
			Kind: s.Kind, Dir: s.Dir, Revision: s.Revision, Used: used,
		})
	}
	return p
}
