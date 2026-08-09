package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
	agenttools "github.com/Compdeep/kaiju/agent/tools"
)

// collectGaps is the content-agnostic code half of the coverage edge: it must
// flag empty/error tool bodies and failed nodes, and leave successful ones alone.
func TestCollectGaps(t *testing.T) {
	g := NewGraph()

	okID := g.AddNode(&Node{Type: NodeTool, Tag: "fetch_ok", ToolName: "web_fetch"})
	g.SetBody(okID, toolMessageBody{msg: agenttools.ToolOK("page", "content", nil)})

	emID := g.AddNode(&Node{Type: NodeTool, Tag: "search_x", ToolName: "web_search"})
	g.SetBody(emID, toolMessageBody{msg: agenttools.ToolEmpty("search", "no reachable results")})

	erID := g.AddNode(&Node{Type: NodeTool, Tag: "fetch_bad", ToolName: "web_fetch"})
	g.SetBody(erID, toolMessageBody{msg: agenttools.ToolFail("page", "HTTP 404", nil)})

	failID := g.AddNode(&Node{Type: NodeTool, Tag: "bash_x", ToolName: "bash"})
	g.SetError(failID, fmt.Errorf("boom"))

	gaps := groundingAgent().collectGaps(g)
	if len(gaps) != 3 {
		t.Fatalf("collectGaps = %d, want 3 (empty + error + failed, NOT ok): %+v", len(gaps), gaps)
	}
	for _, gp := range gaps {
		if gp.Tag == "fetch_ok" {
			t.Fatalf("a successful tool must not be reported as a gap")
		}
	}
}

// A clean run (no empty/error/failed nodes) must skip the edge entirely so the
// common path pays nothing — the LLM lane is never touched.
func TestCoverageEdge_CleanRunSkips(t *testing.T) {
	g := NewGraph()
	id := g.AddNode(&Node{Type: NodeTool, Tag: "ok", ToolName: "web_fetch"})
	g.SetBody(id, toolMessageBody{msg: agenttools.ToolOK("page", "content", nil)})

	if cov := groundingAgent().coverageEdge(nil, g, "some evidence"); cov != "" {
		t.Fatalf("clean run should skip the edge (return \"\"), got %q", cov)
	}
}

// The one safety property of the whole edge: when gathering left gaps but the
// light LLM lane is unavailable (executor nil on a zero Agent), the edge must
// STILL hand the answer-writer an explicit "## Coverage" absence block — never
// return "". A regression to "" here lets the aggregator fabricate the missing
// data, the exact failure the edge exists to prevent.
func TestCoverageEdge_FailOpenToStructural(t *testing.T) {
	g := NewGraph()
	emID := g.AddNode(&Node{Type: NodeTool, Tag: "search_x", ToolName: "web_search"})
	g.SetBody(emID, toolMessageBody{msg: agenttools.ToolEmpty("search", "no reachable results")})
	erID := g.AddNode(&Node{Type: NodeTool, Tag: "fetch_bad", ToolName: "web_fetch"})
	g.SetBody(erID, toolMessageBody{msg: agenttools.ToolFail("page", "HTTP 404", nil)})

	cov := groundingAgent().coverageEdge(context.Background(), g, "REQUEST + EVIDENCE")
	if cov == "" {
		t.Fatal("gaps present but edge returned \"\" — the aggregator gets no absence signal and may fabricate")
	}
	if !strings.HasPrefix(cov, "## Coverage") {
		t.Fatalf("fail-open output must be the structural ## Coverage block, got: %q", cov)
	}
	for _, want := range []string{"search_x", "fetch_bad"} {
		if !strings.Contains(cov, want) {
			t.Fatalf("structural block missing gap %q:\n%s", want, cov)
		}
	}
}

// The generative half: with a light lane available, the edge runs the generator
// (prompt.CoverageGen) and prepends its checklist under the ## Coverage header.
func TestCoverageEdge_GeneratesFromLLM(t *testing.T) {
	const checklist = "BACKED: none\nNOT BACKED: the annual revenue figure"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":`+strconv.Quote(checklist)+`}}],"usage":{"total_tokens":1}}`)
	}))
	defer srv.Close()

	a := &Agent{executor: llm.NewClient(srv.URL, "k", "test-light")}
	g := NewGraph()
	id := g.AddNode(&Node{Type: NodeTool, Tag: "search_x", ToolName: "web_search"})
	g.SetBody(id, toolMessageBody{msg: agenttools.ToolEmpty("search", "no results")})

	cov := a.coverageEdge(context.Background(), g, "REQUEST: revenue?\nEVIDENCE: none")
	if !strings.HasPrefix(cov, "## Coverage") {
		t.Fatalf("generated output must carry the ## Coverage header, got: %q", cov)
	}
	if !strings.Contains(cov, "NOT BACKED: the annual revenue figure") {
		t.Fatalf("generated checklist not prepended:\n%s", cov)
	}
}

// fetchedNode records a web_fetch that actually retrieved a URL — the URL is
// stamped into the page envelope's Data, which collectFetched reads.
func fetchedNode(g *Graph, tag, url string) {
	// The URL goes in the node's PARAMS, which is what marks a handle as
	// followed — the dispatcher resolves a ${node...} template in place before
	// execution, so a real fetch node holds the value it was called with. It is
	// also in the output, as the real tool stamps it there, but nothing reads
	// that any more.
	id := g.AddNode(&Node{
		Type: NodeTool, Tag: tag, ToolName: "web_fetch",
		Params: map[string]any{"url": url},
	})
	g.SetBody(id, toolMessageBody{msg: agenttools.ToolOK("page", "content", map[string]any{"url": url})})
}

// Part 2 of the coverage edge: even with NO gaps (every search returned ok), if a
// search surfaced URLs that no fetch ever read, the aggregator must be told they
// were referenced-but-not-retrieved — so it can't present them as read/verified.
// This is the exact failure that produced a "verified and live" URL list.
func TestCoverageEdge_ReferencedButNotRetrieved(t *testing.T) {
	g := NewGraph()
	searchNode(g, "s1", "https://ref.example/a", "https://ref.example/b") // ok search, no gap
	cov := groundingAgent().coverageEdge(context.Background(), g, "get me 10 real sources")
	if cov == "" {
		t.Fatal("unretrieved references present but edge returned \"\" — aggregator gets no signal and may claim them verified")
	}
	if !strings.Contains(cov, "Referenced but not retrieved") {
		t.Fatalf("expected the referenced-but-not-retrieved block, got:\n%s", cov)
	}
	for _, u := range []string{"https://ref.example/a", "https://ref.example/b"} {
		if !strings.Contains(cov, u) {
			t.Fatalf("block must list the unretrieved reference %q:\n%s", u, cov)
		}
	}
}

// A URL that WAS retrieved must not be flagged — and if nothing else is amiss the
// edge stays silent (no false "unverified" label on a source we actually read).
func TestCoverageEdge_RetrievedUrlNotFlagged(t *testing.T) {
	g := NewGraph()
	searchNode(g, "s1", "https://read.example/a")
	fetchedNode(g, "f1", "https://read.example/a") // same URL, actually read
	if cov := groundingAgent().coverageEdge(context.Background(), g, "req"); cov != "" {
		t.Fatalf("a retrieved URL (no gaps) should leave the edge silent, got:\n%s", cov)
	}
}

// Mixed: one reference read, one not — only the unread one is flagged.
func TestCoverageEdge_FlagsOnlyUnretrieved(t *testing.T) {
	g := NewGraph()
	searchNode(g, "s1", "https://x.example/read", "https://x.example/unread")
	fetchedNode(g, "f1", "https://x.example/read")
	cov := groundingAgent().coverageEdge(context.Background(), g, "req")
	if !strings.Contains(cov, "https://x.example/unread") {
		t.Fatalf("the unread reference must be flagged:\n%s", cov)
	}
	if strings.Contains(cov, "https://x.example/read") {
		t.Fatalf("the reference that was actually read must NOT be flagged:\n%s", cov)
	}
}

// collectGaps stays content-agnostic: a nil graph is a no-op, and a tool node
// not yet on the envelope protocol (a RawTextBody) carries no structural status,
// so it must be skipped — not panicked on — as new body types flow through.
func TestCollectGaps_SkipsNonEnvelopeAndNilGraph(t *testing.T) {
	if gaps := groundingAgent().collectGaps(nil); gaps != nil {
		t.Fatalf("nil graph should yield no gaps, got %+v", gaps)
	}
	g := NewGraph()
	id := g.AddNode(&Node{Type: NodeTool, Tag: "legacy", ToolName: "web_fetch"})
	g.SetBody(id, RawTextBody{Text: "some opaque result"})
	if gaps := groundingAgent().collectGaps(g); len(gaps) != 0 {
		t.Fatalf("a non-envelope tool body must be skipped, got %+v", gaps)
	}
}
