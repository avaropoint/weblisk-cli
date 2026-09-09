package tenant

// accept.go — asking the tenant whether it actually works.
//
// Until this existed, "created" meant the generator returned without an error.
// It did not mean the tenant compiled into something that answers, that the
// operator credential just issued could obtain a token, or that the routes the
// CLI and Studio call are routes this tenant serves.
//
// That gap was not theoretical. Three operator commands had never worked
// against any generated tenant: `operators revoke` posted to a path that does
// not exist (404), `operators role` used POST where the tenant serves PUT
// (405), and there was no approve route at all, so a tenant's second operator
// registered and then waited forever for an act nobody could perform. Every
// build reported success throughout.
//
// The reason it stayed invisible is that the checks that existed all asked the
// generator about itself. This asks the running tenant, over HTTP, the same way
// its clients will.
//
// # Reading the probes
//
// The route probes are UNAUTHENTICATED on purpose. The question is not "may I
// do this" — it is "does this tenant serve this method at this path", and the
// authentication middleware answers that before authorising anything:
//
//	401  the route and method exist, and declined an anonymous caller — pass
//	404  no such route — this tenant cannot do the thing at all
//	405  the route exists, the method does not — a client/server disagreement
//	200  the route exists and needs no credential (health, and only health)
//
// A probe therefore never needs a token, never changes anything, and cannot be
// made to change anything: the subject in the two probes that take one is a
// name no tenant issues.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Check is one question asked of a finished tenant, and its answer.
type Check struct {
	Name string `json:"name"`
	// OK is false when the tenant failed the check, and also false when the
	// check could not be run. Consult Detail; an unrun check is not a pass.
	OK bool `json:"ok"`
	// Fatal marks a check the tenant is unusable without. A non-fatal failure
	// is a capability this tenant does not have, which is worth saying and is
	// not a reason to call the build a failure.
	Fatal  bool   `json:"fatal"`
	Detail string `json:"detail"`
}

// probeSubject is the name used where a route takes one.
//
// Deliberately not a name anybody could hold: these probes run unauthenticated
// and are refused before they reach a handler, but a probe that would be
// destructive if the refusal ever regressed is a probe that should not name a
// real operator.
const probeSubject = "weblisk-route-probe-does-not-exist"

// acceptTimeout bounds the whole acceptance pass. Generous, because the tenant
// was started seconds ago and may still be opening its store.
const acceptTimeout = 30 * time.Second

// requiredRoutes is what the CLI and Studio call.
//
// Built from routes.go, not written out again here. When this list was its own
// copy of the paths it was a second place to be wrong about them, which is the
// fault this whole pass exists to catch.
var requiredRoutes = []AdminRoute{
	Services(),
	Overview(),
	OperatorList(),
	Agents(),
	Audit(),
	OperatorRegister(),
	OperatorToken(),
	// The three that were wrong. Named individually rather than as "operator
	// management" so a failure says which verb is missing.
	OperatorApprove(probeSubject),
	OperatorRole(probeSubject),
	OperatorRemove(probeSubject),
	// Model config is the hub's, not Studio's. Missing these is a capability
	// this tenant does not have (non-fatal on 404) until it is regenerated.
	Model(),
	SetModel(),
	ModelProviders(),
	Complete(),
}

// Accept asks a freshly built tenant whether it works, and reports every answer.
//
// It never returns an error for a failed check — a check is a finding, and the
// caller decides what a finding means. It returns an error only when it could
// not ask at all, which is itself the first check's job to report.
func Accept(ctx context.Context, address string) []Check {
	ctx, cancel := context.WithTimeout(ctx, acceptTimeout)
	defer cancel()

	base := strings.TrimRight(address, "/")
	client := &http.Client{Timeout: 10 * time.Second}
	out := []Check{healthCheck(ctx, client, base)}

	// Every later check talks to the same server, so there is nothing to learn
	// from running them against one that did not answer at all — and ten
	// timeouts in a row is a worse report than one.
	if !out[0].OK {
		out = append(out, Check{
			Name:   "routes",
			Fatal:  true,
			Detail: "not asked — the tenant did not answer its health check",
		})
		return out
	}

	for _, p := range requiredRoutes {
		out = append(out, routeCheck(ctx, client, base, p))
	}
	return out
}

func healthCheck(ctx context.Context, client *http.Client, base string) Check {
	c := Check{Name: "health", Fatal: true}
	req, err := http.NewRequestWithContext(ctx, "GET", base+"/v1/health", nil)
	if err != nil {
		c.Detail = "could not address " + base
		return c
	}
	resp, err := client.Do(req)
	if err != nil {
		c.Detail = "no answer from " + base + " — the tenant was started but is not listening: " + err.Error()
		return c
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		c.Detail = fmt.Sprintf("%s answered %d", base+"/v1/health", resp.StatusCode)
		return c
	}
	var h struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}
	if err := json.Unmarshal(body, &h); err != nil {
		c.Detail = "the health endpoint answered something that is not JSON"
		return c
	}
	// A tenant that is up but degraded is reported as what it is. Collapsing
	// "healthy" and "degraded" into a tick is how a broken store ships.
	var bad []string
	for name, state := range h.Checks {
		if state != "ok" {
			bad = append(bad, name+"="+state)
		}
	}
	if len(bad) > 0 {
		c.Detail = "answering, but reports " + strings.Join(bad, ", ")
		return c
	}
	if h.Status != "healthy" {
		c.Detail = "answering, but reports status " + bracket(h.Status)
		return c
	}
	c.OK = true
	c.Detail = "answering and healthy"
	return c
}

func routeCheck(ctx context.Context, client *http.Client, base string, p AdminRoute) Check {
	c := Check{Name: p.Method + " " + trimProbeSubject(p.Path)}
	req, err := http.NewRequestWithContext(ctx, p.Method, base+p.Path, nil)
	if err != nil {
		c.Detail = "could not be asked: " + err.Error()
		return c
	}
	resp, err := client.Do(req)
	if err != nil {
		c.Detail = "no answer: " + err.Error()
		return c
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))

	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusOK,
		http.StatusBadRequest, http.StatusUnprocessableEntity:
		// Served. 400 and 422 mean the handler ran and disliked an empty body,
		// which is a handler that exists — the question this probe asks.
		c.OK = true
		c.Detail = "served"
	case http.StatusNotFound:
		c.Detail = "this tenant has no such route — " + p.Why
	case http.StatusMethodNotAllowed:
		c.Detail = "this tenant serves that path but not " + p.Method + " — " + p.Why
	default:
		c.Detail = fmt.Sprintf("answered %d, which is neither a refusal nor a route", resp.StatusCode)
	}
	return c
}

// trimProbeSubject keeps the probe's placeholder out of what a person reads.
func trimProbeSubject(path string) string {
	return strings.ReplaceAll(path, probeSubject, "{name}")
}
