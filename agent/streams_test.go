package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
	"time"
)

// A stage that writes something a person reads must stream it.
//
// The chat node did not. It used completeHeavy — one blocking call — so a
// conversational turn produced nothing on screen until the whole reply had been
// generated, then arrived at once. The other chat entry point streamed on the
// same channel the whole time, which is why this looked like a frontend problem
// and was not: the frontend was listening correctly to a path that sent nothing.
//
// Read from the source because the property is which call a stage makes, and a
// test that drives the stage would need a streaming provider to tell the two
// apart.
func TestEveryStageThatAnswersAPersonStreams(t *testing.T) {
	// The files whose whole job is producing the reply a user reads.
	answering := map[string]string{
		"chat_node.go":  "the conversational turn, and the reply that supersedes it",
		"aggregator.go": "the synthesised answer",
		"chat.go":       "the standalone chat lane",
	}

	fset := token.NewFileSet()
	pkg, err := parser.ParseDir(fset, ".", func(f fs.FileInfo) bool {
		return !strings.HasSuffix(f.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse the package: %v", err)
	}

	for _, p := range pkg {
		for path, file := range p.Files {
			base := path[strings.LastIndex(path, "/")+1:]
			why, watched := answering[base]
			if !watched {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch sel.Sel.Name {
				case "completeHeavy", "completeLight", "completeHeavyChecked", "completeLightChecked", "ask", "askParsed":
					t.Errorf("%s (%s) answers with %s, which returns the whole reply at once. "+
						"Nothing appears on screen until the model has finished, and then all of "+
						"it does. Use askStream or askStreamResp and broadcast each chunk as an "+
						"\"outcome\" event.", base, why, sel.Sel.Name)
				}
				return true
			})
		}
	}
}

// The event name the frontend listens for. It listened for "verdict", which
// nothing has ever sent, so no chunk arrived and the reply appeared in one piece
// when the POST returned — the same symptom as not streaming at all, from the
// other end.
//
// Tested on the mapping rather than on the source. It was a grep for one line
// in three files, which is what a mapping written out three times leaves you
// with; there is one now, so the behaviour itself can be checked.
func TestStreamedChunksAreBroadcastAsOutcome(t *testing.T) {
	a := &Agent{dagSubs: map[int]dagSub{}}
	ch, _, unsub := a.SubscribeDAG()
	defer unsub()

	send := a.streamTo(nil, "sess")
	send("the answer", "content")
	send("thinking about it", "reasoning")

	// Chunks are held briefly and sent together now, so this waits rather than
	// reading what happens to be there — and reads them as a set, because the
	// answer and the thinking are held apart and their timers are independent.
	want := map[string]string{"outcome": "the answer", "reasoning": "thinking about it"}
	got := map[string]string{}
	deadline := time.After(2 * time.Second)
	for len(got) < len(want) {
		select {
		case ev := <-ch:
			got[ev.Type] = ev.Text
			if ev.SessionID != "sess" {
				t.Errorf("chunk carried session %q, want the one it was streamed for", ev.SessionID)
			}
		case <-deadline:
			t.Fatalf("only %v arrived, want %v", got, want)
		}
	}
	for typ, text := range want {
		if got[typ] != text {
			t.Errorf("%s arrived as %q, want %q", typ, got[typ], text)
		}
	}
}

// A lane with nowhere to send them does not send them. Converse answers a turn
// with no session when a caller asks for one directly, and a broadcast with no
// destination is an event every open trace receives for a run it is not
// watching.
func TestChunksWithNowhereToGoAreNotBroadcast(t *testing.T) {
	a := &Agent{dagSubs: map[int]dagSub{}}
	ch, _, unsub := a.SubscribeDAG()
	defer unsub()

	a.streamTo(nil, "")("something", "content")
	select {
	case got := <-ch:
		t.Errorf("a chunk with no graph and no session was broadcast: %+v", got)
	default:
	}
}

// Every stage that answers a person goes through the one that streams.
//
// The mapping being in one place is only worth anything if the lanes reach it.
// This is the half a behavioural test cannot see: a lane could stream correctly
// and still pick its own cap, which is the drift writeProse exists to close.
func TestEveryAnsweringStageGoesThroughWriteProse(t *testing.T) {
	for _, c := range []struct{ file, why string }{
		{"chat.go", "the standalone chat lane"},
		{"chat_node.go", "the conversational turn, and the reply that supersedes it"},
		{"aggregator.go", "the synthesised answer"},
	} {
		if src := readSource(t, c.file); !strings.Contains(src, "writeProse(") {
			t.Errorf("%s (%s) does not go through writeProse, so it picks its own reply cap "+
				"and its own streaming — which is how three of these came to disagree about "+
				"how long an answer may be", c.file, c.why)
		}
	}
}
