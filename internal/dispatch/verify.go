package dispatch

// Evaluating verification checklists against structure, not substrings.
//
// # What the substring table could and could not do
//
// The first evaluator held ten checks, nearly all of the form "does this string
// appear anywhere in the generated source". Against 128 assertions that verified
// nine, and it verified them weakly: contains("Retry-After") passes on a comment
// mentioning Retry-After.
//
// Read the 128 and they sort into three kinds:
//
//  1. STRUCTURAL — "ErrorResponse includes `error`, `code`, `category`,
//     `retryable`, `detail` with exact JSON keys". A parser answers this exactly.
//     Roughly forty assertions are of this shape: type fields, enum members,
//     routed paths, declared dependencies, forbidden imports.
//
//  2. BEHAVIOURAL — "all stores survive process restart", "retries with
//     exponential backoff", "concurrent reads/writes do not corrupt data". No
//     reading of the source settles these. They need the conformance suite
//     architecture/testing.md specifies, which does not exist yet.
//
//  3. COMPOUND — "POST /v1/register enforces exclusive namespace ownership (409
//     on conflict)". Structure answers PART of it: if no handler is routed at
//     POST /v1/register the assertion is definitely unmet. Route presence does
//     not establish the 409.
//
// # The asymmetry the third kind requires
//
// Reporting a compound assertion as PASSED because its route exists is the
// failure this file was written to avoid — a confident wrong answer, which is
// worse than an honest gap. Reporting it as unchecked throws away a real signal:
// a missing endpoint is a definite fault, cheaply detected.
//
// So a check may be one-way. Failure is conclusive; success establishes only
// that the necessary condition holds, and is reported as exactly that. Four
// outcomes, never three:
//
//	verified   — the check settles the assertion, and it holds
//	failed     — the check settles it, or a necessary condition is unmet
//	necessary  — a necessary condition holds; the assertion is not established
//	unchecked  — nothing mechanical applies
//
// Only `failed` drives repair. `necessary` is never counted as `verified`.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"sort"
	"strings"
)

// CheckContext is the structure a checklist assertion is evaluated against.
type CheckContext struct {
	Files   []GeneratedFile
	Source  string              // every file concatenated, for text checks
	Fields  map[string][]Field  // struct type name → its fields
	Values  map[string][]string // named string-constant type → its values
	Routes  map[string]string   // "POST /v1/register" → file registering it
	Paths   []string            // every path literal routed
	Imports map[string]string   // import path → first file importing it
	Modules []string            // module paths required by go.mod
	OwnerOf map[string]string   // declared symbol → file declaring it
}

// Field is one struct field and what it serialises as.
type Field struct {
	Name     string
	JSONKey  string
	Optional bool // omitempty
}

// BuildCheckContext parses the generated files once.
func BuildCheckContext(files []GeneratedFile) *CheckContext {
	ctx := &CheckContext{
		Files:   files,
		Fields:  map[string][]Field{},
		Values:  map[string][]string{},
		Routes:  map[string]string{},
		Imports: map[string]string{},
		OwnerOf: map[string]string{},
	}
	var all strings.Builder
	for _, f := range files {
		all.WriteString(f.Content)
		all.WriteString("\n")
		if strings.HasSuffix(f.Path, "go.mod") {
			ctx.Modules = append(ctx.Modules, goModRequires(f.Content)...)
			continue
		}
		if !strings.HasSuffix(f.Path, ".go") {
			continue
		}
		ctx.absorbGo(f)
	}
	ctx.Source = all.String()
	sort.Strings(ctx.Paths)
	return ctx
}

// absorbGo records everything one Go file contributes to the context.
//
// A file that does not parse contributes nothing rather than contributing
// guesses: an unparsed file is a compile error the build will report, and
// inventing structure from broken source would make the checklist disagree with
// the compiler.
func (c *CheckContext) absorbGo(f GeneratedFile) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, f.Path, f.Content, parser.SkipObjectResolution)
	if err != nil {
		return
	}
	for _, imp := range file.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if _, seen := c.Imports[path]; !seen {
			c.Imports[path] = f.Path
		}
	}
	for _, d := range file.Decls {
		switch decl := d.(type) {
		case *ast.GenDecl:
			c.absorbGenDecl(decl, f.Path)
		case *ast.FuncDecl:
			if decl.Name != nil {
				c.OwnerOf[decl.Name.Name] = f.Path
			}
		}
	}
	// Routed paths, from the AST rather than a regex over text, so a path in a
	// comment or an error message is not mistaken for a route.
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "HandleFunc", "Handle":
			if len(call.Args) == 0 {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			c.recordRoute(strings.Trim(lit.Value, `"`), f.Path)
		}
		return true
	})
}

// recordRoute normalises a mux pattern into method+path.
//
// Go 1.22 patterns carry the method — "POST /v1/register" — and older muxes
// carry only the path, with the method switched on inside the handler. Both are
// recorded: the path under every method the source associates with it, and the
// bare path always, so a path-only registration still satisfies a path check.
func (c *CheckContext) recordRoute(pattern, file string) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return
	}
	fields := strings.Fields(pattern)
	path := fields[len(fields)-1]
	if i := strings.Index(path, "/"); i > 0 {
		// A host-qualified pattern: "example.com/v1/x".
		path = path[i:]
	}
	if !containsString(c.Paths, path) {
		c.Paths = append(c.Paths, path)
	}
	if len(fields) > 1 {
		c.Routes[strings.ToUpper(fields[0])+" "+path] = file
	} else {
		// Method unknown at the mux; the handler decides. Record the path alone.
		c.Routes[path] = file
	}
}

func containsString(in []string, want string) bool {
	for _, s := range in {
		if s == want {
			return true
		}
	}
	return false
}

func (c *CheckContext) absorbGenDecl(decl *ast.GenDecl, file string) {
	for _, spec := range decl.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			if s.Name == nil {
				continue
			}
			c.OwnerOf[s.Name.Name] = file
			if st, ok := s.Type.(*ast.StructType); ok {
				c.Fields[s.Name.Name] = structFields(st)
			}
		case *ast.ValueSpec:
			// A named string constant contributes its VALUE to its type's set —
			// which is what an enum assertion is about. `ScopePublic
			// ScopeLevel = "public"` means ScopeLevel admits "public".
			typeName := ""
			if id, ok := s.Type.(*ast.Ident); ok {
				typeName = id.Name
			}
			for i, name := range s.Names {
				if name != nil {
					c.OwnerOf[name.Name] = file
				}
				if typeName == "" || i >= len(s.Values) {
					continue
				}
				if lit, ok := s.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					c.Values[typeName] = append(c.Values[typeName], strings.Trim(lit.Value, `"`))
				}
			}
		}
	}
}

func structFields(st *ast.StructType) []Field {
	var out []Field
	for _, f := range st.Fields.List {
		key, optional := jsonTag(f.Tag)
		if len(f.Names) == 0 {
			// An embedded field contributes its type's name.
			out = append(out, Field{Name: exprString(f.Type), JSONKey: key, Optional: optional})
			continue
		}
		for _, n := range f.Names {
			k := key
			if k == "" {
				k = n.Name
			}
			out = append(out, Field{Name: n.Name, JSONKey: k, Optional: optional})
		}
	}
	return out
}

var reJSONTag = regexp.MustCompile(`json:"([^"]*)"`)

func jsonTag(tag *ast.BasicLit) (key string, optional bool) {
	if tag == nil {
		return "", false
	}
	m := reJSONTag.FindStringSubmatch(tag.Value)
	if m == nil {
		return "", false
	}
	parts := strings.Split(m[1], ",")
	key = parts[0]
	for _, p := range parts[1:] {
		if p == "omitempty" {
			optional = true
		}
	}
	return key, optional
}

var reGoModRequire = regexp.MustCompile(`(?m)^\s*(?:require\s+)?([a-z0-9][\w.\-]*\.[a-z]{2,}(?:/[\w.\-~]+)+)\s+v\S+`)

// goModRequires lists module paths a go.mod requires.
func goModRequires(content string) []string {
	var out []string
	for _, m := range reGoModRequire.FindAllStringSubmatch(content, -1) {
		if !containsString(out, m[1]) {
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out
}

// Outcome is how conclusively an assertion was evaluated.
type Outcome string

const (
	// OutcomeVerified — a check settles the assertion and it holds.
	OutcomeVerified Outcome = "verified"
	// OutcomeFailed — a check settles it against the source, or a necessary
	// condition is unmet. The only outcome that drives repair.
	OutcomeFailed Outcome = "failed"
	// OutcomeNecessary — a necessary condition holds; the assertion itself is
	// NOT established. Never counted as verified.
	OutcomeNecessary Outcome = "necessary"
	// OutcomeUnchecked — nothing mechanical applies.
	OutcomeUnchecked Outcome = "unchecked"
)

// structuralCheck evaluates an assertion against parsed structure.
//
// oneWay marks a check that can only refute: it tests a necessary condition, so
// holding proves nothing and failing proves a fault.
type structuralCheck struct {
	name   string
	oneWay bool
	// applies reports whether this check has anything to say about an assertion.
	applies func(a assertion) bool
	// test returns whether the condition holds, detail when it does not, and the
	// generated files the failure is about.
	//
	// Blame is returned by the check rather than derived afterwards because the
	// check is the only thing that knows. A separate attribution pass would be a
	// second answer to the same question, free to drift from the first.
	test func(a assertion, c *CheckContext) (bool, string, []string)
}

// assertion is one checklist item with the pieces a check reads.
type assertion struct {
	Text   string
	Lower  string
	Ticked []string // backticked spans, verbatim
	Keys   []string // ticked spans that are plausible JSON keys or identifiers
	Types  []string // known type names named in the text
	Routes []string // "POST /v1/register" style references in the text
}

var (
	reTicked     = regexp.MustCompile("`([^`]+)`")
	reJSONKeyish = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	reRouteRef   = regexp.MustCompile(`\b(GET|POST|PUT|PATCH|DELETE)\s+` + "`?" + `(/v\d[\w/{}.\-]*)`)
	rePathRef    = regexp.MustCompile("`?(/v\\d/[\\w/{}.\\-]+)`?")
)

// parseAssertion pulls the mechanically usable pieces out of one item.
func parseAssertion(item ChecklistItem, c *CheckContext) assertion {
	a := assertion{Text: item.Text, Lower: strings.ToLower(item.Text)}
	for _, m := range reTicked.FindAllStringSubmatch(item.Text, -1) {
		a.Ticked = append(a.Ticked, m[1])
		if reJSONKeyish.MatchString(m[1]) {
			a.Keys = append(a.Keys, m[1])
		}
	}
	// Type names: only names the generated source actually declares as structs,
	// so a word that merely looks like a type cannot invent a check.
	for name := range c.Fields {
		if regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`).MatchString(item.Text) {
			a.Types = append(a.Types, name)
		}
	}
	sort.Strings(a.Types)
	for _, m := range reRouteRef.FindAllStringSubmatch(item.Text, -1) {
		a.Routes = append(a.Routes, strings.ToUpper(m[1])+" "+m[2])
	}
	return a
}

var structuralChecks = []structuralCheck{
	{
		// "ErrorResponse includes `error` (required), `code`, `category`,
		// `retryable`, and `detail` fields with exact JSON keys"
		//
		// One-way: the fields being present does not establish the surrounding
		// claims — required-ness, forward compatibility, what a signature covers.
		// Their ABSENCE settles it, and that is the common generation fault.
		name:   "declared JSON keys exist on the named type",
		oneWay: true,
		applies: func(a assertion) bool {
			return len(a.Types) == 1 && len(a.Keys) > 0
		},
		test: func(a assertion, c *CheckContext) (bool, string, []string) {
			typeName := a.Types[0]
			have := map[string]bool{}
			for _, f := range c.Fields[typeName] {
				have[f.JSONKey] = true
				have[strings.ToLower(f.Name)] = true
			}
			var missing []string
			for _, k := range a.Keys {
				if !have[k] {
					missing = append(missing, k)
				}
			}
			if len(missing) == 0 {
				return true, "", nil
			}
			return false, typeName + " is missing JSON key(s): " + strings.Join(missing, ", "),
				blameOwners(c, typeName)
		},
	},
	{
		// "ScopeLevel enum is constrained to `public`, `internal`, ..."
		//
		// One-way in the other direction from the field check: the values being
		// declared does not establish that anything REJECTS values outside the
		// set, which is what "constrained" claims.
		name:   "enum values are declared",
		oneWay: true,
		applies: func(a assertion) bool {
			return strings.Contains(a.Lower, "enum") && strings.Contains(a.Lower, "constrained") && len(a.Keys) > 1
		},
		test: func(a assertion, c *CheckContext) (bool, string, []string) {
			// Enum values may be declared against any type; search all values.
			have := map[string]bool{}
			for _, vals := range c.Values {
				for _, v := range vals {
					have[v] = true
				}
			}
			var missing []string
			for _, k := range a.Keys {
				if !have[k] {
					missing = append(missing, k)
				}
			}
			if len(missing) == 0 {
				return true, "", nil
			}
			return false, "no constant declares value(s): " + strings.Join(missing, ", "), nil
		},
	},
	{
		// "POST /v1/register enforces exclusive namespace ownership (409 on
		// conflict)" — the route must exist. What it enforces is behaviour.
		name:    "referenced endpoint is routed",
		oneWay:  true,
		applies: func(a assertion) bool { return len(a.Routes) > 0 },
		test: func(a assertion, c *CheckContext) (bool, string, []string) {
			var missing []string
			for _, r := range a.Routes {
				path := r[strings.Index(r, " ")+1:]
				if _, ok := c.Routes[r]; ok {
					continue
				}
				// A mux that registers the path without the method is legitimate;
				// the handler switches internally.
				if _, ok := c.Routes[path]; ok {
					continue
				}
				missing = append(missing, r)
			}
			if len(missing) == 0 {
				return true, "", nil
			}
			// Nothing owns a route that does not exist, so the blame is the file
			// the plan put the HTTP surface in — found by where the other routes
			// live rather than guessed at.
			return false, "no handler is registered for: " + strings.Join(missing, ", "), routeHosts(c)
		},
	},
	{
		// "No dependency beyond `github.com/cloudflare/circl`, plus a storage
		// driver only if a backend other than the JSONL default was chosen; every
		// dependency declared in go.mod"
		//
		// Settled, not one-way: go.mod is the complete statement of what a Go
		// module depends on, and every import can be checked against it.
		name: "dependency policy",
		applies: func(a assertion) bool {
			return strings.Contains(a.Lower, "no dependency beyond")
		},
		test: func(a assertion, c *CheckContext) (bool, string, []string) {
			allowed := map[string]bool{}
			for _, t := range a.Ticked {
				if strings.Contains(t, "/") {
					allowed[t] = true
				}
			}
			var extra []string
			for _, m := range c.Modules {
				permitted := false
				for prefix := range allowed {
					if m == prefix || strings.HasPrefix(m, prefix+"/") {
						permitted = true
						break
					}
				}
				if !permitted {
					extra = append(extra, m)
				}
			}
			// And every non-stdlib import must be declared.
			var undeclared []string
			for path := range c.Imports {
				if !strings.Contains(strings.SplitN(path, "/", 2)[0], ".") {
					continue // standard library
				}
				declared := false
				for _, m := range c.Modules {
					if path == m || strings.HasPrefix(path, m+"/") {
						declared = true
						break
					}
				}
				if !declared {
					undeclared = append(undeclared, path)
				}
			}
			sort.Strings(extra)
			sort.Strings(undeclared)
			switch {
			case len(extra) > 0 && len(undeclared) > 0:
				return false,
					"undeclared dependency policy breach: extra " + strings.Join(extra, ", ") +
						"; imported but not in go.mod " + strings.Join(undeclared, ", "),
					append(goModFiles(c), blameImporters(c, undeclared)...)
			case len(extra) > 0:
				// An unpermitted module is declared in go.mod AND imported
				// somewhere; both have to change together or the build breaks.
				return false, "dependency not permitted by the policy: " + strings.Join(extra, ", "),
					append(goModFiles(c), blameImporters(c, extra)...)
			case len(undeclared) > 0:
				return false, "imported but not declared in go.mod: " + strings.Join(undeclared, ", "),
					goModFiles(c)
			}
			return true, "", nil
		},
	},
	{
		// "No quantum-vulnerable algorithms (Ed25519, ECDSA, RSA) are used
		// anywhere" — an absence claim over imports, which a parser settles.
		name: "forbidden algorithms are absent",
		applies: func(a assertion) bool {
			return strings.Contains(a.Lower, "quantum-vulnerable") ||
				strings.Contains(a.Lower, "no other signing algorithm")
		},
		test: func(a assertion, c *CheckContext) (bool, string, []string) {
			forbidden := []string{"crypto/ed25519", "crypto/ecdsa", "crypto/rsa", "crypto/dsa", "crypto/elliptic"}
			var found []string
			for _, f := range forbidden {
				if file, ok := c.Imports[f]; ok {
					found = append(found, f+" (in "+file+")")
				}
			}
			if len(found) == 0 {
				return true, "", nil
			}
			return false, "quantum-vulnerable algorithm imported: " + strings.Join(found, ", "), blameImporters(c, forbidden)
		},
	},
	{
		// "Protocol paths are all prefixed with `/v1`" — every routed path, which
		// the AST gives exactly.
		name: "every routed path is version-prefixed",
		applies: func(a assertion) bool {
			return strings.Contains(a.Lower, "prefixed with") && strings.Contains(a.Text, "/v1")
		},
		test: func(a assertion, c *CheckContext) (bool, string, []string) {
			var bad []string
			for _, p := range c.Paths {
				if p == "/" || strings.HasPrefix(p, "/v1") {
					continue
				}
				bad = append(bad, p)
			}
			if len(bad) == 0 {
				return true, "", nil
			}
			return false, "routed without a /v1 prefix: " + strings.Join(bad, ", "), nil
		},
	},
	{
		name: "all source files declare package main",
		applies: func(a assertion) bool {
			return strings.Contains(a.Lower, "package main")
		},
		test: func(a assertion, c *CheckContext) (bool, string, []string) {
			var bad []string
			for _, f := range c.Files {
				if !strings.HasSuffix(f.Path, ".go") {
					continue
				}
				if !regexp.MustCompile(`(?m)^package\s+main\s*$`).MatchString(f.Content) {
					bad = append(bad, f.Path)
				}
			}
			if len(bad) == 0 {
				return true, "", nil
			}
			return false, "not in package main: " + strings.Join(bad, ", "), bad
		},
	},
	{
		name: "handlers do not panic",
		applies: func(a assertion) bool {
			return strings.Contains(a.Lower, "do not panic")
		},
		test: func(a assertion, c *CheckContext) (bool, string, []string) {
			var found []string
			for _, f := range c.Files {
				if !strings.HasSuffix(f.Path, ".go") {
					continue
				}
				fset := token.NewFileSet()
				file, err := parser.ParseFile(fset, f.Path, f.Content, parser.SkipObjectResolution)
				if err != nil {
					continue
				}
				ast.Inspect(file, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "panic" {
						found = append(found, f.Path)
						return false
					}
					return true
				})
			}
			if len(found) == 0 {
				return true, "", nil
			}
			return false, "panic() called in: " + strings.Join(found, ", "), found
		},
	},
	{
		// Size assertions state exact byte counts; their literals must appear.
		// One-way: the constant existing does not prove it is enforced.
		name:   "declared sizes appear as literals",
		oneWay: true,
		applies: func(a assertion) bool {
			return regexp.MustCompile(`\b(1952|3309)\b`).MatchString(a.Text)
		},
		test: func(a assertion, c *CheckContext) (bool, string, []string) {
			var missing []string
			for _, n := range regexp.MustCompile(`\b(1952|3309)\b`).FindAllString(a.Text, -1) {
				if !strings.Contains(c.Source, n) {
					missing = append(missing, n)
				}
			}
			if len(missing) == 0 {
				return true, "", nil
			}
			return false, "size(s) never appear in the source: " + strings.Join(missing, ", "), nil
		},
	},
}

// blameOwners names the files declaring given symbols.
func blameOwners(c *CheckContext, symbols ...string) []string {
	var out []string
	for _, s := range symbols {
		if f := c.OwnerOf[s]; f != "" && !containsString(out, f) {
			out = append(out, f)
		}
	}
	return out
}

// blameImporters names the files importing any of the given module paths.
func blameImporters(c *CheckContext, paths []string) []string {
	var out []string
	for path, file := range c.Imports {
		for _, p := range paths {
			if path == p || strings.HasPrefix(path, p+"/") {
				if !containsString(out, file) {
					out = append(out, file)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// routeHosts names the files that register routes — where a missing endpoint
// would have to be added.
func routeHosts(c *CheckContext) []string {
	var out []string
	for _, file := range c.Routes {
		if file != "" && !containsString(out, file) {
			out = append(out, file)
		}
	}
	sort.Strings(out)
	return out
}

// goModFiles names the module file, when one was generated.
func goModFiles(c *CheckContext) []string {
	var out []string
	for _, f := range c.Files {
		if strings.HasSuffix(f.Path, "go.mod") {
			out = append(out, f.Path)
		}
	}
	return out
}
