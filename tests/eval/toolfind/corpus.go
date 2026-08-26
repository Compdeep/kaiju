// Package toolfind_live holds the corpus that tool ranking is measured against,
// and the two measurements taken over it.
//
// The corpus lives here rather than beside the code because both measurements
// read it: one runs on words alone and costs nothing, the other runs with a
// real embedding endpoint and costs money. Two copies of a corpus drift, and a
// threshold compared against a drifted copy says nothing.
package toolfind_live

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
	"github.com/Compdeep/kaiju/agent/toolfind"
)

// Case is one objective and the tool that has to be found for it.
type Case struct {
	Objective string // what someone asks for, in their words
	Want      string // the tool that answers it
}

// fakeTool is a registered tool with nothing behind it. The ranking reads a
// name, a description and a parameter schema and never calls anything.
type fakeTool struct {
	name, desc string
	params     json.RawMessage
}

func (f *fakeTool) Name() string                { return f.name }
func (f *fakeTool) Description() string         { return f.desc }
func (f *fakeTool) Parameters() json.RawMessage { return f.params }
func (*fakeTool) Impact(map[string]any) int     { return 0 }
func (*fakeTool) RequiresTarget() bool          { return false }
func (*fakeTool) Execute(context.Context, map[string]any) (string, error) {
	return "", nil
}

// PositionOf is where a tool came in a ranking, or -1.
func PositionOf(names []string, want string) int {
	for i, n := range names {
		if n == want {
			return i
		}
	}
	return -1
}

// Recall measures how often the wanted tool placed inside each of at.
func Recall(t *testing.T, ix toolfind.Index, at []int, log bool) map[int]float64 {
	t.Helper()
	hits := map[int]int{}
	for _, c := range Corpus {
		pos := PositionOf(ix.Rank(context.Background(), c.Objective), c.Want)
		if log {
			t.Logf("%4d  %-34s  %s", pos, c.Want, c.Objective)
		}
		for _, k := range at {
			if pos >= 0 && pos < k {
				hits[k]++
			}
		}
	}
	out := map[int]float64{}
	for _, k := range at {
		out[k] = float64(hits[k]) / float64(len(Corpus))
	}
	return out
}

// EnterpriseRegistry builds a registry of roughly n distinct connector tools.
//
// Distinct matters. An earlier version reached a thousand by repeating fifteen
// systems under numbered names, which meant seven interchangeable copies of
// every tool competed for the same objective and only one of them counted as
// found. That measured the generator, not the ranking.
func EnterpriseRegistry(t testing.TB, n int) *toolapi.Registry {
	t.Helper()
	// The systems the corpus below asks for, with their real operations.
	named := map[string][]string{
		"jira":       {"create_issue", "transition_issue", "comment_issue", "assign_issue", "search_issues", "link_issues", "create_sprint", "close_sprint", "list_boards", "log_work"},
		"servicenow": {"open_case", "resolve_case", "escalate_case", "search_cmdb", "create_change", "approve_change", "list_cases", "attach_evidence", "assign_group", "close_case"},
		"workday":    {"absence_balance", "request_time_off", "worker_profile", "org_chart", "submit_expense", "payroll_summary", "list_positions", "start_onboarding", "compensation_history", "update_address"},
		"salesforce": {"create_opportunity", "update_opportunity", "search_accounts", "create_lead", "convert_lead", "log_activity", "list_contacts", "forecast_summary", "close_won", "attach_quote"},
		"stripe":     {"refund_charge", "create_invoice", "list_payouts", "cancel_subscription", "retry_payment", "search_customers", "dispute_evidence", "create_coupon", "balance_summary", "void_invoice"},
		"s3":         {"put_object", "get_object", "list_objects", "delete_object", "presign_url", "copy_object", "set_lifecycle", "bucket_size", "restore_archive", "sync_prefix"},
		"snowflake":  {"run_query", "list_warehouses", "resume_warehouse", "grant_role", "table_schema", "copy_into", "query_history", "clone_database", "suspend_warehouse", "storage_usage"},
		"datadog":    {"query_metrics", "list_monitors", "mute_monitor", "create_dashboard", "search_logs", "trace_summary", "downtime_schedule", "list_cases", "slo_status", "host_map"},
		"pagerduty":  {"ack_page", "resolve_page", "list_oncall", "escalate_page", "create_schedule", "override_oncall", "page_timeline", "notify_team", "list_services", "snooze_page"},
		"github":     {"open_pull_request", "merge_pull_request", "create_issue", "list_workflow_runs", "rerun_workflow", "review_comment", "protect_branch", "list_releases", "compare_commits", "search_code"},
		"okta":       {"suspend_user", "reset_password", "list_groups", "assign_app", "list_factors", "end_session", "create_user", "deactivate_user", "list_policies", "audit_logins"},
		"zendesk":    {"create_ticket", "reply_ticket", "list_views", "set_priority", "merge_tickets", "search_tickets", "add_tag", "close_ticket", "satisfaction_score", "assign_agent"},
		"confluence": {"create_page", "update_page", "search_content", "attach_file", "list_spaces", "export_pdf", "page_history", "restrict_page", "move_page", "list_children"},
		"netsuite":   {"create_purchase_order", "approve_invoice", "list_vendors", "post_journal", "close_period", "run_report", "list_subsidiaries", "vendor_balance", "create_credit_memo", "reconcile_account"},
		"terraform":  {"plan_workspace", "apply_run", "list_workspaces", "state_show", "lock_workspace", "list_runs", "cancel_run", "variable_set", "policy_check", "destroy_workspace"},
	}
	// The rest of the registry: other systems a business runs, with operations
	// drawn from a shared vocabulary. They are the noise a real search has to
	// rank past, and they share words with the objectives on purpose.
	others := []string{"marketo", "asana", "monday", "notion", "airtable", "box", "dropbox",
		"sharepoint", "teams", "zoom", "opsgenie", "splunk", "newrelic", "sentry", "jenkins",
		"circleci", "gitlab", "bitbucket", "looker", "tableau", "powerbi", "sap", "coupa",
		"ariba", "concur", "docusign", "twilio", "sendgrid", "hubspot", "intercom", "front",
		"freshdesk", "bamboohr", "greenhouse", "lever", "expensify", "ramp", "brex", "gusto",
		"rippling", "mongodb", "postgres", "redis", "kafka", "airflow", "dbt", "fivetran"}
	ops := []string{"list_records", "get_record", "create_record", "update_record",
		"delete_record", "search_records", "export_report", "run_report", "list_users",
		"assign_owner", "add_comment", "attach_file", "set_status", "list_webhooks",
		"create_webhook", "list_teams", "notify_channel", "schedule_job", "cancel_job",
		"list_jobs", "job_history", "check_health", "list_pages", "acknowledge_page",
		"archive_item"}

	params := json.RawMessage(`{"type":"object","properties":{
		"id":{"type":"string","description":"the record identifier"},
		"query":{"type":"string","description":"a filter expression"},
		"limit":{"type":"integer","description":"how many to return"}}}`)

	reg := toolapi.NewRegistry()
	add := func(system, op string) bool {
		if len(reg.List()) >= n {
			return false
		}
		desc := fmt.Sprintf("%s in %s.", strings.ReplaceAll(op, "_", " "), system)
		_ = reg.RegisterWithSource(&fakeTool{name: system + "_" + op, desc: desc, params: params}, system)
		return true
	}
	systems := make([]string, 0, len(named))
	for s := range named {
		systems = append(systems, s)
	}
	sort.Strings(systems) // a registry that is the same twice
	for _, s := range systems {
		for _, op := range named[s] {
			add(s, op)
		}
	}
	for _, s := range others {
		for _, op := range ops {
			if !add(s, op) {
				return reg
			}
		}
	}
	return reg
}

// The corpus: what someone asks for, and the tool that has to be found.
var Corpus = []Case{
	{"the deploy is broken, stop the pager going off for the payments team", "pagerduty_ack_page"},
	{"how many holiday days does this employee have left", "workday_absence_balance"},
	{"give the customer their money back for the duplicate charge", "stripe_refund_charge"},
	{"put this report file in the reports bucket", "s3_put_object"},
	{"move the bug to done now that it is fixed", "jira_transition_issue"},
	{"someone left the company, cut off their access", "okta_deactivate_user"},
	{"the database is slow, what queries ran in the last hour", "snowflake_query_history"},
	{"raise a ticket, the payment gateway is down for everyone", "servicenow_open_case"},
	{"who is on call this weekend", "pagerduty_list_oncall"},
	{"get the change approved before we ship", "servicenow_approve_change"},
	{"write up the postmortem on the wiki", "confluence_create_page"},
	{"ship the branch once review passes", "github_merge_pull_request"},
	{"the CI job flaked, run it again", "github_rerun_workflow"},
	{"stop that monitor waking people for the next two hours", "datadog_mute_monitor"},
	{"pay the supplier invoice", "netsuite_approve_invoice"},
	{"what did the customer say when they scored us", "zendesk_satisfaction_score"},
	{"show me what this infrastructure change would do before I run it", "terraform_plan_workspace"},
	{"the deal is signed, mark it", "salesforce_close_won"},
	{"turn the warehouse back on, the reports are queued", "snowflake_resume_warehouse"},
	{"this user is locked out, send them a new password", "okta_reset_password"},
	{"cancel the subscription, they asked to stop paying", "stripe_cancel_subscription"},
	{"grab that file back out of storage", "s3_get_object"},
	{"give this contractor access to the expenses app", "okta_assign_app"},
	{"log the two hours I spent on the bug", "jira_log_work"},
	{"the customer wants their invoice cancelled, it was raised in error", "stripe_void_invoice"},
	{"which servers are we running right now", "datadog_host_map"},
	{"put the signed contract on the opportunity", "salesforce_attach_quote"},
	{"nobody should be able to edit that page any more", "confluence_restrict_page"},
	{"start the paperwork for the new joiner", "workday_start_onboarding"},
	{"how much are we spending on storage", "snowflake_storage_usage"},
	{"roll the whole environment back to nothing", "terraform_destroy_workspace"},
	{"someone needs to cover Friday night instead of me", "pagerduty_override_oncall"},
	{"tell me what changed between the two builds", "github_compare_commits"},
	{"this complaint and that one are the same problem", "zendesk_merge_tickets"},
	{"who has been signing in and from where", "okta_audit_logins"},
	{"close the books for last month", "netsuite_close_period"},
	{"stop anyone pushing straight to main", "github_protect_branch"},
	{"the customer is furious, bump this to the top", "zendesk_set_priority"},
	{"raise a purchase order for the new laptops", "netsuite_create_purchase_order"},
	{"make a copy of the production database to test against", "snowflake_clone_database"},
}
