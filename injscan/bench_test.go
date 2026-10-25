package injscan

import (
	"os"
	"strings"
	"testing"
)

// BenchmarkScoreLines scores testdata/bench.txt (one line each) as one source, runs included.
func BenchmarkScoreLines(b *testing.B) {
	m, err := Load("model")
	if err != nil {
		b.Skip(err)
	}
	raw, err := os.ReadFile("testdata/bench.txt")
	if err != nil {
		b.Skip(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.ScoreLines(lines, Log)
	}
	b.ReportMetric(float64(b.Elapsed().Microseconds())/float64(b.N*len(lines)), "µs/line")
}
