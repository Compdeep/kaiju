package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Compdeep/kaiju/agent"
	"github.com/Compdeep/kaiju/agent/toolapi"
)

// ─── MemoryStore ────────────────────────────────────────────────────────────

/*
 * MemoryStore saves a key-value pair to the agent's persistent memory.
 * desc: Tool for storing values with optional TTL and tags in the agent's memory system.
 */
type MemoryStore struct {
	mem *agent.Memory
}

/*
 * NewMemoryStore creates a new MemoryStore tool backed by the given memory instance.
 * desc: Initializes MemoryStore with a reference to the agent's persistent memory.
 * param: mem - the agent Memory instance to store values in
 * return: pointer to a new MemoryStore
 */
func NewMemoryStore(mem *agent.Memory) *MemoryStore { return &MemoryStore{mem: mem} }

// RequiresTarget is false: this is the agent's own memory, in this process, so
// there is no other machine for a step to name. Without this the default applies,
// which is true, and an application that checks it would ask every memory step to
// name a machine before it could run.
func (m *MemoryStore) RequiresTarget() bool { return false }

/*
 * Name returns the tool identifier.
 * desc: Returns "memory_store" as the tool name.
 * return: the string "memory_store"
 */
func (m *MemoryStore) Name() string { return "memory_store" }

/*
 * Description returns a human-readable description of the tool.
 * desc: Explains that this tool stores key-value pairs with optional TTL and tags.
 * return: description string
 */
func (m *MemoryStore) Description() string {
	return "Store a key-value pair in persistent memory with optional TTL and tags."
}

/*
 * Impact returns the safety impact level for this tool.
 * desc: Always returns ImpactObserve since storing to internal memory is non-destructive.
 * param: _ - unused parameters
 * return: ImpactObserve (0)
 */
func (m *MemoryStore) Impact(map[string]any) int { return toolapi.ImpactObserve }

/*
 * OutputSchema returns the JSON schema for the tool's output.
 * desc: Defines the output structure containing a result confirmation string.
 * return: JSON schema as raw bytes
 */
func (m *MemoryStore) OutputSchema() json.RawMessage {
	return toolapi.EnvelopeSchema(toolapi.PayloadSchemaOf(memoryStoreData{}))
}

/*
 * Parameters returns the JSON schema for the tool's input parameters.
 * desc: Defines key (required), value (required), optional ttl_sec, and optional tags parameters.
 * return: JSON schema as raw bytes
 */
func (m *MemoryStore) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"key": {"type": "string", "description": "Memory key"},
			"value": {"type": "string", "description": "Value to store"},
			"ttl_sec": {"type": "integer", "description": "Time-to-live in seconds (0 = no expiry)"},
			"tags": {"type": "array", "items": {"type": "string"}, "description": "Tags for search"}
		},
		"required": ["key", "value"],
		"additionalProperties": false
	}`)
}

/*
 * Execute stores a key-value pair in persistent memory.
 * desc: Saves the value under the given key with optional TTL and tags for later retrieval.
 * param: _ - unused context
 * param: params - must contain "key" and "value"; optionally "ttl_sec" and "tags"
 * return: confirmation message with key and byte count, or error if key is empty
 */
// Execute satisfies the Tool interface for callers outside the DAG.
func (m *MemoryStore) Execute(_ context.Context, params map[string]any) (string, error) {
	return toolapi.StringResult(m.ExecuteTyped(nil, params))
}

func (m *MemoryStore) ExecuteTyped(_ context.Context, params map[string]any) (toolapi.ToolMessage, error) {
	// No memory to reach. A tool built without its store panicked inside Memory,
	// which takes the process down rather than failing one step — and an agent
	// crashing on a memory call is a worse answer to "what is stored" than a
	// refusal. Reported as a failure so the run continues and the reason is visible.
	if m.mem == nil {
		return toolapi.ToolFail("status", "memory_store is not available: this agent was built with no memory store", nil), nil
	}
	key, _ := params["key"].(string)
	value, _ := params["value"].(string)
	if key == "" {
		return toolapi.ToolMessage{}, fmt.Errorf("memory_store: key is required")
	}

	var ttl time.Duration
	if ts, ok := toolapi.ParamNum(params, "ttl_sec"); ok && ts > 0 {
		ttl = time.Duration(ts) * time.Second
	}

	// A list of names arrives as []any when a planner wrote the step, as JSON, and
	// as []string when this program built it. Reading only the first shape left the
	// tags empty for the second with no error: the value is stored, the tags are
	// dropped, and memory_search never finds it again.
	tags := toolapi.ParamStrings(params, "tags")

	m.mem.Set(key, value, ttl, tags)
	return toolapi.ToolOK("status", fmt.Sprintf("stored key=%q (%d bytes)", key, len(value)),
		memoryStoreData{Key: key, Bytes: len(value), Tags: tags, TTLSec: int(ttl.Seconds())}), nil
}

var _ toolapi.Tool = (*MemoryStore)(nil)

// ─── MemoryRecall ───────────────────────────────────────────────────────────

/*
 * MemoryRecall retrieves a value from persistent memory by key.
 * desc: Tool for recalling previously stored values from the agent's memory system.
 */
type MemoryRecall struct {
	mem *agent.Memory
}

/*
 * NewMemoryRecall creates a new MemoryRecall tool backed by the given memory instance.
 * desc: Initializes MemoryRecall with a reference to the agent's persistent memory.
 * param: mem - the agent Memory instance to recall values from
 * return: pointer to a new MemoryRecall
 */
func NewMemoryRecall(mem *agent.Memory) *MemoryRecall { return &MemoryRecall{mem: mem} }

// RequiresTarget is false, for the reason given on MemoryStore.
func (m *MemoryRecall) RequiresTarget() bool { return false }

/*
 * Name returns the tool identifier.
 * desc: Returns "memory_recall" as the tool name.
 * return: the string "memory_recall"
 */
func (m *MemoryRecall) Name() string { return "memory_recall" }

/*
 * Description returns a human-readable description of the tool.
 * desc: Explains that this tool recalls a value from persistent memory by key.
 * return: description string
 */
func (m *MemoryRecall) Description() string {
	return "Recall a value from persistent memory by key."
}

/*
 * Impact returns the safety impact level for this tool.
 * desc: Always returns ImpactObserve since reading from memory is non-destructive.
 * param: _ - unused parameters
 * return: ImpactObserve (0)
 */
func (m *MemoryRecall) Impact(map[string]any) int { return toolapi.ImpactObserve }

/*
 * OutputSchema returns the JSON schema for the tool's output.
 * desc: Defines the output structure containing the recalled memory value.
 * return: JSON schema as raw bytes
 */
func (m *MemoryRecall) OutputSchema() json.RawMessage {
	return toolapi.EnvelopeSchema(toolapi.PayloadSchemaOf(memoryRecallData{}))
}

/*
 * Parameters returns the JSON schema for the tool's input parameters.
 * desc: Defines the required key parameter for memory lookup.
 * return: JSON schema as raw bytes
 */
func (m *MemoryRecall) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"key": {"type": "string", "description": "Memory key to recall"}
		},
		"required": ["key"],
		"additionalProperties": false
	}`)
}

/*
 * Execute retrieves a value from persistent memory by key.
 * desc: Looks up the key in the agent's memory and returns its value, or a not-found message.
 * param: _ - unused context
 * param: params - must contain "key"
 * return: stored value string, "key not found" message, or error if key is empty
 */
// Execute satisfies the Tool interface for callers outside the DAG.
func (m *MemoryRecall) Execute(_ context.Context, params map[string]any) (string, error) {
	return toolapi.StringResult(m.ExecuteTyped(nil, params))
}

func (m *MemoryRecall) ExecuteTyped(_ context.Context, params map[string]any) (toolapi.ToolMessage, error) {
	// No memory to reach. A tool built without its store panicked inside Memory,
	// which takes the process down rather than failing one step — and an agent
	// crashing on a memory call is a worse answer to "what is stored" than a
	// refusal. Reported as a failure so the run continues and the reason is visible.
	if m.mem == nil {
		return toolapi.ToolFail("kv", "memory_recall is not available: this agent was built with no memory store", nil), nil
	}
	key, _ := params["key"].(string)
	if key == "" {
		// Nothing was looked up, so this is not an empty memory: reporting it as
		// empty would say memory holds nothing under a key nobody named. A failed
		// result rather than a Go error, so the reason reaches whoever wrote the
		// step, and it names the tool that searches without a key.
		return toolapi.ToolFail("kv",
			"memory_recall needs a key — none was supplied. To search by tag, use memory_search",
			nil), nil
	}
	val, ok := m.mem.Get(key)
	if !ok {
		return toolapi.ToolEmpty("kv", fmt.Sprintf("key=%q not found", key)), nil
	}
	// The key as a field, so a step that recalled one value can name the key it
	// came from rather than carrying it forward as text.
	return toolapi.ToolOK("kv", val, memoryRecallData{
		Key: key, Bytes: len(val),
	}), nil
}

// memoryRecallData is what memory_recall returns beside the stored value.
type memoryRecallData struct {
	Key   string `json:"key" desc:"the key that was read"`
	Bytes int    `json:"bytes" desc:"length of the stored value"`
}

// memoryStoreData is what memory_store returns.
type memoryStoreData struct {
	Key    string   `json:"key" desc:"the key that was written"`
	Bytes  int      `json:"bytes" desc:"length of the value stored"`
	Tags   []string `json:"tags,omitempty" desc:"the tags it was stored under, which memory_search reads"`
	TTLSec int      `json:"ttl_sec" desc:"seconds until it expires, 0 when it does not"`
}

var _ toolapi.Tool = (*MemoryRecall)(nil)

// ─── MemorySearch ───────────────────────────────────────────────────────────

/*
 * MemorySearch finds memory entries by tag.
 * desc: Tool for searching the agent's persistent memory by tag and returning matching key-value pairs.
 */
type MemorySearch struct {
	mem *agent.Memory
}

/*
 * NewMemorySearch creates a new MemorySearch tool backed by the given memory instance.
 * desc: Initializes MemorySearch with a reference to the agent's persistent memory.
 * param: mem - the agent Memory instance to search within
 * return: pointer to a new MemorySearch
 */
func NewMemorySearch(mem *agent.Memory) *MemorySearch { return &MemorySearch{mem: mem} }

// RequiresTarget is false, for the reason given on MemoryStore.
func (m *MemorySearch) RequiresTarget() bool { return false }

/*
 * Name returns the tool identifier.
 * desc: Returns "memory_search" as the tool name.
 * return: the string "memory_search"
 */
func (m *MemorySearch) Name() string { return "memory_search" }

/*
 * Description returns a human-readable description of the tool.
 * desc: Explains that this tool searches persistent memory entries by tag.
 * return: description string
 */
func (m *MemorySearch) Description() string {
	return "Search persistent memory entries by tag."
}

/*
 * Impact returns the safety impact level for this tool.
 * desc: Always returns ImpactObserve since searching memory is non-destructive.
 * param: _ - unused parameters
 * return: ImpactObserve (0)
 */
func (m *MemorySearch) Impact(map[string]any) int { return toolapi.ImpactObserve }

/*
 * OutputSchema returns the JSON schema for the tool's output.
 * desc: Defines the output as a JSON array of objects with key and value fields.
 * return: JSON schema as raw bytes
 */
// OutputSchema declares the tags searched, the count, and the entries.
//
// It declared a bare array, which described the payload as it was and left a
// planner nothing to name but an index. The payload is an object now, so the count
// and the tags are readable without walking the list.
func (m *MemorySearch) OutputSchema() json.RawMessage {
	return toolapi.EnvelopeSchema(toolapi.PayloadSchemaOf(memorySearchData{}))
}

/*
 * Parameters returns the JSON schema for the tool's input parameters.
 * desc: Defines the required tag parameter for memory search.
 * return: JSON schema as raw bytes
 */
// Parameters takes a list of tags, and a single tag for callers that send one.
//
// It took one tag only. An application whose steps pass "tags" got nothing —
// additionalProperties false rejected the call outright — so the tool could not
// answer "what is stored under any of these", which is the question a search is
// for.
func (m *MemorySearch) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"tags": {"type": "array", "items": {"type": "string"}, "description": "Tags to search for; an entry matching any of them is returned"},
			"tag": {"type": "string", "description": "A single tag, for a caller that has one"}
		},
		"additionalProperties": false
	}`)
}

/*
 * Execute searches persistent memory for entries matching the given tag.
 * desc: Queries the memory system by tag and returns matching entries as a JSON array of key-value pairs.
 * param: _ - unused context
 * param: params - must contain "tag"
 * return: JSON array of matching entries, "no entries" message, or error if tag is empty
 */
// Execute satisfies the Tool interface for callers outside the DAG.
func (m *MemorySearch) Execute(_ context.Context, params map[string]any) (string, error) {
	return toolapi.StringResult(m.ExecuteTyped(nil, params))
}

func (m *MemorySearch) ExecuteTyped(_ context.Context, params map[string]any) (toolapi.ToolMessage, error) {
	// No memory to reach. A tool built without its store panicked inside Memory,
	// which takes the process down rather than failing one step — and an agent
	// crashing on a memory call is a worse answer to "what is stored" than a
	// refusal. Reported as a failure so the run continues and the reason is visible.
	if m.mem == nil {
		return toolapi.ToolFail("search", "memory_search is not available: this agent was built with no memory store", nil), nil
	}
	// Both forms, and ParamStrings reads a list whether it arrived as JSON from a
	// planner or as Go strings from this program.
	tags := toolapi.ParamStrings(params, "tags")
	if len(tags) == 0 {
		if one, _ := params["tag"].(string); one != "" {
			tags = []string{one}
		}
	}
	if len(tags) == 0 {
		// The caller's mistake, not an absence in memory. Reported as a failed
		// result rather than ending the step, so the reason reaches whoever wrote
		// the step and it can be written again with a tag in it.
		return toolapi.ToolFail("search", "memory_search needs at least one tag — none was supplied", nil), nil
	}

	results := m.mem.Search(tags)
	if len(results) == 0 {
		return toolapi.ToolEmpty("search", fmt.Sprintf("nothing is stored under the tags %v", tags)), nil
	}

	entries := make([]memoryEntry, len(results))
	var text strings.Builder
	for i, r := range results {
		entries[i] = memoryEntry{Key: r.Key, Value: r.Value}
		text.WriteString("- **" + r.Key + "**: " + r.Value + "\n")
	}
	// Text as well as the entries. This returned the entries with nothing in
	// content, so anything reading the text of a result saw an answer with nothing
	// in it while the same tool's siblings answered in text.
	return toolapi.ToolOK("search", strings.TrimRight(text.String(), "\n"), memorySearchData{
		Tags: tags, Count: len(entries), Entries: entries,
	}), nil
}

// memoryEntry is one stored pair.
type memoryEntry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// memorySearchData is what memory_search returns beside its listing.
type memorySearchData struct {
	Tags    []string      `json:"tags" desc:"the tags that were searched for"`
	Count   int           `json:"count" desc:"entries found"`
	Entries []memoryEntry `json:"entries" desc:"the matching pairs"`
}

var _ toolapi.Tool = (*MemorySearch)(nil)
