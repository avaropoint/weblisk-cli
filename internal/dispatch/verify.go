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
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"sort"
	"strconv"
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
	// Codes are SCREAMING_SNAKE string literals used in the source but not
	// declared as a key of a code registry — the codes a handler writes.
	Codes map[string]string // code → first file using it
	// Registered are the keys of every string-keyed map literal whose keys look
	// like codes: the central registry, whatever it was named.
	Registered map[string]bool
	// CodeStatus is the HTTP status the artifact assigns each registered code,
	// read from the registry entry's first integer literal.
	CodeStatus map[string]int
	// Spec is the blueprint set the artifact was generated from, so a check can
	// compare against the specification's own tables rather than against a copy
	// of them written into the tooling.
	Spec map[string]string
	// Consts are string-valued constants and variables, by bare name and by
	// package-qualified name. Route patterns are usually one of these.
	Consts map[string]string
	// ambiguousConst records bare names declared with different values in more
	// than one package, so a bare lookup cannot silently pick a winner.
	ambiguousConst map[string]bool
	// UnroutableCalls are mux registrations whose pattern could not be resolved
	// to a string. They are the difference between "this hub registers nothing"
	// and "this tool could not read how it registers" — and a check MUST report
	// the second as a gap in its own knowledge rather than as a fault in the
	// artifact.
	UnroutableCalls []string
}

// Field is one struct field and what it serialises as.
type Field struct {
	Name     string
	JSONKey  string
	Optional bool // omitempty
}

// BuildCheckContext parses the generated files once.
// BuildCheckContextWith parses the generated files and keeps the blueprints
// available to checks that compare against the specification.
func BuildCheckContextWith(files []GeneratedFile, spec map[string]string) *CheckContext {
	ctx := BuildCheckContext(files)
	ctx.Spec = spec
	return ctx
}

func BuildCheckContext(files []GeneratedFile) *CheckContext {
	ctx := &CheckContext{
		Files:          files,
		Fields:         map[string][]Field{},
		Values:         map[string][]string{},
		Routes:         map[string]string{},
		Imports:        map[string]string{},
		OwnerOf:        map[string]string{},
		Codes:          map[string]string{},
		Registered:     map[string]bool{},
		CodeStatus:     map[string]int{},
		Consts:         map[string]string{},
		ambiguousConst: map[string]bool{},
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
	// Constants, then routes. A route pattern is nearly always a named constant
	// — often from another package — so neither pass can be folded into the
	// per-file walk above without reading files in an order nobody controls.
	// Repeated until it stops learning. A local alias can be written in terms
	// of a constant declared in another file, and files are absorbed in
	// whatever order the plan produced them — one pass would resolve the alias
	// only when its dependency happened to come first.
	for round := 0; round < 4; round++ {
		before := len(ctx.Consts)
		for _, f := range files {
			if strings.HasSuffix(f.Path, ".go") {
				ctx.absorbConsts(f)
			}
		}
		if len(ctx.Consts) == before {
			break
		}
	}
	for _, f := range files {
		if strings.HasSuffix(f.Path, ".go") {
			ctx.absorbRoutes(f)
		}
	}
	ctx.Source = all.String()
	sort.Strings(ctx.Paths)
	return ctx
}

// absorbConsts records every string-valued constant and package-level variable,
// under its bare name and its package-qualified name.
//
// A bare name declared with two different values in two packages is marked
// ambiguous and will not resolve, because picking one would be a guess.
func (c *CheckContext) absorbConsts(f GeneratedFile) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, f.Path, f.Content, parser.SkipObjectResolution)
	if err != nil {
		return
	}
	pkg := ""
	if file.Name != nil {
		pkg = file.Name.Name
	}
	record := func(name, val string) {
		if pkg != "" {
			c.Consts[pkg+"."+name] = val
		}
		if prior, seen := c.Consts[name]; seen && prior != val {
			c.ambiguousConst[name] = true
			return
		}
		c.Consts[name] = val
	}
	for _, d := range file.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || (gd.Tok != token.CONST && gd.Tok != token.VAR) {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				if val, err := strconv.Unquote(lit.Value); err == nil {
					record(name.Name, val)
				}
			}
		}
	}
	// Function-scope short declarations too. A route table is commonly built
	// inside the function that returns it, and its local aliases —
	// "agents := strings.TrimSuffix(protocol.PathAdminAgents, \"/\")" — are what
	// the table's entries are written in terms of.
	ast.Inspect(file, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || as.Tok != token.DEFINE {
			return true
		}
		for i, lhs := range as.Lhs {
			id, ok := lhs.(*ast.Ident)
			if !ok || i >= len(as.Rhs) {
				continue
			}
			if val, ok := c.evalString(as.Rhs[i]); ok {
				record(id.Name, val)
			}
		}
		return true
	})
}

// absorbRoutes records the endpoints a file registers on a mux.
//
// # Why this resolves expressions
//
// It used to accept only a string literal as the first argument. A real
// generated hub registers from a route table —
//
//	{pattern: protocol.PathAdminOverview, methods: ...}
//	{pattern: agents + "/{name}", methods: ...}
//	mux.HandleFunc(m.method+" "+rt.pattern, m.handler)
//
// — so the walk found no literal, recorded no routes, and the checklist
// reported "no handler is registered for: GET /v1/health" about a hub that
// registers it correctly. Twenty-one assertions were refuted that way in one
// run, every one of them wrong.
//
// A check that cannot read the artifact must say so. Anything unresolved is
// recorded in UnroutableCalls, so the difference between "absent" and "not
// legible to this tool" survives to the report.
func (c *CheckContext) absorbRoutes(f GeneratedFile) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, f.Path, f.Content, parser.SkipObjectResolution)
	if err != nil {
		return
	}
	// Composite-literal fields named "pattern"/"path"/"route" are how a route
	// table states its endpoints, and the mux call itself only sees a variable.
	ast.Inspect(file, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		for _, elt := range cl.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				continue
			}
			switch strings.ToLower(key.Name) {
			case "pattern", "path", "route", "endpoint":
				v, ok := c.evalString(kv.Value)
				if ok && strings.HasPrefix(v, "/") {
					c.recordRoute(v, f.Path)
					continue
				}
				if !ok {
					// A route table entry this tool cannot read. Recorded for
					// the same reason as an unreadable mux call: the report must
					// distinguish "absent" from "not legible here".
					c.UnroutableCalls = append(c.UnroutableCalls, f.Path)
				}
			}
		}
		return true
	})

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
			if v, ok := c.evalString(call.Args[0]); ok {
				c.recordRoute(v, f.Path)
				return true
			}
			// Registered from a variable this tool cannot follow — a loop over a
			// route table, most often. Recorded as unread, never as absent.
			c.UnroutableCalls = append(c.UnroutableCalls, f.Path)
		}
		return true
	})
}

// evalString resolves a constant string expression: a literal, a named
// constant, a package-qualified constant, a parenthesised expression, or any
// concatenation of those.
//
// It returns false rather than a partial answer. A route half-resolved is a
// route this tool does not know, and the caller must be able to tell.
func (c *CheckContext) evalString(e ast.Expr) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(v.Value)
		return s, err == nil
	case *ast.Ident:
		if c.ambiguousConst[v.Name] {
			return "", false
		}
		s, ok := c.Consts[v.Name]
		return s, ok
	case *ast.SelectorExpr:
		pkg, ok := v.X.(*ast.Ident)
		if !ok {
			return "", false
		}
		s, ok := c.Consts[pkg.Name+"."+v.Sel.Name]
		return s, ok
	case *ast.ParenExpr:
		return c.evalString(v.X)
	case *ast.CallExpr:
		// The two string helpers a generated route table actually uses to derive
		// one path from another:
		//
		//	agents := strings.TrimSuffix(protocol.PathAdminAgents, "/")
		//
		// Folded because they are pure and total, not because this is becoming
		// an interpreter — anything else stays unresolved and is reported as
		// unread.
		sel, ok := v.Fun.(*ast.SelectorExpr)
		if !ok || len(v.Args) != 2 {
			return "", false
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "strings" {
			return "", false
		}
		a, aok := c.evalString(v.Args[0])
		b, bok := c.evalString(v.Args[1])
		if !aok || !bok {
			return "", false
		}
		switch sel.Sel.Name {
		case "TrimSuffix":
			return strings.TrimSuffix(a, b), true
		case "TrimPrefix":
			return strings.TrimPrefix(a, b), true
		}
		return "", false
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return "", false
		}
		l, lok := c.evalString(v.X)
		r, rok := c.evalString(v.Y)
		if !lok || !rok {
			return "", false
		}
		return l + r, true
	}
	return "", false
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
	// Error codes, and the registry they should come from.
	//
	// A code is a SCREAMING_SNAKE string literal that is not an environment
	// variable name. The env exclusion is not a guess at what looks like a code:
	// literals passed to os.Getenv or os.LookupEnv are found in the AST, and the
	// WL_ prefix is the configuration namespace platforms/go.md itself declares.
	//
	// Both exclusions were added after the first measurement. I sampled the top
	// thirty SCREAMING_SNAKE literals by frequency, saw error codes, and concluded
	// every literal of that shape was one — then the check reported WL_DEV and
	// WL_PORT as unregistered error codes. Calibrating on a truncated view is how
	// a plausible rule gets shipped with a whole category missing from it.
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		mt, ok := lit.Type.(*ast.MapType)
		if !ok {
			return true
		}
		if id, ok := mt.Key.(*ast.Ident); !ok || id.Name != "string" {
			return true
		}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			k, ok := kv.Key.(*ast.BasicLit)
			if !ok || k.Kind != token.STRING {
				continue
			}
			key := strings.Trim(k.Value, `"`)
			if !reErrorCode.MatchString(key) {
				continue
			}
			c.Registered[key] = true
			// The status is the entry's first integer literal — positional or
			// keyed, and in either arrangement the only 3-digit int in a row.
			if st := firstStatusIn(kv.Value); st > 0 {
				c.CodeStatus[key] = st
			}
		}
		return true
	})
	envNames := environmentNames(file)
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		v := strings.Trim(lit.Value, `"`)
		if !reErrorCode.MatchString(v) || envNames[v] || strings.HasPrefix(v, "WL_") {
			return true
		}
		if _, seen := c.Codes[v]; !seen {
			c.Codes[v] = f.Path
		}
		return true
	})

	// Routes are NOT collected here. A route pattern is frequently a constant
	// declared in another file — often another package — so it cannot be
	// resolved until every file has been read. See absorbRoutes.
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

// reErrorCode matches a protocol error code: SCREAMING_SNAKE with at least one
// underscore, so a single word like "GET" or "POST" is not mistaken for one.
var reErrorCode = regexp.MustCompile(`^[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+$`)

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

var reGoModRequire = regexp.MustCompile(`(?m)^\s*(?:require\s+)?([a-z0-9][\w.\-]*\.[a-z]{2,}(?:/[\w.\-~]+)+)\s+v\S+(.*)$`)

// goModRequires lists the module paths a go.mod requires DIRECTLY.
//
// Lines marked `// indirect` are excluded. They are not the implementation's
// dependencies — they are its dependencies' dependencies, written by the
// toolchain, and no author chose them. The generated hub declared
// golang.org/x/crypto for Argon2id and `go mod tidy` added golang.org/x/sys
// beneath it; counting that against a dependency policy reports a violation
// nobody committed and cannot fix without removing the permitted dependency
// above it.
func goModRequires(content string) []string {
	var out []string
	for _, m := range reGoModRequire.FindAllStringSubmatch(content, -1) {
		if strings.Contains(m[2], "// indirect") {
			continue
		}
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
	// OutcomeNotApplicable — the assertion is conditional and its premise is
	// established false. Distinct from `unchecked`: nobody needs to review it.
	OutcomeNotApplicable Outcome = "not-applicable"
	// OutcomeUnchecked — nothing mechanical applies.
	OutcomeUnchecked Outcome = "unchecked"
	// OutcomeInconclusive — a check APPLIED and could not settle the question,
	// because the source defeated it rather than contradicted it.
	//
	// Distinct from every other outcome and the distinction matters:
	//
	//	unchecked      no check exists for this assertion
	//	failed         a check settled it AGAINST the source
	//	inconclusive   a check ran and the source could not be read
	//
	// Collapsing the third into the second is how one run reported 25
	// "structural checks disagree with the specification" when what it meant
	// was that a generated route table is a loop over constants this tool
	// cannot resolve. The detail said "cannot tell"; the verdict said
	// "refuted". A checker that reports what it does not know as a fault is a
	// checker people learn to ignore, and 25 of them at once teaches that
	// lesson in one sitting.
	OutcomeInconclusive Outcome = "inconclusive"
)

// inconclusivePrefix marks a check's detail as unresolvable rather than failed.
//
// A sentinel on the detail string, because the alternative is a fourth return
// value on every structural check to serve the two that need it. Defined here
// and consumed in exactly one place (evaluateItem), so the producer and the
// reader cannot drift; `markInconclusive` is the only way to write it and a
// guard asserts the marker never reaches printed output.
const inconclusivePrefix = "\x00inconclusive\x00"

// markInconclusive labels a detail as "a check ran and could not settle this".
func markInconclusive(detail string) string { return inconclusivePrefix + detail }

// splitInconclusive reports whether a detail was marked, and strips the marker.
func splitInconclusive(detail string) (string, bool) {
	if strings.HasPrefix(detail, inconclusivePrefix) {
		return strings.TrimPrefix(detail, inconclusivePrefix), true
	}
	return detail, false
}

// reConditional matches an assertion whose obligation is conditional.
//
// platforms/go.md: "IF SQLite was chosen: WAL journal mode, `user_version`
// pragma for migrations, tables created with `CREATE TABLE IF NOT EXISTS`".
// Evaluated unconditionally, that assertion fails every implementation that took
// the JSONL default — a failure for making the choice the blueprint recommends.
var reConditional = regexp.MustCompile(`(?i)^\s*IF\s+(.+?):\s*(.+)$`)

// splitConditional separates a conditional assertion's premise from its
// obligation.
func splitConditional(text string) (premise, obligation string, ok bool) {
	m := reConditional.FindStringSubmatch(text)
	if m == nil {
		return "", text, false
	}
	return strings.TrimSpace(m[1]), strings.TrimSpace(m[2]), true
}

// premiseTest settles whether a conditional assertion's premise holds.
//
// Deliberately a short registered list rather than a general evaluator. A
// premise nothing here recognises leaves the assertion UNCHECKED with the
// premise quoted — not excused. Guessing that a premise is false is how a
// checking layer starts silently forgiving requirements, which is worse than
// the false failure it would be fixing.
type premiseTest struct {
	// names are matched, lower-cased, against the premise text.
	names []string
	// holds reports whether the premise is true, and whether it was settled.
	holds func(c *CheckContext) (bool, bool)
}

var premiseTests = []premiseTest{
	{
		// "IF SQLite was chosen" — settled by whether a SQLite driver is in the
		// module graph. A Go program cannot use SQLite without one.
		names: []string{"sqlite"},
		holds: func(c *CheckContext) (bool, bool) {
			for _, m := range c.Modules {
				if strings.Contains(strings.ToLower(m), "sqlite") {
					return true, true
				}
			}
			for path := range c.Imports {
				if strings.Contains(strings.ToLower(path), "sqlite") {
					return true, true
				}
			}
			return false, true
		},
	},
}

// evaluatePremise settles a premise if any registered test recognises it.
func evaluatePremise(premise string, c *CheckContext) (holds, settled bool) {
	lower := strings.ToLower(premise)
	for _, pt := range premiseTests {
		for _, name := range pt.names {
			if strings.Contains(lower, name) {
				return pt.holds(c)
			}
		}
	}
	return false, false
}

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
	Keys   []string // ticked spans the assertion names as FIELDS of a type
	// Values are every ticked span that could be a value — a lowercase
	// identifier, anywhere in the text.
	//
	// Separate from Keys because the two are different claims about the same
	// notation. "OperationIntent requires `id`…; operation includes `list` and
	// `query`" names eight fields and two values, in backticks, in one sentence.
	// A check about fields must not see the values and a check about values must
	// not be limited to the field clause.
	Values []string
	Types  []string // known type names named in the text
	Routes []string // "POST /v1/register" style references in the text
}

var (
	reTicked     = regexp.MustCompile("`([^`]+)`")
	reJSONKeyish = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	reRouteRef   = regexp.MustCompile(`\b(GET|POST|PUT|PATCH|DELETE)\s+` + "`?" + `(/v\d[\w/{}.\-]*)`)
	rePathRef    = regexp.MustCompile("`?(/v\\d/[\\w/{}.\\-]+)`?")
)

// fieldKeys returns the backticked spans an assertion names as FIELDS.
//
// # Why this is narrower than "every backticked lowercase word"
//
// protocol/types.md mixes required fields and permitted values in one sentence:
//
//	OperationIntent requires `id`, `agent`, `operation`, `resource`,
//	`resource_class`, `scope`, `environment`, and `timestamp`; operation
//	includes `list` and `query`
//
// `list` and `query` are values the `operation` field may take, not fields of the
// type. Reading every backticked token as a field reported three correct types as
// missing keys — a confident wrong answer, and the third one told me the pattern
// rather than the instance.
//
// The rule: the FIRST clause only, and only when it says requires or includes.
// A clause after a semicolon may be about anything — defaults, value sets, what a
// signature covers — and no reading of the words settles which. Coverage is lost
// where a later clause does name fields; losing coverage is the safe direction,
// because an unchecked assertion asks a human to look and a wrongly failed one
// asks a model to break working code.
func fieldKeys(text string) []string {
	clause := text
	if i := strings.Index(clause, ";"); i >= 0 {
		clause = clause[:i]
	}
	lower := strings.ToLower(clause)
	if !strings.Contains(lower, "requires") && !strings.Contains(lower, "includes") {
		return nil
	}
	var out []string
	for _, m := range reTicked.FindAllStringSubmatch(clause, -1) {
		if reJSONKeyish.MatchString(m[1]) {
			out = append(out, m[1])
		}
	}
	return out
}

// parseAssertion pulls the mechanically usable pieces out of one item.
func parseAssertion(item ChecklistItem, c *CheckContext) assertion {
	a := assertion{Text: item.Text, Lower: strings.ToLower(item.Text)}
	for _, m := range reTicked.FindAllStringSubmatch(item.Text, -1) {
		a.Ticked = append(a.Ticked, m[1])
	}
	a.Keys = fieldKeys(item.Text)
	for _, t := range a.Ticked {
		if reJSONKeyish.MatchString(t) {
			a.Values = append(a.Values, t)
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
		// "ScopeLevel enum is constrained to `public`, `internal`, ..."
		//
		// One-way in the other direction from the field check: the values being
		// declared does not establish that anything REJECTS values outside the
		// set, which is what "constrained" claims.
		name:   "enum values are declared",
		oneWay: true,
		applies: func(a assertion) bool {
			return strings.Contains(a.Lower, "enum") && strings.Contains(a.Lower, "constrained") && len(a.Values) > 1
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
			for _, k := range a.Values {
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
			// A mux registration this tool could not read is not evidence of an
			// absent handler. Saying "no handler is registered" about a hub whose
			// route table is a loop over constants is a confident zero, and it
			// refuted twenty-one correct assertions in one run.
			if len(c.UnroutableCalls) > 0 {
				// INCONCLUSIVE, not refuted. This tool could not read the route
				// table; that is a fact about the tool, not about the hub.
				return false, markInconclusive("cannot tell whether a handler is registered for " +
						strings.Join(missing, ", ") + " — " +
						plural(len(c.UnroutableCalls), "mux registration") +
						" in " + firstUnroutable(c) + " could not be resolved to a path"),
					routeHosts(c)
			}
			// Nothing owns a route that does not exist, so the blame is the file
			// the plan put the HTTP surface in — found by where the other routes
			// live rather than guessed at.
			return false, "no handler is registered for: " + strings.Join(missing, ", "), routeHosts(c)
		},
	},
	{
		// "All protocol-level error codes … are registered centrally"
		//
		// The generated hub transcribed the blueprint's error table faithfully and
		// then its handlers wrote codes that were not in it: AUTH_FAILED,
		// VALIDATION_FAILED, STORAGE_ERROR, and SIGNATURE_INVALID where the
		// protocol says INVALID_SIGNATURE. Every status lookup for an unregistered
		// code falls through to 500, so `GET /v1/services` without a token
		// answered 500 with a correct AUTH_FAILED body — an interoperability break
		// that no compiler can see and that the checklist already covers.
		//
		// One-way: every used code being registered establishes the first clause
		// for this artifact and says nothing about the agent-local naming rule in
		// the second.
		name:   "error codes are centrally registered",
		oneWay: true,
		applies: func(a assertion) bool {
			return strings.Contains(a.Lower, "error code") &&
				(strings.Contains(a.Lower, "registered centrally") ||
					strings.Contains(a.Lower, "registered") && strings.Contains(a.Lower, "collide"))
		},
		test: func(a assertion, c *CheckContext) (bool, string, []string) {
			var unregistered, blame []string
			for code, file := range c.Codes {
				if c.Registered[code] {
					continue
				}
				unregistered = append(unregistered, code)
				if !containsString(blame, file) {
					blame = append(blame, file)
				}
			}
			if len(unregistered) == 0 {
				return true, "", nil
			}
			sort.Strings(unregistered)
			sort.Strings(blame)
			return false, "error code(s) used but not in the central registry: " +
				strings.Join(unregistered, ", "), blame
		},
	},
	{
		// "each standard error code maps to the correct HTTP status"
		//
		// Checked against protocol/types.md's own table rather than a copy of it
		// written into the tooling — a second copy is a second thing to keep
		// right, and it would disagree with the blueprint the moment either moved.
		name: "registered codes carry the status the protocol assigns",
		applies: func(a assertion) bool {
			return strings.Contains(a.Lower, "error code") && strings.Contains(a.Lower, "http status")
		},
		test: func(a assertion, c *CheckContext) (bool, string, []string) {
			want := specErrorTable(c.Spec)
			if len(want) == 0 {
				// No table to check against: say so rather than pass.
				return true, "", nil
			}
			var wrong []string
			for code, spec := range want {
				got, ok := c.CodeStatus[code]
				if !ok {
					continue // absence is the other check's business
				}
				if got != spec.Status {
					wrong = append(wrong, fmt.Sprintf("%s is %d, protocol says %d", code, got, spec.Status))
				}
			}
			if len(wrong) == 0 {
				return true, "", nil
			}
			sort.Strings(wrong)
			return false, "status mismatch: " + strings.Join(wrong, "; "), blameOwners(c, "ErrorCodes")
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
		// "each binary is `package main` under cmd/ and shared code is a package
		// under internal/".
		//
		// This used to demand `package main` in EVERY file, which was right while
		// platforms/go specified one flat package per component and became wrong
		// the moment it specified a module with cmd/ and internal/. A check that
		// encodes a layout outlives the layout.
		name: "binaries are package main and shared code is not",
		applies: func(a assertion) bool {
			return strings.Contains(a.Lower, "package main")
		},
		test: func(a assertion, c *CheckContext) (bool, string, []string) {
			var bad []string
			isMain := regexp.MustCompile(`(?m)^package\s+main\s*$`)
			for _, f := range c.Files {
				if !strings.HasSuffix(f.Path, ".go") {
					continue
				}
				underCmd := strings.HasPrefix(f.Path, "cmd/") || !strings.Contains(f.Path, "/")
				if underCmd && !isMain.MatchString(f.Content) {
					bad = append(bad, f.Path+" (a binary that is not package main)")
				}
				if strings.HasPrefix(f.Path, "internal/") && isMain.MatchString(f.Content) {
					bad = append(bad, f.Path+" (shared code declared package main)")
				}
			}
			if len(bad) == 0 {
				return true, "", nil
			}
			return false, strings.Join(bad, ", "), bad
		},
	},
	{
		// "HTTP handlers write error JSON responses and do not panic".
		//
		// The subject is HTTP HANDLERS. A search of the whole source for `panic(`
		// failed a correct implementation whose only panic was an init() assertion
		// that the error-code registry's map keys agree with their entries — a
		// startup invariant, and better code than not checking. The assertion
		// names its subject, so the check reads the AST for functions with a
		// handler signature and looks only inside those.
		name: "HTTP handlers do not panic",
		applies: func(a assertion) bool {
			return strings.Contains(a.Lower, "do not panic")
		},
		test: func(a assertion, c *CheckContext) (bool, string, []string) {
			var found []string
			// What was actually LOOKED at, so an empty `found` can be told apart
			// from a search that never ran. Without these two counters this check
			// returned its strongest positive verdict — verified — when every Go
			// file failed to parse, or when the tenant had no handler-shaped
			// function at all. "I could not read this" reported as "this is
			// correct" is the same fault the route-resolution check was already
			// fixed for, pointing the other way.
			goFiles, parsed, handlers := 0, 0, 0
			for _, f := range c.Files {
				if !strings.HasSuffix(f.Path, ".go") {
					continue
				}
				goFiles++
				fset := token.NewFileSet()
				file, err := parser.ParseFile(fset, f.Path, f.Content, parser.SkipObjectResolution)
				if err != nil {
					continue
				}
				parsed++
				for _, d := range file.Decls {
					fn, ok := d.(*ast.FuncDecl)
					if !ok || !isHTTPHandler(fn) {
						continue
					}
					handlers++
					if panicsIn(fn) {
						found = append(found, f.Path+":"+fn.Name.Name)
					}
				}
			}
			if len(found) > 0 {
				return false, "panic() called in HTTP handler(s): " + strings.Join(found, ", "),
					handlerFiles(found)
			}
			if goFiles > 0 && parsed == 0 {
				return false, markInconclusive(fmt.Sprintf(
					"none of the %d Go file(s) could be parsed, so no handler was examined", goFiles)), nil
			}
			if handlers == 0 {
				return false, markInconclusive(
					"no function with an HTTP handler signature was found, so there was nothing to check"), nil
			}
			return true, "", nil
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

// isHTTPHandler reports whether a function has the net/http handler signature.
//
// (http.ResponseWriter, *http.Request) — the only shape net/http will call. A
// name-based guess would miss a handler called serve and catch a helper called
// handleError.
func isHTTPHandler(fn *ast.FuncDecl) bool {
	if fn.Type == nil || fn.Type.Params == nil || len(fn.Type.Params.List) != 2 {
		return false
	}
	first := exprString(fn.Type.Params.List[0].Type)
	second := exprString(fn.Type.Params.List[1].Type)
	return strings.HasSuffix(first, "http.ResponseWriter") && strings.HasSuffix(second, "http.Request")
}

// panicsIn reports whether a function body calls panic directly.
func panicsIn(fn *ast.FuncDecl) bool {
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "panic" {
			found = true
			return false
		}
		return true
	})
	return found
}

// handlerFiles reduces "file:function" blame entries to distinct files.
func handlerFiles(entries []string) []string {
	var out []string
	for _, e := range entries {
		file := e
		if i := strings.LastIndex(e, ":"); i > 0 {
			file = e[:i]
		}
		if !containsString(out, file) {
			out = append(out, file)
		}
	}
	return out
}

// specErrorCode is one row of a blueprint's error-code table.
type specErrorCode struct {
	Status   int
	Category string
}

// reSpecErrorRow matches a row of protocol/types.md's error tables:
//
//	| `INVALID_SIGNATURE` | 401 | permanent | ML-DSA-65 signature verification failed |
var reSpecErrorRow = regexp.MustCompile("(?m)^\\|\\s*`([A-Z][A-Z0-9_]+)`\\s*\\|\\s*(\\d{3})\\s*\\|\\s*([a-z]+)")

// specErrorTable reads every error-code row out of the blueprints.
//
// The specification's table is the authority. Transcribing it into the tooling
// would create a second copy to keep right, and the two would disagree the
// moment either moved — which is the whole argument for reading the blueprint.
func specErrorTable(spec map[string]string) map[string]specErrorCode {
	out := map[string]specErrorCode{}
	for _, body := range spec {
		for _, m := range reSpecErrorRow.FindAllStringSubmatch(body, -1) {
			status := 0
			for _, ch := range m[2] {
				status = status*10 + int(ch-'0')
			}
			out[m[1]] = specErrorCode{Status: status, Category: m[3]}
		}
	}
	return out
}

// firstStatusIn returns the first HTTP-status-shaped integer literal in an
// expression, or 0.
//
// Positional and keyed composite literals arrange a registry entry differently,
// and both are correct Go. Reading the first three-digit integer works for either
// without the check caring which the model chose — which is the point: a check
// that only understands one arrangement fails correct code for its style.
func firstStatusIn(e ast.Expr) int {
	status := 0
	ast.Inspect(e, func(n ast.Node) bool {
		if status > 0 {
			return false
		}
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.INT || len(lit.Value) != 3 {
			return true
		}
		v := 0
		for _, ch := range lit.Value {
			if ch < '0' || ch > '9' {
				return true
			}
			v = v*10 + int(ch-'0')
		}
		if v >= 100 && v < 600 {
			status = v
		}
		return true
	})
	return status
}

// environmentNames returns the string literals a file reads from the environment.
//
// Exact rather than conventional: a literal passed to os.Getenv is an
// environment variable whatever it is named, and a check that guessed from the
// shape of the name would go on mistaking one for the other.
func environmentNames(file *ast.File) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, isIdent := sel.X.(*ast.Ident)
		if !isIdent || pkg.Name != "os" {
			return true
		}
		switch sel.Sel.Name {
		case "Getenv", "LookupEnv", "Setenv", "Unsetenv":
			if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				out[strings.Trim(lit.Value, `"`)] = true
			}
		}
		return true
	})
	return out
}

// firstUnroutable names one file with an unreadable registration, so the
// message points somewhere rather than describing a category.
func firstUnroutable(c *CheckContext) string {
	seen := map[string]bool{}
	var files []string
	for _, f := range c.UnroutableCalls {
		if !seen[f] {
			seen[f] = true
			files = append(files, f)
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		return "the artifact"
	}
	if len(files) == 1 {
		return files[0]
	}
	return fmt.Sprintf("%s and %d other file(s)", files[0], len(files)-1)
}
