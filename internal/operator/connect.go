package operator

// Connecting to a hub that already exists.
//
// # Two different acts, conflated once
//
// BOOTSTRAP creates a tenant: generate it, build it, start it, and establish
// its first operator. It needs the tenant's filesystem, because it is producing
// the tenant. That is `weblisk server provision`, and it is necessarily local.
//
// CONNECT presents a credential to a hub that is already running. It needs an
// ADDRESS and an operator identity, and nothing else — no project directory, no
// CLI-recorded run state, no access to the tenant's files. A hub on another
// host, in a container, or started by something that is not this CLI is
// reachable on exactly the same terms as one next door.
//
// The first implementation of Studio's connect called provision, so it required
// a workspace in scope, a project directory on this machine, and run state the
// CLI had written. That made "connect" mean "connect to a tenant I built here",
// which is the opposite of the boundary: Studio is a client of the tenant over
// the tenant's own services, and a client that needs the server's filesystem is
// not a client.
//
// # The three outcomes, and why they are three
//
// An operator either holds a credential at this hub, or is waiting for one, or
// cannot reach it. Reporting "pending approval" as a failure would tell someone
// their tenant is broken when the system is working exactly as designed —
// architecture/admin auto-approves the FIRST operator and requires an existing
// admin for every later one, so a second person joining an established tenant
// waits, and that is the correct answer rather than an error.

import (
	"errors"
	"fmt"
	"strings"
)

// ConnectState is the outcome of presenting a credential to a hub.
type ConnectState string

const (
	// ConnectedState — a token was issued and is held.
	ConnectedState ConnectState = "connected"
	// PendingApprovalState — registered at this hub, awaiting an admin. Not an
	// error: the hub is working as specified.
	PendingApprovalState ConnectState = "pending_approval"
	// UnreachableState — no hub answered, or it is not one of ours.
	UnreachableState ConnectState = "unreachable"
)

// ConnectResult is what a caller needs in order to proceed, or to explain why
// it cannot.
type ConnectResult struct {
	State     ConnectState `json:"state"`
	Address   string       `json:"address"`
	Operator  string       `json:"operator"`
	Token     string       `json:"token,omitempty"`
	ExpiresAt int64        `json:"expires_at,omitempty"`
	// Detail explains a state that is not "connected", in terms of what the
	// person can do about it.
	Detail string   `json:"detail,omitempty"`
	Steps  []string `json:"steps,omitempty"`
}

// Connect obtains an operator credential from a hub at an address.
//
// Idempotent by construction, because it is the ordinary way to connect and it
// will be run repeatedly: an operator this hub already knows gets a token
// straight away, one it does not know is registered first, and one awaiting
// approval is told so.
func Connect(address, name, passphrase string) (ConnectResult, error) {
	// Refused here too, so the message names the address the operator typed
	// rather than surfacing from three calls down.
	if terr := CheckCredentialTransport(address); terr != nil {
		return ConnectResult{
			State:    UnreachableState,
			Address:  address,
			Operator: name,
			Detail:   terr.Error(),
			Steps: []string{
				"reach the hub over https://, or",
				"forward it to this machine so the address is a loopback one",
			},
		}, terr
	}
	res := ConnectResult{Address: strings.TrimRight(strings.TrimSpace(address), "/"), Operator: name}
	if res.Address == "" {
		return res, fmt.Errorf("a hub address is required")
	}
	if strings.TrimSpace(name) == "" {
		return res, fmt.Errorf("an operator name is required — a credential is issued to a subject")
	}
	if len(passphrase) < 12 {
		return res, fmt.Errorf("the operator passphrase must be at least 12 characters")
	}

	// The identity is the subject's and is reused across hubs. Creating one here
	// is correct for a first connection from a new machine; replacing one is
	// never correct, and EnsureIdentity refuses to.
	created, err := EnsureIdentity(name, passphrase)
	if err != nil {
		return res, fmt.Errorf("operator identity: %w", err)
	}
	if created {
		res.Steps = append(res.Steps, "operator identity created for "+name)
	} else {
		res.Steps = append(res.Steps, "existing operator identity unlocked")
	}

	// Ask for a token first. A hub that already knows this operator needs no
	// registration, and attempting one would either fail as a duplicate or —
	// worse, at a hub with no operators — silently make this identity the
	// bootstrap admin of a tenant somebody else runs.
	if token, expires, terr := RequestToken(res.Address, name); terr == nil {
		res.State, res.Token, res.ExpiresAt = ConnectedState, token, expires
		res.Steps = append(res.Steps, "token issued — this hub already knows this operator")
		saveToken(res.Address, name, token, expires)
		return res, nil
	}

	// Not known here, or not approved. Register, then ask again.
	if rerr := RegisterWith(res.Address, name, passphrase); rerr != nil {
		switch {
		case IsAlreadyRegistered(rerr):
			res.Steps = append(res.Steps, "already registered with this hub")
		case errors.Is(rerr, ErrNeedsExistingAdmin):
			res.State = PendingApprovalState
			res.Detail = "this hub already has operators, so it will not register a new " +
				"one unauthenticated. An existing admin must add " + name +
				" — the first operator of a hub is auto-approved and every later one is not."
			return res, nil
		default:
			res.State = UnreachableState
			res.Detail = rerr.Error()
			return res, nil
		}
	} else {
		res.Steps = append(res.Steps, "registered with this hub")
	}

	token, expires, terr := RequestToken(res.Address, name)
	if terr != nil {
		// Which refusal it is decides what the person should do, so the three
		// are told apart rather than reported as the most hopeful one.
		var te *TokenError
		if errors.As(terr, &te) && te.EndpointAbsent() {
			res.State = UnreachableState
			res.Detail = fmt.Sprintf(
				"this hub does not serve POST /v1/admin/operators/token (HTTP %d). "+
					"It predates that part of the protocol, so no operator token can be "+
					"issued by it — regenerate the hub from current blueprints.", te.Status)
			return res, nil
		}
		if errors.As(terr, &te) && te.NotApproved() {
			res.State = PendingApprovalState
			res.Detail = "registered, and this hub has not approved this operator yet. " +
				"An existing admin must approve it — the first operator of a hub is " +
				"auto-approved, and every later one is not."
			return res, nil
		}
		res.State = UnreachableState
		res.Detail = terr.Error()
		return res, nil
	}
	res.State, res.Token, res.ExpiresAt = ConnectedState, token, expires
	res.Steps = append(res.Steps, "token issued")
	saveToken(res.Address, name, token, expires)
	return res, nil
}
