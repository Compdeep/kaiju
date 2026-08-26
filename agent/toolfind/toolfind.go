// Package toolfind answers one question about a tool registry: given what a run
// is trying to do, which tools should be put in front of the model, and in what
// order.
//
// It exists because a registry outgrows a prompt. Kaiju's own twenty-seven
// tools compile to about eleven thousand characters of planner index and fit
// whole; an enterprise registry of a thousand connector tools is somewhere
// between a hundred and six hundred thousand, and no prompt carries that. Once
// the whole list cannot be shown, something has to choose — and the choice has
// to be made from what the run is doing, at every point where the run plans,
// not once at the start.
//
// The interface is two methods because everything else is detail: which of the
// two searches found a tool, whether its vector was on disk or had to be paid
// for, how the two rankings were merged. A caller asks for an order and gets
// one, always, whatever is missing underneath.
package toolfind

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"

	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/agent/toolapi"
)

// Index ranks a registry against what a run is trying to do.
type Index interface {
	// Rank returns every tool in the registry, the ones most likely to serve
	// the objective first. Never empty for a non-empty registry: a run whose
	// objective matches nothing gets the registry in its own order rather than
	// no tools at all.
	Rank(ctx context.Context, objective string) []string

	// Ready reports how much of the registry can be ranked on vectors, and how
	// much there is. Equal numbers mean the search is running on both halves;
	// a smaller first number means the embedding pass is still going or partly
	// failed, and ranking is on words alone for the difference.
	//
	// Here because a deployment large enough to need this package is one where
	// an operator has to be able to see that its search is degraded — a boot
	// log and a status endpoint both ask this question, and a run that quietly
	// ranks a thousand tools on words looks exactly like one that does not.
	Ready() (indexed, total int)

	// Systems describes the registry by where its tools came from — one line
	// per source, with a count and a sample of what it holds.
	//
	// It is what a model is shown INSTEAD of the tools that did not fit, so a
	// plan is written knowing a system exists even when none of its tools were
	// ranked high enough to show. Without it a model reads a short list as the
	// whole world and reports work impossible that the registry can do.
	Systems() string
}

// Open builds an index over reg.
//
// dir is where embeddings are kept between runs; "" keeps them in memory and
// pays for them again at each boot. embed is the client used to compute them;
// nil ranks on words alone. Neither is required, and neither failing stops a
// caller getting an order back — see Rank.
//
// Embedding happens in the background: Open returns as soon as the word index
// is built, and vectors join the ranking as they arrive. A boot is therefore
// never delayed by a thousand embedding calls, and the first few runs rank on
// words, which is what this package would fall back to anyway.
func Open(dir string, reg *toolapi.Registry, embed *llm.Client) (Index, error) {
	if reg == nil {
		return nil, fmt.Errorf("toolfind: no registry")
	}
	ix := &index{dir: dir, reg: reg}
	// Assigned inside the guard: a nil *llm.Client stored in an interface is
	// not a nil interface, and every check below would then call through it.
	if embed != nil {
		ix.embed = embed
		ix.model = embed.Model()
	}
	ix.sync(context.Background())
	return ix, nil
}

// embedder is the part of llm.Client this package uses. Narrowed to two
// methods so the ranking can be tested without a network, and so nothing here
// can reach for the rest of a chat client by accident.
type embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float64, error)
	Model() string
}

type index struct {
	dir   string
	reg   *toolapi.Registry
	embed embedder
	model string

	mu      sync.RWMutex
	version uint64            // the registry version the state below was built from
	names   []string          // registry order
	docs    map[string]string // tool name → the text it is found by
	hashes  map[string]string // tool name → hash of that text
	lexicon *lexicon          // word index over docs
	vectors map[string][]float32
	systems string

	embedding sync.Mutex // held while a background embedding pass runs
}

/*
 * Rank orders the registry against an objective.
 * desc: Merges a word ranking and a vector ranking, and pins a tool the
 *       objective named outright. Re-reads the registry first when it has
 *       changed, so a tool added by a plugin mid-run is rankable at once.
 * param: ctx - cancelled with the caller.
 * param: objective - what the run is trying to do, in its own words.
 * return: every tool name, most relevant first.
 */
func (ix *index) Rank(ctx context.Context, objective string) []string {
	ix.sync(ctx)

	ix.mu.RLock()
	defer ix.mu.RUnlock()

	if len(ix.names) == 0 {
		return nil
	}
	if strings.TrimSpace(objective) == "" {
		return append([]string(nil), ix.names...)
	}

	// Two searches, and each tool is ranked by the one that can see it.
	//
	// Not by both. Fusing them with equal weight was the first thing tried, and
	// it is measurably worse: over forty objectives against a thousand tools,
	// recall at forty was 0.62 fused and 0.78 on vectors alone, and the gap
	// held at every cut-off. The reason is visible in the misses. An objective
	// is written the way a person asks for work — "the deal is signed, mark
	// it" — and word ranking answers it confidently and wrongly, because
	// "list", "record" and "report" occur in half the registry. A confident
	// wrong answer promoted alongside a right one costs the right one its
	// place.
	//
	// So words rank the tools that have no vector, which is every tool when
	// there is no embedding endpoint, and the rest while the first embedding
	// pass is still running. Both halves produce a reciprocal-rank term of the
	// same shape, so a tool ranked first by words sits beside one ranked first
	// by vectors — which is what a half-warm index needs, and reduces to
	// vectors alone once every tool has one.
	const rrfK = 60.0
	lexical := rankOf(ix.lexicon.score(objective))
	semantic := rankOf(ix.semanticScore(ctx, objective))

	fused := make(map[string]float64, len(ix.names))
	for _, n := range ix.names {
		_, embedded := ix.vectors[n]
		// No semantic ranking at all means the objective could not be embedded.
		// Words are then the only opinion there is, for every tool.
		if len(semantic) == 0 {
			embedded = false
		}
		source := lexical
		if embedded {
			source = semantic
		}
		if r, ok := source[n]; ok {
			fused[n] = 1 / (rrfK + float64(r))
		}
	}

	// A tool the objective named is not a guess to be ranked. It goes first.
	pinned := ix.named(objective)

	scored := make([]string, 0, len(ix.names))
	unscored := make([]string, 0, len(ix.names))
	for _, n := range ix.names {
		if pinned[n] {
			continue
		}
		if _, ok := fused[n]; ok {
			scored = append(scored, n)
		} else {
			unscored = append(unscored, n)
		}
	}
	sort.SliceStable(scored, func(i, j int) bool {
		return fused[scored[i]] > fused[scored[j]]
	})

	out := make([]string, 0, len(ix.names))
	for _, n := range ix.names { // registry order among the pinned
		if pinned[n] {
			out = append(out, n)
		}
	}
	out = append(out, scored...)
	return append(out, unscored...)
}

// Ready reports vectors held against tools registered.
func (ix *index) Ready() (int, int) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	if ix.embed == nil {
		// Nothing was ever going to be embedded, so nothing is outstanding.
		return len(ix.names), len(ix.names)
	}
	return len(ix.vectors), len(ix.names)
}

// Systems returns the per-source description of the registry.
func (ix *index) Systems() string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.systems
}

// semanticScore is the cosine similarity of the objective to each tool that has
// a vector. Nil when there is no embedding client, no vector, or the objective
// itself cannot be embedded — in which case the word ranking stands alone,
// which is the whole reason the two are merged by rank and not by score.
func (ix *index) semanticScore(ctx context.Context, objective string) map[string]float64 {
	if ix.embed == nil || len(ix.vectors) == 0 {
		return nil
	}
	qv, err := ix.embed.Embed(ctx, []string{objective})
	if err != nil || len(qv) == 0 || len(qv[0]) == 0 {
		if err != nil {
			log.Printf("[toolfind] objective embed failed, ranking on words only: %v", err)
		}
		return nil
	}
	q := toFloat32(qv[0])
	if !normalize(q) {
		return nil
	}
	out := make(map[string]float64, len(ix.vectors))
	for name, v := range ix.vectors {
		if s := similarity(q, v); s > 0 {
			out[name] = s
		}
	}
	return out
}

// named returns the tools the objective mentioned by their registered name.
//
// Whole-name matching only, and bounded by non-word characters, so `jira_issue`
// in a sentence is a name and the `issue` inside `jira_issue_transition` is
// not.
func (ix *index) named(objective string) map[string]bool {
	lower := strings.ToLower(objective)
	out := map[string]bool{}
	for _, n := range ix.names {
		if len(n) < 3 {
			continue // too short to mean anything on its own
		}
		if wholeWordAt(lower, strings.ToLower(n)) {
			out[n] = true
		}
	}
	return out
}

func wholeWordAt(haystack, needle string) bool {
	from := 0
	for {
		i := strings.Index(haystack[from:], needle)
		if i < 0 {
			return false
		}
		i += from
		beforeOK := i == 0 || !isWordByte(haystack[i-1])
		after := i + len(needle)
		afterOK := after == len(haystack) || !isWordByte(haystack[after])
		if beforeOK && afterOK {
			return true
		}
		from = i + 1
	}
}

func isWordByte(b byte) bool {
	return b == '_' || b == '-' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// rankOf turns scores into 1-based positions, best first. Ties keep a stable
// order so two runs of the same objective produce the same ranking.
func rankOf(scores map[string]float64) map[string]int {
	if len(scores) == 0 {
		return nil
	}
	names := make([]string, 0, len(scores))
	for n := range scores {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if scores[names[i]] != scores[names[j]] {
			return scores[names[i]] > scores[names[j]]
		}
		return names[i] < names[j]
	})
	out := make(map[string]int, len(names))
	for i, n := range names {
		out[n] = i + 1
	}
	return out
}

/*
 * sync rebuilds the word index when the registry has changed, and starts a
 * background pass for any embedding it now lacks.
 * desc: Called at the top of every Rank because a plugin activated mid-run adds
 *       tools to a live registry, and a tool that cannot be ranked is one the
 *       planner will not be shown. Rebuilding the word index over a thousand
 *       short documents is work measured in milliseconds; the embedding it may
 *       trigger is not, and so does not block the caller.
 * param: ctx - cancelled with the caller. Only the fingerprint check and the
 *        word index use it; the embedding pass runs on its own context so a
 *        finished run does not cancel work the next one needs.
 */
func (ix *index) sync(ctx context.Context) {
	// One comparison, not a rebuild. The registry counts its own changes, which
	// is the only way a same-name replacement — a hot reload editing a tool's
	// description — can be seen at all: the list of names is identical before
	// and after, and an index keyed on that list would go on ranking the old
	// description until the process restarted.
	version := ix.reg.Version()

	ix.mu.RLock()
	unchanged := version == ix.version && ix.lexicon != nil
	ix.mu.RUnlock()
	if unchanged {
		return
	}
	names := ix.reg.List()

	docs := make(map[string]string, len(names))
	hashes := make(map[string]string, len(names))
	for _, n := range names {
		tool, ok := ix.reg.Get(n)
		if !ok {
			continue
		}
		doc := document(n, ix.reg.GetSource(n), tool)
		docs[n] = doc
		sum := sha256.Sum256([]byte(doc))
		hashes[n] = hex.EncodeToString(sum[:8])
	}

	ix.mu.Lock()
	// Vectors already held stay held when their document is unchanged; the rest
	// are dropped, so a tool whose description moved never ranks on the old one.
	kept := make(map[string][]float32, len(ix.vectors))
	for n, v := range ix.vectors {
		if _, still := docs[n]; still && hashes[n] == ix.hashes[n] {
			kept[n] = v
		}
	}
	if ix.vectors == nil { // first sync — whatever a previous run left on disk
		kept = loadVectors(ix.dir, ix.model, hashes)
	}
	if kept == nil {
		// Never nil: the background pass writes into this map, and a nil one
		// would panic there rather than here, on a goroutine, at a boot with an
		// empty store — which is every first boot.
		kept = map[string][]float32{}
	}
	ix.names, ix.docs, ix.hashes = names, docs, hashes
	ix.lexicon = newLexicon(docs)
	ix.vectors = kept
	ix.systems = describeSystems(ix.reg, names)
	ix.version = version
	ix.mu.Unlock()

	if ix.embed != nil {
		go ix.fillVectors()
	}
}

// fillVectors embeds whatever has no current vector and stores the result.
// One pass runs at a time; a second caller returns rather than queueing, and
// the next sync picks up anything it missed.
func (ix *index) fillVectors() {
	if !ix.embedding.TryLock() {
		return
	}
	defer ix.embedding.Unlock()

	ix.mu.RLock()
	docs, have := ix.docs, ix.vectors
	ix.mu.RUnlock()

	got := embedMissing(context.Background(), ix.embed, docs, have)
	if len(got) == 0 {
		return
	}

	ix.mu.Lock()
	for n, v := range got {
		if _, still := ix.docs[n]; still {
			ix.vectors[n] = v
		}
	}
	hashes, vectors := ix.hashes, ix.vectors
	ix.mu.Unlock()

	saveVectors(ix.dir, ix.model, hashes, vectors)
}

// document is the text a tool is found by.
//
// More than the description, which is one line and collides with every other
// one-line description once a registry is large. The name is included twice —
// as written and split on its separators — because `jira_create_issue` is three
// words to a person and one term to an index.
func document(name, source string, tool toolapi.Tool) string {
	var sb strings.Builder
	sb.WriteString(name)
	// The split name only when splitting changed it. A name with no separator
	// in it — git, bash, compute — is otherwise written twice, and a document
	// two-thirds of which is one repeated word matches things the tool has
	// nothing to do with.
	if split := strings.NewReplacer("_", " ", "-", " ").Replace(name); split != name {
		sb.WriteString(" ")
		sb.WriteString(split)
	}
	if source != "" {
		sb.WriteString(" ")
		sb.WriteString(source)
	}
	sb.WriteString("\n")
	sb.WriteString(tool.Description())
	sb.WriteString("\n")
	sb.WriteString(paramText(tool.Parameters()))
	return sb.String()
}

func sortStrings(s []string) { sort.Strings(s) }
