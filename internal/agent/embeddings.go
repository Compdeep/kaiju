package agent

import (
	"context"
	"log"
	"math"
	"sort"

	"github.com/Compdeep/kaiju/internal/agent/llm"
	"github.com/Compdeep/kaiju/internal/agent/tools"
)

/*
 * ToolVector pairs a tool name with its embedding vector.
 * desc: Stores the precomputed embedding for a single tool's description.
 */
type ToolVector struct {
	Name   string
	Vector []float64
}

/*
 * EmbeddingStore holds precomputed tool embeddings for semantic routing.
 * desc: Immutable after Load() — no mutex needed for reads. Supports ranking
 *       tools by cosine similarity to a query embedding, with configurable
 *       top-K and threshold, plus always-include overrides.
 */
type EmbeddingStore struct {
	vectors []ToolVector
	topK    int
	thresh  float64
	always  map[string]bool // always-include tool names
}

/*
 * NewEmbeddingStore creates an EmbeddingStore with the given parameters.
 * desc: Initializes the store with ranking configuration and always-include set.
 * param: topK - maximum number of top-ranked tools to return.
 * param: threshold - minimum cosine similarity score for inclusion.
 * param: alwaysInclude - tool names that are always returned regardless of score.
 * return: pointer to the new EmbeddingStore.
 */
func NewEmbeddingStore(topK int, threshold float64, alwaysInclude []string) *EmbeddingStore {
	always := make(map[string]bool, len(alwaysInclude))
	for _, name := range alwaysInclude {
		always[name] = true
	}
	return &EmbeddingStore{
		topK:   topK,
		thresh: threshold,
		always: always,
	}
}

/*
 * Load embeds all tool descriptions via a single batch call and stores vectors.
 * desc: Called once at boot after all tools are registered. Collects descriptions
 *       from the registry, sends them to the embedding API in one batch, and
 *       stores the resulting vectors.
 * param: ctx - context for the embedding API call.
 * param: client - LLM client with embedding support.
 * param: registry - tool registry to read tool descriptions from.
 * return: error if the embedding call fails.
 */
func (es *EmbeddingStore) Load(ctx context.Context, client *llm.Client, registry *tools.Registry) error {
	names := registry.List()
	if len(names) == 0 {
		return nil
	}

	// Collect descriptions
	texts := make([]string, 0, len(names))
	validNames := make([]string, 0, len(names))
	for _, name := range names {
		tool, ok := registry.Get(name)
		if !ok {
			continue
		}
		texts = append(texts, tool.Description())
		validNames = append(validNames, name)
	}

	vectors, err := client.Embed(ctx, texts)
	if err != nil {
		return err
	}

	es.vectors = make([]ToolVector, len(validNames))
	for i, name := range validNames {
		es.vectors[i] = ToolVector{Name: name, Vector: vectors[i]}
	}

	log.Printf("[embeddings] loaded %d tool vectors", len(es.vectors))
	return nil
}

/*
 * toolScore pairs a tool name with its cosine similarity score.
 * desc: Internal type used during tool ranking.
 */
type toolScore struct {
	Name  string
	Score float64
}

/*
 * RankTools embeds the query text, ranks against stored vectors, and returns
 * top-K tool names plus always-include tools.
 * desc: Falls back to all tools on error or if no vectors are loaded.
 * param: ctx - context for the embedding API call.
 * param: client - LLM client with embedding support.
 * param: query - the user query text to embed and rank against.
 * param: registry - tool registry for fallback listing.
 * return: ordered slice of tool names, or error.
 */
func (es *EmbeddingStore) RankTools(ctx context.Context, client *llm.Client, query string, registry *tools.Registry) ([]string, error) {
	if len(es.vectors) == 0 {
		return registry.List(), nil
	}

	qVecs, err := client.Embed(ctx, []string{query})
	if err != nil {
		log.Printf("[embeddings] query embed failed, falling back to all tools: %v", err)
		return registry.List(), nil
	}
	if len(qVecs) == 0 || len(qVecs[0]) == 0 {
		return registry.List(), nil
	}
	qVec := qVecs[0]

	// Score all tools
	scores := make([]toolScore, 0, len(es.vectors))
	for _, sv := range es.vectors {
		sim := cosineSimilarity(qVec, sv.Vector)
		scores = append(scores, toolScore{Name: sv.Name, Score: sim})
	}

	// Sort descending by score
	sort.Slice(scores, func(i, j int) bool {
		return scores[i].Score > scores[j].Score
	})

	// Select top-K above threshold + always-include
	seen := make(map[string]bool)
	result := make([]string, 0, es.topK+len(es.always))

	for i, s := range scores {
		if i >= es.topK && !es.always[s.Name] {
			continue
		}
		if s.Score < es.thresh && !es.always[s.Name] {
			continue
		}
		if !seen[s.Name] {
			result = append(result, s.Name)
			seen[s.Name] = true
		}
	}

	// Ensure always-include tools are present even if not in top-K
	for name := range es.always {
		if !seen[name] {
			if _, ok := registry.Get(name); ok {
				result = append(result, name)
				seen[name] = true
			}
		}
	}

	if len(result) == 0 {
		return registry.List(), nil
	}

	return result, nil
}

/*
 * cosineSimilarity computes the cosine similarity between two vectors.
 * desc: Returns the dot product of a and b divided by the product of their
 *       magnitudes. Returns 0 for mismatched lengths or zero-norm vectors.
 * param: a - first embedding vector.
 * param: b - second embedding vector.
 * return: cosine similarity in the range [-1, 1].
 */
func cosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}

	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return dot / denom
}
