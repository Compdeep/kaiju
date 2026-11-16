package configapi

import (
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

// qwen3.6 is sampled as Qwen publishes for thinking: temperature 0.6, top_p
// 0.95, top_k 20, presence penalty 1.5. At the engine's temperature 0.3 its plans repeated to the
// cap (see llm.Sampling).
func TestQwen36CarriesItsPublishedSampling(t *testing.T) {
	f, ok := Facts("qwen/qwen3.6-35b-a3b")
	if !ok {
		t.Fatal("the catalog does not carry qwen/qwen3.6-35b-a3b")
	}
	// With the presence penalty Qwen gives for thinking on general tasks, "to
	// reduce endless repetitions" (llm.Sampling).
	want := llm.Sampling{Temperature: 0.6, TopP: 0.95, TopK: 20, PresencePenalty: 1.5}
	if f.Thinking.Sampling != want {
		t.Errorf("qwen3.6 reaches the client with sampling %+v, want %+v", f.Thinking.Sampling, want)
	}
}
