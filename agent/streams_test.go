package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
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
func TestStreamedChunksAreBroadcastAsOutcome(t *testing.T) {
	for _, file := range []string{"chat_node.go", "aggregator.go", "chat.go"} {
		src := readSource(t, file)
		if !strings.Contains(src, `evType := "outcome"`) {
			t.Errorf("%s does not broadcast its chunks as \"outcome\" — the client listens "+
				"for that name and will show nothing until the call returns", file)
		}
	}
}
