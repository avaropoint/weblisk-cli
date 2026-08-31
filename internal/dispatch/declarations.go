package dispatch

// Extracting what a generated file declares.
//
// # Why this exists
//
// architecture/generation.md, prompt contract element 3: a generator must pass
// the accumulated DECLARATIONS of previously generated files, not their names.
//
// The first real run of manifest-driven generation produced all six files of a
// Go orchestrator and 73 build errors — 7 symbols declared in two files, 29
// undefined, 19 signature mismatches. Every one had the same cause. The prompt
// said "already generated: protocol.go, identity.go", which tells a model that
// those files exist and nothing about what is in them. So identity.go declared
// writeCanonical, helpers.go declared it again, main.go called
// NewOrchestrator(OrchestratorConfig) while orchestrator.go defined
// NewOrchestrator(int, *SigningKeyPair, string), and helpers.go called a
// CanonicalizeJSON that nobody wrote.
//
// Naming the symbols removes the guesswork. Naming their SIGNATURES removes the
// mismatches too, which is why the Go extractor reproduces parameter and result
// types rather than just identifiers.
//
// # Exact where possible
//
// Go is parsed with go/parser — standard library, so no dependency, and exact
// rather than approximated. A regex over source would miss a method on a type
// declared in a block, or invent a declaration from a string literal, and a
// WRONG declaration list is worse than none: it tells the model something false
// and it will believe it.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"sort"
	"strings"
)

// Declaration is one top-level symbol a file exposes.
type Declaration struct {
	Name      string
	Signature string // full form where the language allows it
	// Receiver is the type a method is declared on, empty for everything else.
	//
	// Methods are namespaced BY that type: func (a *SQLiteStore) LoadAgents and
	// func (b *MemStore) LoadAgents are different methods and do not collide.
	// Recording only the bare name reported legal code as a redeclaration, and
	// blocked a run in which all ten files had generated correctly.
	Receiver string
}

// Key is the identifier a declaration actually occupies.
//
// For a method that is receiver-qualified; for everything else it is the name.
func (d Declaration) Key() string {
	if d.Receiver != "" {
		return d.Receiver + "." + d.Name
	}
	return d.Name
}

func (d Declaration) String() string {
	if d.Signature != "" {
		return d.Signature
	}
	return d.Name
}

// ExtractDeclarations returns the top-level symbols a file declares.
//
// Returns nil for languages with no extractor rather than guessing. A caller
// that receives nothing knows it has nothing; a caller handed a bad guess does
// not.
func ExtractDeclarations(path, content string) []Declaration {
	switch strings.ToLower(pathExt(path)) {
	case ".go":
		return goDeclarations(content)
	case ".js", ".mjs", ".ts":
		return jsDeclarations(content)
	}
	return nil
}

func pathExt(p string) string {
	if i := strings.LastIndex(p, "."); i >= 0 {
		return p[i:]
	}
	return ""
}

// goDeclarations parses Go source and returns its exported and unexported
// top-level declarations.
//
// Unexported ones are included deliberately: within one package they collide
// exactly as exported ones do, and the redeclaration errors this exists to
// prevent were lower-case helpers.
func goDeclarations(src string) []Declaration {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "gen.go", src, parser.SkipObjectResolution)
	if err != nil {
		// A file that does not parse has no trustworthy declarations. Returning
		// none is honest; returning a partial list from a broken parse is not.
		return nil
	}
	var out []Declaration
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			out = append(out, Declaration{
				Name: d.Name.Name, Signature: goFuncSignature(d), Receiver: goReceiverType(d),
			})
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					out = append(out, Declaration{
						Name:      s.Name.Name,
						Signature: fmt.Sprintf("type %s %s", s.Name.Name, typeDetail(s.Type)),
					})
				case *ast.ValueSpec:
					kind := "var"
					if d.Tok == token.CONST {
						kind = "const"
					}
					for _, n := range s.Names {
						if n.Name == "_" {
							continue
						}
						out = append(out, Declaration{
							Name:      n.Name,
							Signature: fmt.Sprintf("%s %s", kind, n.Name),
						})
					}
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// goReceiverType returns the bare type a method is declared on, without pointer
// or type-parameter decoration, or "" for a plain function.
func goReceiverType(d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return ""
	}
	t := d.Recv.List[0].Type
	for {
		switch x := t.(type) {
		case *ast.StarExpr:
			t = x.X
		case *ast.IndexExpr: // generic receiver: Foo[T]
			t = x.X
		case *ast.IndexListExpr:
			t = x.X
		case *ast.Ident:
			return x.Name
		default:
			return ""
		}
	}
}

// goFuncSignature renders a function or method signature.
func goFuncSignature(d *ast.FuncDecl) string {
	var b strings.Builder
	b.WriteString("func ")
	if d.Recv != nil && len(d.Recv.List) > 0 {
		b.WriteString("(" + fieldListString(d.Recv, false) + ") ")
	}
	b.WriteString(d.Name.Name)
	b.WriteString("(" + fieldListString(d.Type.Params, true) + ")")
	if d.Type.Results != nil && len(d.Type.Results.List) > 0 {
		res := fieldListString(d.Type.Results, true)
		if len(d.Type.Results.List) > 1 || len(d.Type.Results.List[0].Names) > 0 {
			b.WriteString(" (" + res + ")")
		} else {
			b.WriteString(" " + res)
		}
	}
	return b.String()
}

func fieldListString(fl *ast.FieldList, withNames bool) string {
	if fl == nil {
		return ""
	}
	parts := make([]string, 0, len(fl.List))
	for _, f := range fl.List {
		t := exprString(f.Type)
		if withNames && len(f.Names) > 0 {
			names := make([]string, 0, len(f.Names))
			for _, n := range f.Names {
				names = append(names, n.Name)
			}
			parts = append(parts, strings.Join(names, ", ")+" "+t)
		} else {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, ", ")
}

// typeDetail renders a type with enough of its shape for another FILE to use it.
//
// # Why this is not exprString
//
// exprString collapses a struct to the word "struct", which was a deliberate
// choice and the wrong one. A run left fifty build errors concentrated in one
// file, six of them "target.Name undefined (type AgentEntry has no field or
// method Name)" — because the file was told AgentEntry existed and never told
// what was in it, so it guessed.
//
// Reaching into another file's struct is among the most common cross-file
// errors there is, and the information that prevents it was being discarded to
// save prompt space. Fields are cheap; a wrong guess costs a repair round.
//
// Interfaces get their method set for the same reason. Everything else stays
// compact — a caller needs a map's key and value types, not a recursive
// expansion of them.
func typeDetail(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StructType:
		if t.Fields == nil || len(t.Fields.List) == 0 {
			return "struct{}"
		}
		parts := make([]string, 0, len(t.Fields.List))
		for _, f := range t.Fields.List {
			ft := exprString(f.Type)
			if len(f.Names) == 0 {
				parts = append(parts, ft) // embedded
				continue
			}
			names := make([]string, 0, len(f.Names))
			for _, n := range f.Names {
				names = append(names, n.Name)
			}
			parts = append(parts, strings.Join(names, ", ")+" "+ft)
		}
		return "struct{ " + strings.Join(parts, "; ") + " }"
	case *ast.InterfaceType:
		if t.Methods == nil || len(t.Methods.List) == 0 {
			return "interface{}"
		}
		parts := make([]string, 0, len(t.Methods.List))
		for _, m := range t.Methods.List {
			if len(m.Names) == 0 {
				parts = append(parts, exprString(m.Type))
				continue
			}
			sig := ""
			if fn, ok := m.Type.(*ast.FuncType); ok {
				sig = "(" + fieldListString(fn.Params, true) + ")"
				if fn.Results != nil && len(fn.Results.List) > 0 {
					sig += " (" + fieldListString(fn.Results, false) + ")"
				}
			}
			parts = append(parts, m.Names[0].Name+sig)
		}
		return "interface{ " + strings.Join(parts, "; ") + " }"
	}
	return exprString(e)
}

// exprString renders a type expression compactly. Composite types collapse to
// their kind — a prompt needs "type Orchestrator struct", not sixty fields.
func exprString(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + exprString(t.X)
	case *ast.SelectorExpr:
		return exprString(t.X) + "." + t.Sel.Name
	case *ast.ArrayType:
		return "[]" + exprString(t.Elt)
	case *ast.MapType:
		return "map[" + exprString(t.Key) + "]" + exprString(t.Value)
	case *ast.Ellipsis:
		return "..." + exprString(t.Elt)
	case *ast.StructType:
		return "struct"
	case *ast.InterfaceType:
		return "interface"
	case *ast.FuncType:
		return "func(" + fieldListString(t.Params, false) + ")"
	case *ast.ChanType:
		return "chan " + exprString(t.Value)
	}
	return "…"
}

var reJSDecl = regexp.MustCompile(`(?m)^export\s+(?:async\s+)?(?:function|class|const|let|var)\s+([A-Za-z_$][\w$]*)`)

// jsDeclarations finds exported top-level names. Approximate by nature, so only
// exports are reported — those are the ones another file can collide with.
func jsDeclarations(src string) []Declaration {
	seen := map[string]bool{}
	var out []Declaration
	for _, m := range reJSDecl.FindAllStringSubmatch(src, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, Declaration{Name: m[1]})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// FormatDeclarations renders an accumulated set for a prompt, grouped by file.
func FormatDeclarations(byFile map[string][]Declaration, order []string) string {
	var b strings.Builder
	for _, path := range order {
		decls := byFile[path]
		if len(decls) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n%s declares:\n", path)
		for _, d := range decls {
			fmt.Fprintf(&b, "  %s\n", d)
		}
	}
	return b.String()
}

// DuplicateDeclarations reports symbols declared in more than one file — the
// coherence failure of architecture/generation.md Layer 2.
func DuplicateDeclarations(byFile map[string][]Declaration) map[string][]string {
	where := map[string]map[string]bool{}
	for path, decls := range byFile {
		for _, d := range decls {
			key := d.Key()
			if where[key] == nil {
				where[key] = map[string]bool{}
			}
			// A set, not a list: two methods of the same name on different types
			// within ONE file are legal, and reporting "store.go, store.go" as a
			// collision was how that bug announced itself.
			where[key][path] = true
		}
	}
	dupes := map[string][]string{}
	for key, files := range where {
		if len(files) > 1 {
			list := make([]string, 0, len(files))
			for f := range files {
				list = append(list, f)
			}
			sort.Strings(list)
			dupes[key] = list
		}
	}
	return dupes
}
