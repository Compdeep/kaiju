package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Compdeep/kaiju/internal/agent"
	"github.com/Compdeep/kaiju/internal/agent/tools"
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
func (m *MemoryStore) Impact(map[string]any) int { return tools.ImpactObserve }

/*
 * OutputSchema returns the JSON schema for the tool's output.
 * desc: Defines the output structure containing a result confirmation string.
 * return: JSON schema as raw bytes
 */
func (m *MemoryStore) OutputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"result":{"type":"string"}}}`)
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
func (m *MemoryStore) Execute(_ context.Context, params map[string]any) (string, error) {
	key, _ := params["key"].(string)
	value, _ := params["value"].(string)
	if key == "" {
		return "", fmt.Errorf("memory_store: key is required")
	}

	var ttl time.Duration
	if ts, ok := params["ttl_sec"].(float64); ok && ts > 0 {
		ttl = time.Duration(ts) * time.Second
	}

	var tags []string
	if t, ok := params["tags"].([]any); ok {
		for _, v := range t {
			if s, ok := v.(string); ok {
				tags = append(tags, s)
			}
		}
	}

	m.mem.Set(key, value, ttl, tags)
	return fmt.Sprintf("stored key=%q (%d bytes)", key, len(value)), nil
}

var _ tools.Tool = (*MemoryStore)(nil)

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
func (m *MemoryRecall) Impact(map[string]any) int { return tools.ImpactObserve }

/*
 * OutputSchema returns the JSON schema for the tool's output.
 * desc: Defines the output structure containing the recalled memory value.
 * return: JSON schema as raw bytes
 */
func (m *MemoryRecall) OutputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"value":{"type":"string","description":"recalled memory value"}}}`)
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
func (m *MemoryRecall) Execute(_ context.Context, params map[string]any) (string, error) {
	key, _ := params["key"].(string)
	if key == "" {
		return "", fmt.Errorf("memory_recall: key is required")
	}
	val, ok := m.mem.Get(key)
	if !ok {
		return fmt.Sprintf("key=%q not found", key), nil
	}
	return val, nil
}

var _ tools.Tool = (*MemoryRecall)(nil)

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
func (m *MemorySearch) Impact(map[string]any) int { return tools.ImpactObserve }

/*
 * OutputSchema returns the JSON schema for the tool's output.
 * desc: Defines the output as a JSON array of objects with key and value fields.
 * return: JSON schema as raw bytes
 */
func (m *MemorySearch) OutputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"array","items":{"type":"object","properties":{"key":{"type":"string"},"value":{"type":"string"}}}}`)
}

/*
 * Parameters returns the JSON schema for the tool's input parameters.
 * desc: Defines the required tag parameter for memory search.
 * return: JSON schema as raw bytes
 */
func (m *MemorySearch) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"tag": {"type": "string", "description": "Tag to search for"}
		},
		"required": ["tag"],
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
func (m *MemorySearch) Execute(_ context.Context, params map[string]any) (string, error) {
	tag, _ := params["tag"].(string)
	if tag == "" {
		return "", fmt.Errorf("memory_search: tag is required")
	}
	results := m.mem.Search([]string{tag})
	if len(results) == 0 {
		return fmt.Sprintf("no entries with tag=%q", tag), nil
	}
	// Format results as key=value pairs
	type entry struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	out := make([]entry, len(results))
	for i, r := range results {
		out[i] = entry{Key: r.Key, Value: r.Value}
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

var _ tools.Tool = (*MemorySearch)(nil)
