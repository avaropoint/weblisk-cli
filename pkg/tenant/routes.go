package tenant

// routes.go — the requests a client of a tenant issues, declared once.
//
// This file exists because the CLI and the tenant disagreed about three of
// them and nothing noticed for months: `operators revoke` posted to
// /v1/admin/operators/{name}/revoke, which no tenant has ever served (404);
// `operators role` used POST where the tenant serves PUT (405); and approve had
// no route at all. Each command was written from memory of the specification
// rather than from it.
//
// Two independent copies of a request shape — one in the command that sends it,
// one in whatever checks it — drift, and the drift is invisible because both
// halves look right on their own. So there is one copy. The commands build
// their requests from these, `Accept` probes exactly these, and a tenant that
// fails the probe is a tenant the command would have failed against.

// AdminRoute is one method-and-path a client issues against a tenant.
type AdminRoute struct {
	Method string
	Path   string
	// Why says what stops working when a tenant does not serve this, in the
	// words of the person who would notice. "GET /v1/audit missing" is a fact;
	// "the audit log cannot be read" is the same fact, usable.
	Why string
}

// The operator-management routes, per architecture/admin's Operator Management
// table. The name is interpolated by the caller, which is why these are
// functions rather than constants.

// OperatorApprove admits a registered operator. Admin only.
func OperatorApprove(name string) AdminRoute {
	return AdminRoute{"POST", "/v1/admin/operators/" + name + "/approve",
		"a second operator can never be admitted — `weblisk operators approve` has nothing to call"}
}

// OperatorRole changes an operator's role. PUT, not POST.
func OperatorRole(name string) AdminRoute {
	return AdminRoute{"PUT", "/v1/admin/operators/" + name + "/role",
		"`weblisk operators role` cannot change anybody's role"}
}

// OperatorRemove removes an operator. DELETE on the operator itself — there is
// no /revoke sub-route, and inventing one is what shipped.
func OperatorRemove(name string) AdminRoute {
	return AdminRoute{"DELETE", "/v1/admin/operators/" + name,
		"`weblisk operators revoke` cannot remove anybody"}
}

// OperatorList, OperatorGet and the unauthenticated registration pair.
func OperatorList() AdminRoute {
	return AdminRoute{"GET", "/v1/admin/operators", "`weblisk operators list` cannot see anybody"}
}
func OperatorGet(name string) AdminRoute {
	return AdminRoute{"GET", "/v1/admin/operators/" + name, "an operator's detail cannot be read"}
}
func OperatorRegister() AdminRoute {
	return AdminRoute{"POST", "/v1/admin/operators/register", "nobody else can register"}
}
func OperatorToken() AdminRoute {
	return AdminRoute{"POST", "/v1/admin/operators/token",
		"no operator can obtain a token, including the first"}
}

// The reads Studio and the CLI make of a running tenant.
func Services() AdminRoute {
	return AdminRoute{"GET", "/v1/services", "the signed service directory cannot be read"}
}
func Overview() AdminRoute {
	return AdminRoute{"GET", "/v1/admin/overview", "Studio's tenant panel has nothing to show"}
}
func Agents() AdminRoute {
	return AdminRoute{"GET", "/v1/admin/agents", "agents cannot be listed"}
}
func Audit() AdminRoute {
	return AdminRoute{"GET", "/v1/audit", "the audit log cannot be read"}
}
