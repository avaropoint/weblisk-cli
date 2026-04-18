package protocol

// Protocol Types
//
// All types for the Weblisk Agent Protocol v1. Every agent and
// orchestrator speaks this exact contract — no exceptions.

// Path Constants

const PathPrefix = "/v1"

const (
	PathDescribe = PathPrefix + "/describe"
	PathExecute  = PathPrefix + "/execute"
	PathHealth   = PathPrefix + "/health"
	PathMessage  = PathPrefix + "/message"
	PathServices = PathPrefix + "/services"
	PathRegister = PathPrefix + "/register"
	PathTask     = PathPrefix + "/task"
	PathChannel  = PathPrefix + "/channel"
	PathAudit    = PathPrefix + "/audit"
)

// Agent Types

// AgentManifest describes an agent's capabilities.
type AgentManifest struct {
	Name         string       `json:"name"`
	Version      string       `json:"version"`
	Description  string       `json:"description"`
	URL          string       `json:"url,omitempty"`
	Capabilities []Capability `json:"capabilities"`
	Inputs       []IOSpec     `json:"inputs,omitempty"`
	Outputs      []IOSpec     `json:"outputs,omitempty"`
	PublicKey     string      `json:"public_key"`
}

// Capability is a single thing an agent can do.
type Capability struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Resources   []string `json:"resources,omitempty"`
}

// IOSpec describes an input or output parameter.
type IOSpec struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required,omitempty"`
}

// Task Types

// TaskRequest is what the orchestrator sends to an agent.
type TaskRequest struct {
	ID          string            `json:"id"`
	Type        string            `json:"type"`
	Payload     map[string]any    `json:"payload"`
	Context     *TaskContext      `json:"context,omitempty"`
	Signature   string            `json:"signature,omitempty"`
	Token       string            `json:"token,omitempty"`
}

// TaskContext provides additional context for task execution.
type TaskContext struct {
	Origin      string            `json:"origin,omitempty"`
	Workspace   string            `json:"workspace,omitempty"`
	Constraints map[string]string `json:"constraints,omitempty"`
}

// TaskResult is what an agent returns after execution.
type TaskResult struct {
	ID       string           `json:"id"`
	Status   string           `json:"status"` // "success", "error", "partial"
	Output   map[string]any   `json:"output,omitempty"`
	Changes  []ProposedChange `json:"changes,omitempty"`
	Error    string           `json:"error,omitempty"`
}

// ProposedChange is a file change an agent wants to make.
type ProposedChange struct {
	Path    string     `json:"path"`
	Action  string     `json:"action"` // "create", "modify", "delete"
	Content string     `json:"content,omitempty"`
	Diff    *ChangeDiff `json:"diff,omitempty"`
}

// ChangeDiff is a before/after diff for a modification.
type ChangeDiff struct {
	Before string `json:"before"`
	After  string `json:"after"`
}

// Messaging Types

// AgentMessage is a message between agents or from orchestrator.
type AgentMessage struct {
	ID        string         `json:"id"`
	From      string         `json:"from"`
	To        string         `json:"to"`
	Type      string         `json:"type"`
	Action    string         `json:"action,omitempty"`
	Body      string         `json:"body,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
	Timestamp int64          `json:"timestamp"`
	Signature string         `json:"signature,omitempty"`
}

// Service Discovery Types

// ServiceEntry describes a registered agent in the directory.
type ServiceEntry struct {
	Name      string   `json:"name"`
	URL       string   `json:"url"`
	PublicKey string   `json:"public_key"`
	Caps      []string `json:"capabilities"`
	Status    string   `json:"status,omitempty"`
}

// ServiceDirectory is the full list of registered agents.
type ServiceDirectory struct {
	Services  []ServiceEntry `json:"services"`
	UpdatedAt int64          `json:"updated_at,omitempty"`
}

// Registration Types

// RegisterRequest is what an agent sends to register.
type RegisterRequest struct {
	Manifest  AgentManifest `json:"manifest"`
	Signature string        `json:"signature"`
	Timestamp int64         `json:"timestamp"`
}

// RegisterResponse is what the orchestrator returns.
type RegisterResponse struct {
	OK      bool   `json:"ok"`
	AgentID string `json:"agent_id,omitempty"`
	Token   string `json:"token,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Orchestrator Types

// OrchestratorInfo is returned by the orchestrator health endpoint.
type OrchestratorInfo struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	PublicKey string `json:"public_key"`
	Agents    int    `json:"agents"`
	Uptime    int64  `json:"uptime"`
}

// Channel Types

// ChannelRequest asks to open a direct channel between agents.
type ChannelRequest struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Token string `json:"token"`
}

// ChannelGrant is the orchestrator's response to a channel request.
type ChannelGrant struct {
	OK      bool   `json:"ok"`
	Channel string `json:"channel,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Audit Types

// HealthStatus is returned by the health check endpoint.
type HealthStatus struct {
	Status    string `json:"status"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Uptime    int64  `json:"uptime,omitempty"`
}

// AuditEntry is a single audit log record.
type AuditEntry struct {
	Timestamp int64  `json:"timestamp"`
	Agent     string `json:"agent"`
	Action    string `json:"action"`
	TaskID    string `json:"task_id,omitempty"`
	Status    string `json:"status"`
	Details   string `json:"details,omitempty"`
}
