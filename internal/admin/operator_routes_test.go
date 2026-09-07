package admin

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/avaropoint/weblisk-cli/pkg/tenant"
)

// recorder answers every request with an empty JSON object and remembers the
// method and path it was asked for.
type recorder struct {
	method, path string
}

func (rec *recorder) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.method, rec.path = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"operators":[]}`))
	}))
}

func (rec *recorder) client(url string) *Client {
	return &Client{
		BaseURL:    url,
		Token:      "test-token",
		HTTPClient: &http.Client{Timeout: 5 * time.Second},
		JSONOutput: true,
	}
}

// Each operator command must issue the method and path the shared table
// declares. Asserted through the command's own request rather than by reading
// the table twice, because the fault this catches is a command that agrees with
// itself and disagrees with the tenant:
//
//	operators revoke → POST /v1/admin/operators/{name}/revoke  (404, always)
//	operators role   → POST /v1/admin/operators/{name}/role    (405, always)
//
// Both had never worked against any generated tenant.
func TestOperatorCommandsIssueTheDeclaredRequest(t *testing.T) {
	for _, tc := range []struct {
		name string
		want tenant.AdminRoute
		call func(c *Client) error
	}{
		{
			"approve", tenant.OperatorApprove("alice"),
			func(c *Client) error { _, err := c.route(tenant.OperatorApprove("alice"), nil); return err },
		},
		{
			"role", tenant.OperatorRole("alice"),
			func(c *Client) error {
				_, err := c.route(tenant.OperatorRole("alice"), map[string]string{"role": "viewer"})
				return err
			},
		},
		{
			"remove", tenant.OperatorRemove("alice"),
			func(c *Client) error { _, err := c.route(tenant.OperatorRemove("alice"), nil); return err },
		},
		{
			"list", tenant.OperatorList(),
			func(c *Client) error { _, err := c.route(tenant.OperatorList(), nil); return err },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			srv := rec.server(t)
			defer srv.Close()

			if err := tc.call(rec.client(srv.URL)); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if rec.method != tc.want.Method {
				t.Errorf("issued %s, the table declares %s", rec.method, tc.want.Method)
			}
			if rec.path != tc.want.Path {
				t.Errorf("issued %s, the table declares %s", rec.path, tc.want.Path)
			}
		})
	}
}

// The routes must not regress to the shapes that shipped broken. Named
// explicitly so that reintroducing either is a test edit somebody has to argue
// for, not a silent change.
func TestTheBrokenOperatorRoutesStayFixed(t *testing.T) {
	if r := tenant.OperatorRemove("alice"); r.Method != "DELETE" || r.Path != "/v1/admin/operators/alice" {
		t.Errorf("removal is %s %s; the tenant serves DELETE /v1/admin/operators/{name} and has never served a /revoke sub-route", r.Method, r.Path)
	}
	if r := tenant.OperatorRole("alice"); r.Method != "PUT" {
		t.Errorf("role change is %s; the tenant answers POST on that path with 405", r.Method)
	}
	if r := tenant.OperatorApprove("alice"); r.Method != "POST" || r.Path != "/v1/admin/operators/alice/approve" {
		t.Errorf("approval is %s %s; architecture/admin specifies POST /v1/admin/operators/{name}/approve", r.Method, r.Path)
	}
}

// A 404 and a 405 must be reported apart, and apart from a plain HTTP error.
// Flattened to "not found", a route this CLI asks for and the tenant does not
// serve reads as "no such operator", and the reader investigates the wrong half
// of the sentence.
func TestRouteFaultsAreNamedApart(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{
		{http.StatusNotFound, "no such route or record"},
		{http.StatusMethodNotAllowed, "not that method"},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
		}))
		c := &Client{BaseURL: srv.URL, Token: "t", HTTPClient: &http.Client{Timeout: 5 * time.Second}}
		_, err := c.route(tenant.OperatorApprove("alice"), nil)
		srv.Close()
		if err == nil {
			t.Fatalf("%d produced no error", tc.status)
		}
		if !contains(err.Error(), tc.want) {
			t.Errorf("%d says %q, want it to mention %q", tc.status, err.Error(), tc.want)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

// The test above proves route() sends what the table says. It does NOT prove
// that OperatorsApprove calls OperatorApprove — a command wired to the wrong
// table entry would pass it, sending a well-formed request to the wrong place.
// That is the same shape as the fault being fixed, so it is checked at the
// source: each command's body must name its own route constructor.
//
// Derived from the file rather than from a list typed here. A hand-written
// subject list tests the diligence of whoever last edited it.
func TestEachOperatorCommandCallsItsOwnRoute(t *testing.T) {
	want := map[string]string{
		"OperatorsApprove":  "OperatorApprove",
		"OperatorsRole":     "OperatorRole",
		"OperatorsRevoke":   "OperatorRemove",
		"OperatorsList":     "OperatorList",
		"OperatorsDescribe": "OperatorGet",
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "admin.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil {
			continue
		}
		expect, tracked := want[fn.Name.Name]
		if !tracked {
			continue
		}
		seen[fn.Name.Name] = true

		var called []string
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "tenant" {
				return true
			}
			called = append(called, sel.Sel.Name)
			return true
		})

		if len(called) == 0 {
			t.Errorf("%s builds its request by hand — it must use tenant.%s so the method and path travel together", fn.Name.Name, expect)
			continue
		}
		for _, got := range called {
			if got != expect {
				t.Errorf("%s calls tenant.%s; it must call tenant.%s", fn.Name.Name, got, expect)
			}
		}
	}

	for name := range want {
		if !seen[name] {
			t.Errorf("%s is no longer in admin.go — this guard's map is stale", name)
		}
	}
}
