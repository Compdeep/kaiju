// Unified dispatcher for the pre-integration eval suite. Not part of
// `go test ./...`. These run against a real LLM, spend tokens, and take
// minutes — they are meant to be invoked explicitly when a developer
// considers a day's work shippable, not on every save.
//
// Usage:
//
//	go run ./tests/eval/cmd -kind=editor [-tier=core|extended|all] [-only=<sub>]
//	go run ./tests/eval/cmd -kind=pipeline [-timeout=6m] [-keep]
//	go run ./tests/eval/cmd -kind=holmes  [-only=<name>] [-keep]
//	go run ./tests/eval/cmd -kind=all     [-tier=core]
//
// Common flags:
//
//	-kaiju=<path>     kaiju binary (built with `go build -o kaiju ./cmd/kaiju`)
//	-config=<path>    kaiju.json (default ./kaiju.json)
//	-timeout=<dur>    per-suite timeout
//	-keep             keep tmp workdirs
//	-dry              editor only — print discovered scenarios and exit
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/Compdeep/kaiju/tests/eval/editor"
	"github.com/Compdeep/kaiju/tests/eval/holmes"
	"github.com/Compdeep/kaiju/tests/eval/pipeline"
)

func main() {
	kind := flag.String("kind", "", "which suite: editor | pipeline | holmes | all")
	kaijuBin := flag.String("kaiju", "./kaiju", "path to kaiju binary (pipeline + holmes)")
	configPath := flag.String("config", "kaiju.json", "path to kaiju.json")
	only := flag.String("only", "", "editor: substring fixture filter; holmes: scenario name")
	tier := flag.String("tier", "all", "editor: core | extended | all")
	timeout := flag.Duration("timeout", 0, "per-suite timeout (0 = suite default)")
	dry := flag.Bool("dry", false, "editor: parse scenarios and print count only")
	keep := flag.Bool("keep", false, "pipeline + holmes: keep tmp workdirs after run")
	report := flag.String("report", "tests/eval/editor/report.md", "editor: report output path")
	corpus := flag.String("corpus", "tests/eval/editor/corpus", "editor: corpus root")
	flag.Parse()

	if *kind == "" {
		flag.Usage()
		log.Fatalf("-kind is required")
	}

	switch *kind {
	case "editor":
		os.Exit(runEditor(*configPath, *corpus, *report, *only, *tier, *dry, *timeout))
	case "pipeline":
		os.Exit(runPipeline(*kaijuBin, *configPath, *timeout, *keep))
	case "holmes":
		os.Exit(runHolmes(*kaijuBin, *configPath, *only, *timeout, *keep))
	case "all":
		fmt.Println("━━━ editor ━━━")
		ec := runEditor(*configPath, *corpus, *report, *only, *tier, *dry, *timeout)
		fmt.Println("━━━ pipeline ━━━")
		pc := runPipeline(*kaijuBin, *configPath, *timeout, *keep)
		fmt.Println("━━━ holmes ━━━")
		hc := runHolmes(*kaijuBin, *configPath, *only, *timeout, *keep)
		fmt.Printf("\n── dispatcher summary ──\n  editor   exit %d\n  pipeline exit %d\n  holmes   exit %d\n", ec, pc, hc)
		if ec != 0 || pc != 0 || hc != 0 {
			os.Exit(1)
		}
	default:
		log.Fatalf("unknown -kind %q (want editor, pipeline, holmes, all)", *kind)
	}
}

func runEditor(configPath, corpus, report, only, tier string, dry bool, timeout time.Duration) int {
	return editor.Run(editor.Options{
		ConfigPath: configPath,
		CorpusDir:  corpus,
		ReportOut:  report,
		Only:       only,
		Tier:       tier,
		Dry:        dry,
		Timeout:    timeout,
	})
}

func runPipeline(kaijuBin, configPath string, timeout time.Duration, keep bool) int {
	return pipeline.Run(pipeline.Options{
		KaijuBin:   kaijuBin,
		ConfigPath: configPath,
		Timeout:    timeout,
		Keep:       keep,
	})
}

func runHolmes(kaijuBin, configPath, only string, timeout time.Duration, keep bool) int {
	return holmes.Run(holmes.Options{
		KaijuBin:   kaijuBin,
		ConfigPath: configPath,
		Only:       only,
		Timeout:    timeout,
		Keep:       keep,
	})
}
