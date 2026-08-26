package toolfind

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

type fakeTool struct {
	name, desc string
	params     json.RawMessage
}

func (f *fakeTool) Name() string        { return f.name }
func (f *fakeTool) Description() string { return f.desc }
func (f *fakeTool) Parameters() json.RawMessage {
	if f.params == nil {
		return json.RawMessage(`{}`)
	}
	return f.params
}
func (*fakeTool) Impact(map[string]any) int { return 0 }
func (*fakeTool) RequiresTarget() bool      { return false }
func (*fakeTool) Execute(context.Context, map[string]any) (string, error) {
	return "", nil
}

func registryOf(t *testing.T, tools ...*fakeTool) *toolapi.Registry {
	t.Helper()
	reg := toolapi.NewRegistry()
	for _, tl := range tools {
		src := tl.name
		if i := strings.Index(tl.name, "_"); i > 0 {
			src = tl.name[:i]
		}
		if err := reg.RegisterWithSource(tl, src); err != nil {
			t.Fatalf("register %s: %v", tl.name, err)
		}
	}
	return reg
}

func openOn(t *testing.T, reg *toolapi.Registry) Index {
	t.Helper()
	ix, err := Open(t.TempDir(), reg, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return ix
}

func positionOf(names []string, want string) int {
	for i, n := range names {
		if n == want {
			return i
		}
	}
	return -1
}

var sample = []*fakeTool{
	{name: "jira_create_issue", desc: "Open a new issue on a project board."},
	{name: "jira_transition_issue", desc: "Move an issue to another workflow state."},
	{name: "servicenow_open_case", desc: "Raise a ticket for an outage or a fault."},
	{name: "workday_absence_balance", desc: "Read how much leave a worker has left."},
	{name: "s3_put_object", desc: "Upload a file to a bucket.",
		params: json.RawMessage(`{"properties":{"bucket":{"description":"target bucket name"},"key":{"description":"object key"}}}`)},
	{name: "stripe_refund_charge", desc: "Return money taken from a customer."},
}

// The whole registry comes back, whatever the objective. A run whose words
// match nothing must still be able to plan — the caller decides how many of
// these fit, not this package.
func TestRank_ReturnsWholeRegistry(t *testing.T) {
	reg := registryOf(t, sample...)
	ix := openOn(t, reg)

	for _, objective := range []string{"", "refund a customer", "xyzzy nothing matches this"} {
		got := ix.Rank(context.Background(), objective)
		if len(got) != len(reg.List()) {
			t.Errorf("objective %q returned %d of %d tools", objective, len(got), len(reg.List()))
		}
	}
}

// Words shared with the objective are the whole of the ranking when there is no
// embedding client, and they have to be enough on their own — every deployment
// starts here, and one without an embedding endpoint stays here.
func TestRank_WordsAloneFindTheRightTool(t *testing.T) {
	ix := openOn(t, registryOf(t, sample...))

	for objective, want := range map[string]string{
		"refund money to a customer":           "stripe_refund_charge",
		"how much leave does this worker have": "workday_absence_balance",
		"upload the report to the bucket":      "s3_put_object",
		"move the issue to done":               "jira_transition_issue",
	} {
		got := ix.Rank(context.Background(), objective)
		if got[0] != want {
			t.Errorf("objective %q ranked %q first, want %q (order: %v)", objective, got[0], want, got[:3])
		}
	}
}

// A parameter name is often the only place the word a run uses appears at all.
func TestRank_FindsToolByItsParameters(t *testing.T) {
	ix := openOn(t, registryOf(t, sample...))
	got := ix.Rank(context.Background(), "write it to the bucket under a new object key")
	if got[0] != "s3_put_object" {
		t.Errorf("parameter words did not reach the index: %v", got[:3])
	}
}

// A tool the objective named outright is not a guess to be ranked among
// others. This is the case that matters when the planner asks for a tool by a
// name it read somewhere and the description says nothing like the objective.
func TestRank_NamedToolIsPinnedFirst(t *testing.T) {
	ix := openOn(t, registryOf(t, sample...))
	got := ix.Rank(context.Background(), "call jira_transition_issue on the ticket")
	if got[0] != "jira_transition_issue" {
		t.Errorf("a named tool was not pinned first: %v", got[:3])
	}
}

// The name has to be the whole name. A tool whose name merely occurs inside
// another word is not what the objective asked for.
func TestRank_PartialNameIsNotAName(t *testing.T) {
	reg := registryOf(t,
		&fakeTool{name: "issue", desc: "Something entirely unrelated to tickets."},
		&fakeTool{name: "jira_create_issue", desc: "Open a new issue on a project board."})
	ix := openOn(t, reg)

	got := ix.Rank(context.Background(), "run jira_create_issue for this bug")
	if got[0] != "jira_create_issue" {
		t.Errorf("want the named tool first, got %v", got)
	}
}

// A registry that changes while the agent runs — a plugin switched on — has to
// be rankable at once. A tool that cannot be ranked is one the planner is never
// shown, and it would stay invisible until the next boot.
func TestRank_PicksUpToolsAddedAfterOpen(t *testing.T) {
	reg := registryOf(t, sample...)
	ix := openOn(t, reg)

	added := &fakeTool{name: "pagerduty_ack_page", desc: "Acknowledge a page so it stops escalating."}
	if err := reg.RegisterWithSource(added, "pagerduty"); err != nil {
		t.Fatalf("register: %v", err)
	}

	got := ix.Rank(context.Background(), "acknowledge the page that just fired")
	if got[0] != "pagerduty_ack_page" {
		t.Errorf("a tool registered after Open did not rank: %v", got[:3])
	}
}

// And one removed has to leave, or the planner is offered a tool that no
// longer dispatches.
func TestRank_DropsUnregisteredTools(t *testing.T) {
	reg := registryOf(t, sample...)
	ix := openOn(t, reg)
	reg.Unregister("stripe_refund_charge")

	got := ix.Rank(context.Background(), "refund money to a customer")
	if positionOf(got, "stripe_refund_charge") >= 0 {
		t.Errorf("an unregistered tool was still ranked: %v", got)
	}
}

// Two runs of one objective have to produce one order. A ranking that moves on
// its own makes every failure above it unreproducible.
func TestRank_IsStable(t *testing.T) {
	ix := openOn(t, registryOf(t, sample...))
	first := ix.Rank(context.Background(), "raise a ticket for the outage")
	for i := 0; i < 5; i++ {
		if got := ix.Rank(context.Background(), "raise a ticket for the outage"); !equal(got, first) {
			t.Fatalf("run %d differed:\n %v\n %v", i, first, got)
		}
	}
}

// Systems is what stands in for the tools that did not fit, so it has to name
// every source and say how many each holds.
func TestSystems_NamesEverySourceWithACount(t *testing.T) {
	got := openOn(t, registryOf(t, sample...)).Systems()

	for _, want := range []string{"jira — 2 tools", "workday — 1 tool", "servicenow", "stripe", "s3"} {
		if !strings.Contains(got, want) {
			t.Errorf("systems is missing %q:\n%s", want, got)
		}
	}
	// The system's own prefix is not repeated inside its sample.
	if strings.Contains(got, "jira_create_issue") {
		t.Errorf("sample repeated the system prefix:\n%s", got)
	}
	if !strings.Contains(got, "create issue") {
		t.Errorf("sample did not say what the system does:\n%s", got)
	}
}

// A registry whose tools all came from one place has nothing to describe. The
// caller states the total as a count; a single line repeating it is noise, and
// this is what kaiju's own registry looks like.
func TestSystems_EmptyWhenEverythingSharesASource(t *testing.T) {
	reg := toolapi.NewRegistry()
	for _, n := range []string{"bash", "file_read", "web_fetch"} {
		if err := reg.Register(&fakeTool{name: n, desc: "a tool"}); err != nil {
			t.Fatal(err)
		}
	}
	if got := openOn(t, reg).Systems(); got != "" {
		t.Errorf("want no systems line for one source, got %q", got)
	}
}

// Ranking must not depend on a writable directory, an embedding client, or a
// vector store that survived. Each of these is absent in some real deployment.
func TestOpen_DegradesWithoutStorageOrEmbedding(t *testing.T) {
	reg := registryOf(t, sample...)

	for name, dir := range map[string]string{
		"no directory":   "",
		"unwritable dir": filepath.Join(t.TempDir(), "does", "not", "exist"),
	} {
		t.Run(name, func(t *testing.T) {
			ix, err := Open(dir, reg, nil)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			if got := ix.Rank(context.Background(), "refund money to a customer"); got[0] != "stripe_refund_charge" {
				t.Errorf("ranking broke without storage: %v", got[:3])
			}
		})
	}
}

func TestOpen_RejectsNilRegistry(t *testing.T) {
	if _, err := Open(t.TempDir(), nil, nil); err == nil {
		t.Fatal("a nil registry must not produce a working index")
	}
}

// A stored vector belongs to the document it was taken from. When a tool's
// description changes the old vector has to be discarded, or the tool ranks on
// what it used to say.
func TestVectorStore_DropsVectorsWhoseDocumentChanged(t *testing.T) {
	dir := t.TempDir()
	hashes := map[string]string{"a": "hash-a", "b": "hash-b"}
	vecs := map[string][]float32{"a": {1, 0}, "b": {0, 1}}
	saveVectors(dir, "model-1", hashes, vecs)

	// Same model, but "b" now hashes differently — its vector is stale.
	got := loadVectors(dir, "model-1", map[string]string{"a": "hash-a", "b": "hash-CHANGED"})
	if _, ok := got["a"]; !ok {
		t.Error("an unchanged tool lost its vector")
	}
	if _, ok := got["b"]; ok {
		t.Error("a changed tool kept a vector taken from its old description")
	}
}

// Vectors from two models cannot be compared. Changing the model has to
// invalidate the whole store rather than silently mixing them.
func TestVectorStore_DiscardedWhenTheModelChanges(t *testing.T) {
	dir := t.TempDir()
	saveVectors(dir, "model-1", map[string]string{"a": "h"}, map[string][]float32{"a": {1, 0}})

	if got := loadVectors(dir, "model-2", map[string]string{"a": "h"}); len(got) != 0 {
		t.Errorf("vectors from another model survived: %v", got)
	}
}

// The store is a cache, not a source of truth. Nothing readable there means
// everything is embedded again, which is what a first run does.
func TestVectorStore_MissingAndCorruptFilesAreNotErrors(t *testing.T) {
	dir := t.TempDir()
	if got := loadVectors(dir, "m", map[string]string{"a": "h"}); got != nil {
		t.Errorf("a missing store returned %v", got)
	}
	if err := os.WriteFile(filepath.Join(dir, vectorFile), []byte("not gob"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadVectors(dir, "m", map[string]string{"a": "h"}); got != nil {
		t.Errorf("a corrupt store returned %v", got)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A hot reload swaps a tool for a new one under the same name. The list of
// registered names is identical before and after, so an index that watches
// names sees nothing and goes on ranking the description the tool used to
// carry — until the process restarts.
//
// This is what Registry.Version exists for, and it is the case a names-only
// check silently fails.
func TestRank_SeesAToolReplacedUnderItsOwnName(t *testing.T) {
	reg := registryOf(t, sample...)
	ix := openOn(t, reg)

	before := ix.Rank(context.Background(), "how much leave a worker has left")
	if before[0] != "workday_absence_balance" {
		t.Fatalf("fixture is wrong: %v", before[:3])
	}

	// Same name, entirely different work.
	reg.Replace(&fakeTool{
		name: "workday_absence_balance",
		desc: "Compile the quarterly revenue forecast from booked deals.",
	}, "workday")

	after := ix.Rank(context.Background(), "compile the quarterly revenue forecast")
	if after[0] != "workday_absence_balance" {
		t.Errorf("the replacement's own description did not reach the index: %v", after[:3])
	}
	stale := ix.Rank(context.Background(), "how much leave a worker has left")
	if stale[0] == "workday_absence_balance" {
		t.Errorf("the tool still ranks on the description it no longer has: %v", stale[:3])
	}
}

// Switching a tool off has to reach the index too — it is a change to what the
// registry holds, and nothing about the list of names says it happened.
func TestRank_SeesAToolSwitchedOff(t *testing.T) {
	reg := registryOf(t, sample...)
	ix := openOn(t, reg)
	if got := ix.Rank(context.Background(), "refund money to a customer"); got[0] != "stripe_refund_charge" {
		t.Fatalf("fixture is wrong: %v", got[:3])
	}

	if err := reg.SetEnabled("stripe_refund_charge", false); err != nil {
		t.Fatal(err)
	}
	if got := ix.Rank(context.Background(), "refund money to a customer"); got[0] == "stripe_refund_charge" {
		t.Errorf("a tool switched off still led the ranking: %v", got[:3])
	}
}
