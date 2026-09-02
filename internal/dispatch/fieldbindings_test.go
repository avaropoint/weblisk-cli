package dispatch

import (
	"sort"
	"strings"
	"testing"
)

// knownFieldBindingFaults are bindings that name a field their type does not
// define, recorded so the corpus can be ratcheted rather than frozen.
//
// Every entry is a real fault: generation is told to implement a field the
// protocol has no name for, and the model complies. They are pinned rather than
// fixed in bulk because the fix is a design decision per type — TaskRequest
// carries `from` and no `target_agent`, so twenty blueprints either mean `to`,
// or the type is missing a field, and guessing at scale would encode the wrong
// answer twenty times.
//
// Three classes were fixed rather than pinned, because each had already caused
// a failure in generated code:
//
//	ErrorResponse    message         → error            (25 blueprints)
//	HealthStatus     component       → name             (7 blueprints)
//	HealthStatus     uptime_seconds  → uptime
//	HealthStatus     details         → checks           (6 blueprints)
//	ServiceDirectory agents          → services         (5 blueprints)
var knownFieldBindingFaults = map[string]bool{
	"agents/alerting.md AgentManifest port":                                                       true,
	"agents/alerting.md EventEnvelope action,from,to":                                             true,
	"agents/alerting.md TaskRequest target_agent":                                                 true,
	"agents/cron.md AgentManifest port":                                                           true,
	"agents/cron.md EventEnvelope action,from,to":                                                 true,
	"agents/cron.md TaskRequest target_agent":                                                     true,
	"agents/email-send.md AgentManifest port":                                                     true,
	"agents/email-send.md EventEnvelope action,from,to":                                           true,
	"agents/email-send.md TaskRequest target_agent":                                               true,
	"agents/health-monitor.md AgentManifest port":                                                 true,
	"agents/health-monitor.md EventEnvelope action,from,to":                                       true,
	"agents/health-monitor.md TaskRequest target_agent":                                           true,
	"agents/hub.md EventEnvelope action,from,to":                                                  true,
	"agents/hub.md FederatedTaskResult data_bytes_in,data_bytes_out,latency_ms,listing_id":        true,
	"agents/hub.md ListingEntry capability,listing_id,metrics,pricing,provider,signature,tier":    true,
	"agents/sync.md AgentManifest port":                                                           true,
	"agents/sync.md EventEnvelope action,from,to":                                                 true,
	"agents/sync.md TaskRequest target_agent":                                                     true,
	"agents/task.md EventEnvelope action,from,to":                                                 true,
	"agents/task.md TaskRequest target_agent":                                                     true,
	"agents/webhook.md AgentManifest port":                                                        true,
	"agents/webhook.md EventEnvelope action,from,to":                                              true,
	"agents/webhook.md TaskRequest target_agent":                                                  true,
	"agents/workflow.md EventEnvelope action,from,to":                                             true,
	"agents/workflow.md TaskRequest target_agent":                                                 true,
	"architecture/admin.md DomainManifest name,type":                                              true,
	"architecture/admin.md GatewayConfig routes":                                                  true,
	"architecture/agent.md TaskRequest task_id":                                                   true,
	"architecture/change-management.md AgentManifest requires":                                    true,
	"architecture/client.md GatewayConfig client_config,csrf_config,session_config":               true,
	"architecture/content.md ScopeDeclaration context":                                            true,
	"architecture/data-security.md GatewayConfig internal_headers,response_sanitization":          true,
	"architecture/domain.md TaskRequest task_id":                                                  true,
	"architecture/enforcement.md ApprovalRequest agent,authority_required,context,operation":      true,
	"architecture/enforcement.md GatewayConfig internal_headers":                                  true,
	"architecture/enforcement.md PolicyDecision enforcement,matched_rules,result":                 true,
	"architecture/gateway.md TaskRequest input":                                                   true,
	"architecture/gateway.md TaskResult output":                                                   true,
	"architecture/lifecycle.md TaskResult output":                                                 true,
	"architecture/observability.md TaskResult duration_ms":                                        true,
	"architecture/orchestrator.md AgentEntry last_seen":                                           true,
	"architecture/orchestrator.md ChannelGrant target_public_key,token":                           true,
	"architecture/storage.md AgentEntry AgentID,Manifest,Status,Token":                            true,
	"architecture/storage.md EntityContext entity_name,entity_type":                               true,
	"architecture/storage.md WorkflowExecution workflow":                                          true,
	"architecture/testing.md ChannelGrant token":                                                  true,
	"architecture/testing.md TaskRequest input":                                                   true,
	"architecture/testing.md TaskResult output":                                                   true,
	"architecture/threat-model.md ClientRecord binding_hash,capabilities,client_type,trust_level": true,
	"architecture/threat-model.md RouteTable auth,rate_limit":                                     true,
	"patterns/alerting.md TaskRequest target_agent":                                               true,
	"patterns/api-ai.md Message content,role,tool_call_id":                                        true,
	"patterns/api-ai.md Usage completion_tokens,prompt_tokens,total_tokens":                       true,
	"patterns/approval.md PolicyDecision enforcement,matched_rules,result":                        true,
	"patterns/auth-session.md ClientRecord client_type,session_id,trust_level":                    true,
	"patterns/command.md LogEntry details":                                                        true,
	"patterns/domain-controller.md DomainManifest capabilities,name,type,version":                 true,
	"patterns/domain-controller.md Feedback after,before,metric":                                  true,
	"patterns/domain-controller.md Observation element,value":                                     true,
	"patterns/governance.md ApprovalRequest authority_level,severity":                             true,
	"patterns/interop.md AgentState health,uptime":                                                true,
	"patterns/interop.md TaskRequest task":                                                        true,
	"patterns/interop.md TaskResult data,error":                                                   true,
	"patterns/messaging.md AgentManifest namespace":                                               true,
	"patterns/migration.md IntentDecision decided_at,gate,matched_policies,result":                true,
	"patterns/migration.md OperationIntent justification,resource_id,resource_type":               true,
	"patterns/notification.md SecretDeclaration description,key,required,rotation":                true,
	"patterns/observability.md AgentState health,uptime":                                          true,
	"patterns/offline.md DataBoundaryTag encryption_required,offline_permitted,scope,ttl":         true,
	"patterns/privacy.md ScopeDeclaration context,custom_labels,propagation_rule":                 true,
	"patterns/rate-limiting.md AgentConfig agent_rate_limits":                                     true,
	"patterns/rate-limiting.md GatewayConfig rate_limiting,routes,trusted_proxies":                true,
	"patterns/realtime-chat.md ChatMessage channel,content,from,id,metadata,timestamp":            true,
	"patterns/realtime-chat.md GatewayConfig routes,websocket":                                    true,
	"patterns/realtime-chat.md WebSocketFrame type":                                               true,
	"patterns/safety.md PolicyDecision enforcement,matched_rules,result":                          true,
	"patterns/secrets.md AgentConfig secrets":                                                     true,
	"patterns/task-dispatch.md TaskRequest target_agent":                                          true,
	"patterns/user-management.md PaginatedResponse data,pagination":                               true,
	"patterns/user-management.md Session csrf_token,expires_at,user_id":                           true,
	"patterns/versioning.md AgentContext agent_name,version":                                      true,
	"platforms/cloudflare.md AgentManifest actions":                                               true,
	"platforms/cloudflare.md TaskRequest task_id":                                                 true,
	"platforms/go.md AgentManifest actions":                                                       true,
	"platforms/go.md TaskRequest task_id":                                                         true,
	"platforms/node.md AgentManifest actions":                                                     true,
	"platforms/node.md TaskRequest task_id":                                                       true,
	"platforms/rust.md AgentManifest actions":                                                     true,
	"platforms/rust.md TaskRequest task_id":                                                       true,
}

func TestNoNewBindingNamesAFieldItsTypeDoesNotDefine(t *testing.T) {
	bps := readBlueprints(t, []string{"architecture", "protocol", "patterns", "agents", "platforms", "schemas"})
	if len(TypeFields(bps)) < 50 {
		t.Fatalf("only %d types indexed — the extractor is not reading the corpus", len(TypeFields(bps)))
	}

	faults := CheckFieldBindings(bps)
	var novel []string
	for _, f := range faults {
		if !knownFieldBindingFaults[f.Key()] {
			novel = append(novel, f.Key())
		}
	}
	sort.Strings(novel)
	for _, n := range novel {
		t.Errorf("binding names a field its type does not define: %s\n"+
			"  Generation reads fields_used to decide what to implement, so this asks the\n"+
			"  model for a field the protocol has no name for — and it complies.", n)
	}
}

// A pinned fault that has been fixed must leave the list, or the list stops
// describing the corpus and starts hiding it.
func TestPinnedFieldBindingFaultsStillExist(t *testing.T) {
	bps := readBlueprints(t, []string{"architecture", "protocol", "patterns", "agents", "platforms", "schemas"})
	live := map[string]bool{}
	for _, f := range CheckFieldBindings(bps) {
		live[f.Key()] = true
	}
	for pinned := range knownFieldBindingFaults {
		if !live[pinned] {
			t.Errorf("%q is pinned and no longer exists — remove it from knownFieldBindingFaults", pinned)
		}
	}
}

// The three fixed classes must stay fixed.
func TestTheFieldNamesThatBrokeGeneratedCodeAreCorrect(t *testing.T) {
	bps := readBlueprints(t, []string{"architecture", "protocol", "patterns", "agents", "platforms", "schemas"})
	types := TypeFields(bps)

	for _, c := range []struct{ typ, wrong, right string }{
		{"ErrorResponse", "message", "error"},
		{"HealthStatus", "component", "name"},
		{"HealthStatus", "uptime_seconds", "uptime"},
		{"ServiceDirectory", "agents", "services"},
	} {
		if !types[c.typ][c.right] {
			t.Errorf("%s should define %q and does not", c.typ, c.right)
		}
		if types[c.typ][c.wrong] {
			t.Errorf("%s defines %q — the wrong name was added to the type instead of "+
				"correcting the bindings", c.typ, c.wrong)
		}
	}
	for _, f := range CheckFieldBindings(bps) {
		for _, bad := range []string{"message", "uptime_seconds"} {
			if (f.Type == "ErrorResponse" || f.Type == "HealthStatus") &&
				strings.Contains(strings.Join(f.Fields, ","), bad) {
				t.Errorf("%s still binds %q from %s", f.Blueprint, bad, f.Type)
			}
		}
	}
}
