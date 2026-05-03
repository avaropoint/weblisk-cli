package main

// Weblisk CLI — project scaffolding, code generation, and hub operations.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/avaropoint/weblisk-cli/internal/admin"
	"github.com/avaropoint/weblisk-cli/internal/build"
	"github.com/avaropoint/weblisk-cli/internal/config"
	"github.com/avaropoint/weblisk-cli/internal/deploy"
	"github.com/avaropoint/weblisk-cli/internal/deps"
	"github.com/avaropoint/weblisk-cli/internal/dispatch"
	"github.com/avaropoint/weblisk-cli/internal/doctor"
	"github.com/avaropoint/weblisk-cli/internal/domain"
	"github.com/avaropoint/weblisk-cli/internal/gateway"
	"github.com/avaropoint/weblisk-cli/internal/marketplace"
	"github.com/avaropoint/weblisk-cli/internal/operator"
	"github.com/avaropoint/weblisk-cli/internal/policy"
	"github.com/avaropoint/weblisk-cli/internal/project"
	"github.com/avaropoint/weblisk-cli/internal/secrets"
	"github.com/avaropoint/weblisk-cli/internal/serve"
	"github.com/avaropoint/weblisk-cli/internal/server"
	"github.com/avaropoint/weblisk-cli/internal/server/agent"
	"github.com/avaropoint/weblisk-cli/internal/test"
)

// version is set at build time via -ldflags "-X main.version=X.Y.Z"
var version = "dev"

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		printHelp()
		return
	}

	cmd := args[0]
	rest := args[1:]

	switch cmd {

	// ── Project Commands ─────────────────────────────────────

	case "new":
		name, templates, local, lib := parseNewArgs(rest)
		if name == "" {
			fatal("Usage: weblisk new <project-name> [--template blog|dashboard|docs] [--local] [--lib path]")
		}
		cwd, _ := os.Getwd()
		if err := project.Scaffold(name, cwd, templates, local, lib); err != nil {
			fatal("scaffold failed: %v", err)
		}

	case "dev":
		root, port := parseDevArgs(rest)
		config.Load(root)
		if err := serve.Serve(root, port); err != nil {
			fatal("dev server failed: %v", err)
		}

	case "build":
		root, opts := parseBuildArgs(rest)
		config.Load(root)
		if err := build.Build(root, opts); err != nil {
			fatal("build failed: %v", err)
		}

	case "vendor":
		cwd, _ := os.Getwd()
		config.Load(cwd)
		dest := ""
		for i := 0; i < len(rest); i++ {
			a := rest[i]
			if a == "--dest" && i+1 < len(rest) {
				i++
				dest = rest[i]
			} else if strings.HasPrefix(a, "--dest=") {
				dest = strings.SplitN(a, "=", 2)[1]
			}
		}
		if err := project.Vendor(cwd, dest); err != nil {
			fatal("vendor failed: %v", err)
		}

	case "version", "--version", "-v":
		fmt.Printf("weblisk v%s\n", version)

	// ── Server Commands ──────────────────────────────────────

	case "server":
		cwd, _ := os.Getwd()
		config.Load(cwd)
		if err := server.Handle(rest, cwd); err != nil {
			fatal("server: %v", err)
		}

	case "agent":
		cwd, _ := os.Getwd()
		config.Load(cwd)
		agentArgs, agentRoot := agent.ParseArgs(rest)
		if agentRoot == "." {
			agentRoot = cwd
		}
		if err := agent.Handle(agentArgs, agentRoot); err != nil {
			fatal("agent: %v", err)
		}

	case "domain":
		cwd, _ := os.Getwd()
		config.Load(cwd)
		if err := domain.Handle(rest, cwd); err != nil {
			fatal("domain: %v", err)
		}

	case "gateway":
		cwd, _ := os.Getwd()
		config.Load(cwd)
		if err := gateway.Handle(rest, cwd); err != nil {
			fatal("gateway: %v", err)
		}

	// ── Blueprint Commands ───────────────────────────────────

	case "blueprint", "blueprints":
		cwd, _ := os.Getwd()
		config.Load(cwd)
		if len(rest) > 0 && rest[0] == "update" {
			if err := dispatch.UpdateBlueprints(cwd); err != nil {
				fatal("blueprint update: %v", err)
			}
		} else {
			fmt.Print("\n  Usage: weblisk blueprint update\n\n")
		}

	case "validate":
		cwd, _ := os.Getwd()
		config.Load(cwd)
		if err := dispatch.Validate(cwd, rest); err != nil {
			fatal("validate: %v", err)
		}

	case "doctor":
		cwd, _ := os.Getwd()
		if err := doctor.Run(cwd); err != nil {
			if _, ok := err.(*doctor.WarningsOnly); ok {
				os.Exit(2)
			}
			fatal("doctor: %v", err)
		}

	case "secret", "secrets":
		cwd, _ := os.Getwd()
		config.Load(cwd)
		if err := secrets.Handle(rest, cwd); err != nil {
			fatal("secret: %v", err)
		}

	case "pattern":
		cwd, _ := os.Getwd()
		config.Load(cwd)
		if len(rest) >= 2 && rest[0] == "apply" {
			pattern := rest[1]
			resource := ""
			for i := 2; i < len(rest); i++ {
				if rest[i] == "--resource" && i+1 < len(rest) {
					i++
					resource = rest[i]
				} else if strings.HasPrefix(rest[i], "--resource=") {
					resource = strings.SplitN(rest[i], "=", 2)[1]
				}
			}
			if err := dispatch.PatternApply(cwd, pattern, resource); err != nil {
				fatal("pattern apply: %v", err)
			}
		} else {
			fmt.Print("\n  Usage: weblisk pattern apply <pattern-name> [--resource <target>]\n\n")
		}

	// ── Identity Commands ────────────────────────────────────

	case "operator":
		if err := operator.Handle(rest); err != nil {
			fatal("operator: %v", err)
		}

	// ── Marketplace Commands ─────────────────────────────────

	case "marketplace":
		cwd, _ := os.Getwd()
		config.Load(cwd)
		if err := marketplace.Handle(rest, cwd, version); err != nil {
			fatal("marketplace: %v", err)
		}

	// ── Operations Commands ──────────────────────────────────

	case "status":
		if err := admin.Status(rest); err != nil {
			fatal("status: %v", err)
		}

	case "agents":
		if len(rest) == 0 {
			rest = []string{"list"}
		}
		switch rest[0] {
		case "list":
			if err := admin.AgentsList(rest[1:]); err != nil {
				fatal("agents list: %v", err)
			}
		case "describe":
			if len(rest) < 2 {
				fatal("Usage: weblisk agents describe <name>")
			}
			if err := admin.AgentsDescribe(rest[1], rest[2:]); err != nil {
				fatal("agents describe: %v", err)
			}
		case "deregister":
			if len(rest) < 2 {
				fatal("Usage: weblisk agents deregister <name> --confirm")
			}
			if err := admin.AgentsDeregister(rest[1], rest[2:]); err != nil {
				fatal("agents deregister: %v", err)
			}
		default:
			fatal("Unknown agents command: %s\n  Try: weblisk agents list|describe|deregister", rest[0])
		}

	case "domains":
		if len(rest) == 0 {
			rest = []string{"list"}
		}
		switch rest[0] {
		case "list":
			if err := admin.DomainsList(rest[1:]); err != nil {
				fatal("domains list: %v", err)
			}
		case "describe":
			if len(rest) < 2 {
				fatal("Usage: weblisk domains describe <name>")
			}
			if err := admin.DomainsDescribe(rest[1], rest[2:]); err != nil {
				fatal("domains describe: %v", err)
			}
		default:
			fatal("Unknown domains command: %s\n  Try: weblisk domains list|describe", rest[0])
		}

	case "workflows":
		if len(rest) == 0 {
			rest = []string{"list"}
		}
		switch rest[0] {
		case "list":
			if err := admin.WorkflowsList(rest[1:]); err != nil {
				fatal("workflows list: %v", err)
			}
		case "describe":
			if len(rest) < 2 {
				fatal("Usage: weblisk workflows describe <id>")
			}
			if err := admin.WorkflowsDescribe(rest[1], rest[2:]); err != nil {
				fatal("workflows describe: %v", err)
			}
		default:
			fatal("Unknown workflows command: %s\n  Try: weblisk workflows list|describe", rest[0])
		}

	case "approvals":
		if len(rest) == 0 {
			rest = []string{"list"}
		}
		switch rest[0] {
		case "list":
			if err := admin.ApprovalsList(rest[1:]); err != nil {
				fatal("approvals list: %v", err)
			}
		case "describe", "show":
			if len(rest) < 2 {
				fatal("Usage: weblisk approvals describe <id>")
			}
			if err := admin.ApprovalsDescribe(rest[1], rest[2:]); err != nil {
				fatal("approvals describe: %v", err)
			}
		case "accept":
			if len(rest) < 2 {
				fatal("Usage: weblisk approvals accept <id...>")
			}
			ids := filterNonFlags(rest[1:])
			flags := filterFlags(rest[1:])
			if err := admin.ApprovalsAccept(ids, flags); err != nil {
				fatal("approvals accept: %v", err)
			}
		case "reject":
			if len(rest) < 2 {
				fatal("Usage: weblisk approvals reject <id> --reason <reason>")
			}
			id := rest[1]
			reason := ""
			for i := 2; i < len(rest); i++ {
				if rest[i] == "--reason" && i+1 < len(rest) {
					i++
					reason = rest[i]
				} else if strings.HasPrefix(rest[i], "--reason=") {
					reason = strings.SplitN(rest[i], "=", 2)[1]
				}
			}
			if err := admin.ApprovalsReject(id, reason, rest[2:]); err != nil {
				fatal("approvals reject: %v", err)
			}
		default:
			fatal("Unknown approvals command: %s\n  Try: weblisk approvals list|describe|accept|reject", rest[0])
		}

	case "strategies":
		if len(rest) == 0 {
			rest = []string{"list"}
		}
		switch rest[0] {
		case "list":
			if err := admin.StrategiesList(rest[1:]); err != nil {
				fatal("strategies list: %v", err)
			}
		case "describe":
			if len(rest) < 2 {
				fatal("Usage: weblisk strategies describe <id>")
			}
			if err := admin.StrategiesDescribe(rest[1], rest[2:]); err != nil {
				fatal("strategies describe: %v", err)
			}
		case "create":
			if err := admin.StrategiesCreate(rest[1:]); err != nil {
				fatal("strategies create: %v", err)
			}
		case "update":
			if len(rest) < 2 {
				fatal("Usage: weblisk strategies update <id> --priority <n> | --deadline <date>")
			}
			if err := admin.StrategiesUpdate(rest[1], rest[2:]); err != nil {
				fatal("strategies update: %v", err)
			}
		case "delete":
			if len(rest) < 2 {
				fatal("Usage: weblisk strategies delete <id>")
			}
			if err := admin.StrategiesDelete(rest[1], rest[2:]); err != nil {
				fatal("strategies delete: %v", err)
			}
		default:
			fatal("Unknown strategies command: %s\n  Try: weblisk strategies list|describe|create|update|delete", rest[0])
		}

	case "federations", "federation":
		if len(rest) == 0 {
			rest = []string{"peers"}
		}
		switch rest[0] {
		case "peers":
			if err := admin.FederationPeers(rest[1:]); err != nil {
				fatal("federations peers: %v", err)
			}
		case "pending":
			if err := admin.FederationPending(rest[1:]); err != nil {
				fatal("federations pending: %v", err)
			}
		case "accept":
			if len(rest) < 2 {
				fatal("Usage: weblisk federations accept <id>")
			}
			if err := admin.FederationAccept(rest[1], rest[2:]); err != nil {
				fatal("federations accept: %v", err)
			}
		case "reject":
			if len(rest) < 2 {
				fatal("Usage: weblisk federations reject <id>")
			}
			if err := admin.FederationReject(rest[1], rest[2:]); err != nil {
				fatal("federations reject: %v", err)
			}
		case "revoke":
			if len(rest) < 2 {
				fatal("Usage: weblisk federations revoke <peer> --confirm")
			}
			if err := admin.FederationRevoke(rest[1], rest[2:]); err != nil {
				fatal("federations revoke: %v", err)
			}
		case "describe":
			if len(rest) < 2 {
				fatal("Usage: weblisk federations describe <name>")
			}
			if err := admin.FederationsDescribe(rest[1], rest[2:]); err != nil {
				fatal("federations describe: %v", err)
			}
		case "contracts":
			if err := admin.FederationsContracts(rest[1:]); err != nil {
				fatal("federations contracts: %v", err)
			}
		default:
			fatal("Unknown federations command: %s\n  Try: weblisk federations peers|pending|accept|reject|revoke|describe|contracts", rest[0])
		}

	case "audit":
		if err := admin.Audit(rest); err != nil {
			fatal("audit: %v", err)
		}

	// ── Operators Commands ───────────────────────────────────

	case "operators":
		if len(rest) == 0 {
			rest = []string{"list"}
		}
		switch rest[0] {
		case "list":
			if err := admin.OperatorsList(rest[1:]); err != nil {
				fatal("operators list: %v", err)
			}
		case "describe":
			if len(rest) < 2 {
				fatal("Usage: weblisk operators describe <name>")
			}
			if err := admin.OperatorsDescribe(rest[1], rest[2:]); err != nil {
				fatal("operators describe: %v", err)
			}
		case "revoke":
			if len(rest) < 2 {
				fatal("Usage: weblisk operators revoke <name> --confirm")
			}
			if err := admin.OperatorsRevoke(rest[1], rest[2:]); err != nil {
				fatal("operators revoke: %v", err)
			}
		case "role":
			if len(rest) < 2 {
				fatal("Usage: weblisk operators role <name> <role>")
			}
			role := ""
			flags := rest[2:]
			// Try positional role first (spec: `weblisk operators role bob operator`)
			for i := 0; i < len(flags); i++ {
				if flags[i] == "--role" && i+1 < len(flags) {
					i++
					role = flags[i]
				} else if strings.HasPrefix(flags[i], "--role=") {
					role = strings.SplitN(flags[i], "=", 2)[1]
				} else if !strings.HasPrefix(flags[i], "-") && role == "" {
					role = flags[i]
				}
			}
			if role == "" {
				fatal("Usage: weblisk operators role <name> <role>")
			}
			if err := admin.OperatorsRole(rest[1], role, flags); err != nil {
				fatal("operators role: %v", err)
			}
		default:
			fatal("Unknown operators command: %s\n  Try: weblisk operators list|describe|revoke|role", rest[0])
		}

	// ── Observations Commands ────────────────────────────────

	case "observations":
		if len(rest) == 0 {
			rest = []string{"list"}
		}
		switch rest[0] {
		case "list":
			if err := admin.ObservationsList(rest[1:]); err != nil {
				fatal("observations list: %v", err)
			}
		case "trends":
			if err := admin.ObservationsTrends(rest[1:]); err != nil {
				fatal("observations trends: %v", err)
			}
		default:
			fatal("Unknown observations command: %s\n  Try: weblisk observations list|trends", rest[0])
		}

	// ── Test Commands ────────────────────────────────────────

	case "test":
		if err := test.Handle(rest); err != nil {
			fatal("test: %v", err)
		}

	// ── Deploy Commands ──────────────────────────────────────

	case "deploy":
		if err := deploy.Handle(rest); err != nil {
			fatal("deploy: %v", err)
		}

	// ── Deps Commands ────────────────────────────────────────

	case "deps":
		cwd, _ := os.Getwd()
		if err := deps.Handle(rest, cwd); err != nil {
			fatal("deps: %v", err)
		}

	// ── Policy Commands ──────────────────────────────────────

	case "policy":
		cwd, _ := os.Getwd()
		if err := policy.Handle(rest, cwd); err != nil {
			fatal("policy: %v", err)
		}

	// ── Update (framework files) ─────────────────────────────

	case "update":
		cwd, _ := os.Getwd()
		config.Load(cwd)
		if err := project.UpdateFramework(cwd, version); err != nil {
			fatal("update failed: %v", err)
		}

	case "help", "--help", "-h":
		printHelp()

	default:
		fmt.Fprintf(os.Stderr, "  Unknown command: %s\n\n", cmd)
		printHelp()
		os.Exit(1)
	}
}

// Arg parsers

func parseNewArgs(args []string) (string, []string, bool, string) {
	var name, lib string
	var templates []string
	local := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--local" {
			local = true
		} else if strings.HasPrefix(a, "--template") {
			if strings.Contains(a, "=") {
				templates = append(templates, strings.SplitN(a, "=", 2)[1])
			} else if i+1 < len(args) {
				i++
				templates = append(templates, args[i])
			}
		} else if strings.HasPrefix(a, "--lib") {
			if strings.Contains(a, "=") {
				lib = strings.SplitN(a, "=", 2)[1]
			} else if i+1 < len(args) {
				i++
				lib = args[i]
			}
		} else if !strings.HasPrefix(a, "-") {
			name = a
		}
	}
	if len(templates) == 0 {
		templates = []string{"default"}
	}
	return name, templates, local, lib
}

func parseBuildArgs(args []string) (string, build.Options) {
	root := "."
	var opts build.Options
	for _, a := range args {
		switch a {
		case "--minify":
			opts.Minify = true
		case "--fingerprint":
			opts.Fingerprint = true
		default:
			if !strings.HasPrefix(a, "-") {
				root = a
			}
		}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	return abs, opts
}

func parseDevArgs(args []string) (string, int) {
	root := "."
	port := config.Resolve().Port
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--port" && i+1 < len(args) {
			i++
			if p, err := strconv.Atoi(args[i]); err == nil {
				port = p
			}
		} else if strings.HasPrefix(a, "--port=") {
			if p, err := strconv.Atoi(strings.SplitN(a, "=", 2)[1]); err == nil {
				port = p
			}
		} else if !strings.HasPrefix(a, "-") {
			root = a
		}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	return abs, port
}

func filterNonFlags(args []string) []string {
	var result []string
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			result = append(result, a)
		}
	}
	return result
}

func filterFlags(args []string) []string {
	var result []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			result = append(result, args[i])
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && strings.Contains(args[i], "=") == false {
				i++
				result = append(result, args[i])
			}
		}
	}
	return result
}

// Help text

func printHelp() {
	fmt.Print(`
  Weblisk CLI v` + version + `

  Usage:
    weblisk <command> [options]

  Project:
    new <name>              Scaffold a new project from templates
      --template <t>        Template: blog, dashboard, docs (default: default)
      --local               Include framework files locally instead of CDN
      --lib <path>          Local framework path (default: lib/weblisk)
    dev [root]              Start development server with live reload
      --port <n>            Port number (default: 3000)
    build [root]            Production build — minify, fingerprint, optimize
      --minify              Minify HTML, CSS, and JS
      --fingerprint         Hash asset filenames for cache busting
    vendor                  Download framework files into project
      --dest <path>         Destination directory (default: lib/weblisk)
    version                 Print CLI version
    update                  Re-download framework modules (--local projects)

  Code Generation:
    server init             Generate orchestrator via AI
      --platform <p>        Target: go (default), cloudflare
    server start            Build and start the orchestrator
    server verify           Verify a running orchestrator
      --url <url>           Orchestrator URL (default: http://localhost:9800)

    agent create <name>     Generate a work agent via AI
      --platform <p>        Target: go (default), cloudflare
    agent start <name>      Build and start an agent
      --port <n>            Listen port (default: auto)
      --orch <url>          Orchestrator URL
    agent verify            Verify a running agent
    agent list              List generated agents

    domain create <name>    Generate a domain controller via AI
      --platform <p>        Target: go (default), cloudflare
    domain start <name>     Build and run a domain controller

    gateway create          Generate the application gateway via AI
      --platform <p>        Target: go (default), cloudflare
    gateway start           Build and run the gateway

  Blueprints:
    blueprint update        Re-fetch all blueprint sources
    validate                Validate blueprint compliance
    pattern apply <name>    Apply a cross-cutting pattern via AI
      --resource <target>   Target resource
    doctor                  Validate project health and configuration

  Secrets:
    secret list             Show all secrets and status
    secret set <a> <key>    Set a secret value (prompts interactively)
    secret get <a> <key>    Print secret value (--confirm required)
    secret delete <a> <k>   Remove a secret
    secret rotate <a> <k>   Rotate a secret value

  Identity:
    operator init           Generate Ed25519 operator key pair
      --name <name>         Operator name (default: system username)
      --force               Regenerate keys
    operator register       Register with an orchestrator
      --orch <url>          Orchestrator URL
      --role <role>         Request a specific role
    operator token          Inspect or refresh operator token
      --refresh             Force refresh
    operator rotate         Rotate key pair (re-registers with orchestrator)

  Marketplace:
    marketplace search <q>  Search marketplace listings
    marketplace describe <id> Full listing detail
    marketplace buy <id>    Purchase a listing
    marketplace install <id> Download an installable asset
    marketplace list        List active purchases and subscriptions
    marketplace publish     Publish a capability or asset
    marketplace update <id> Update a published listing
    marketplace delist <id> Remove a listing
    marketplace dashboard   View seller metrics
    marketplace reviews <id> View reviews for a listing
    marketplace review <id> Leave a review
    marketplace collaborations  List active collaborations
    marketplace usage <id>  View usage metrics
    marketplace terminate <id> Terminate a collaboration

  Operations (requires operator registration):
    status                  System overview
    agents                  List registered agents
    agents describe <name>  Agent detail
    agents deregister <n>   Force-deregister an agent
    domains                 List domain controllers
    domains describe <name> Domain detail
    workflows               List recent workflow executions
    workflows describe <id> Workflow execution detail
    approvals               List pending recommendations
    approvals describe <id> Recommendation detail
    approvals accept <ids>  Accept recommendations
    approvals reject <id>   Reject a recommendation (--reason required)
    strategies              List strategies
    strategies describe <id> Strategy detail
    strategies create       Create a strategy (--json for stdin)
    strategies update <id>  Update a strategy
    strategies delete <id>  Delete a strategy
    operators               List registered operators
    operators describe <n>  Operator detail
    operators revoke <n>    Revoke an operator (--confirm required)
    operators role <n>      Change operator role (--role required)
    observations            List system observations
    observations trends     Show observation trends
    federations peers       List federation peers
    federations pending     List pending peering requests
    federations accept <id> Accept a peering request
    federations reject <id> Reject a peering request
    federations revoke <p>  Revoke peer trust (--confirm required)
    federations describe <n> Peer detail
    federations contracts   List active federation contracts
    audit                   Query the audit log
      --actor <name>        Filter by actor
      --action <type>       Filter by action
      --since <duration>    Filter by time
      --follow              Tail the log

  Testing & Compliance:
    test conformance        Run blueprint conformance tests (L1/L2/L3)
      --level <n>           Run only level n tests
    test mock-orchestrator  Start a mock orchestrator for local dev
      --port <n>            Port (default: 9800)

  Deployment:
    deploy rollback         Rollback to previous deployment
      --version <v>         Target version

  Dependencies:
    deps audit              Audit lockfile integrity and packages

  Policy:
    policy validate         Validate policies.yaml
    policy test             Run policy test assertions

  Environment (.env):
    WL_ORIGIN             Production origin URL (for sitemap)
    WL_PORT               Dev server port (default: 3000)
    WL_DIST               Output directory (default: dist)
    WL_CDN                CDN base URL (rewrites importmaps on build)
    WL_LIB                Local framework path (default: lib/weblisk)
    WL_ORCH               Orchestrator URL (default: http://localhost:9800)
    WL_BLUEPRINT_SOURCES  Additional blueprint repo URLs (comma-separated)
    WL_TEMPLATE_SOURCES   Additional template repo URLs (comma-separated)
    WL_AI_PROVIDER        AI backend: openai, ollama, anthropic, cloudflare
    WL_AI_MODEL           Model name (provider-specific default)
    WL_AI_BASE_URL        Endpoint override
    WL_AI_KEY             API key

  The CLI carries the blueprint — your AI model writes the code.
  Configure WL_AI_PROVIDER to get started with code generation.
`)
}

// ── Exit Codes ──────────────────────────────────────────────

const (
	exitOK         = 0
	exitError      = 1
	exitAuth       = 2
	exitConnection = 3
	exitNotFound   = 4
	exitPermission = 5
)

func fatal(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	fmt.Fprintf(os.Stderr, "  Error: %s\n", msg)
	os.Exit(classifyExit(msg))
}

func classifyExit(msg string) int {
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "authentication failed"),
		strings.Contains(lower, "session expired"),
		strings.Contains(lower, "no token"),
		strings.Contains(lower, "token expired"),
		strings.Contains(lower, "unauthorized"):
		return exitAuth
	case strings.Contains(lower, "connection failed"),
		strings.Contains(lower, "connection refused"),
		strings.Contains(lower, "orchestrator running"),
		strings.Contains(lower, "no such host"),
		strings.Contains(lower, "timeout"):
		return exitConnection
	case strings.Contains(lower, "not found"),
		strings.Contains(lower, "no such file"):
		return exitNotFound
	case strings.Contains(lower, "permission denied"),
		strings.Contains(lower, "forbidden"),
		strings.Contains(lower, "access denied"):
		return exitPermission
	default:
		return exitError
	}
}
