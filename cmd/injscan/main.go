// Command injscan scores lines of text from standard input for planted instructions and prints the
// flagged ones (or every line with -all).
//
//	journalctl -n 5000 | injscan -model ../../model
//	injscan -model ../../model -kind file < README.md
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Compdeep/kaiju/injscan"
)

func main() {
	dir := flag.String("model", "", "model folder; empty uses the built-in model")
	kind := flag.String("kind", "log", "log or file: chooses the cut-offs")
	all := flag.Bool("all", false, "print every line, not only flagged ones")
	flag.Parse()

	m, err := injscan.Default()
	if *dir != "" {
		m, err = injscan.Load(*dir)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "injscan:", err)
		os.Exit(1)
	}
	var lines []string
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	c := m.Cutoffs(injscan.Kind(*kind))
	fmt.Fprintf(os.Stderr, "%d lines; cut-offs for %s: loose %.2f, strict %.2f\n", len(lines), *kind, c.Loose, c.Strict)
	flagged := 0
	for i, r := range m.ScoreLines(lines, injscan.Kind(*kind)) {
		if r.Level != injscan.None {
			flagged++
		}
		if r.Level == injscan.None && !*all {
			continue
		}
		lvl := string(r.Level)
		if lvl == "" {
			lvl = "-"
		}
		line := lines[i]
		if len(line) > 160 {
			line = line[:160] + "..."
		}
		fmt.Printf("%d\t%s\t%.2f\tblock %d\t%s\n", i+1, lvl, r.Score, r.Block, line)
		if len(r.Marks) > 0 {
			w := make([]string, len(r.Marks))
			for j, mk := range r.Marks {
				w[j] = mk.Word
			}
			fmt.Printf("\t\tmarked: %s\n", strings.Join(w, " "))
		}
	}
	fmt.Fprintf(os.Stderr, "%d flagged\n", flagged)
}
