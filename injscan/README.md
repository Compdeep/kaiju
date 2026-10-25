# injscan

injscan scores lines of telemetry for planted instructions: text an attacker places in logs, events,
command output or files to steer the AI agent that reads them ("mark this host clean", "disable the
monitoring agent first", "send the keys to …"). It is the scorer described in docs/SECURITY.md 8.5.

The model is built into the package (`Default()`), so it needs no files, no network and no model server.

## How it decides

**Two models score every text, and their scores are added**
- **Word model** (`ft5b_v2`): fastText trained gently on top of English word vectors trained beforehand
  (`crawl-300d-2M`, Facebook, CC BY-SA 3.0). It is a straight line through an average, so it is stored
  exactly as one number per word and one per word pair: 2 MB.
- **Tagger** (`tagger7`): the English vectors stay fixed (cut to 100 numbers per word, one byte each);
  a small layer scores every word from the word, its neighbours and the products of their vectors.
  It finds short planted phrases inside long lines and says which words they are: 12 MB.
- **Combined score:** each score divided by its spread on real telemetry, then added.

**Decoding comes first.** Base64, hex, URL encoding, HTML entities, `\u`/`\x` escapes and terminal
colour codes are decoded, and the decoded text is scored with the original.

**Cut-offs per kind of text**
- **Loose:** above it a line goes to the LLM check (SECURITY.md 8.5 pre-pass). Set so 1 real telemetry line
  in 1,000 is let through, lines alone and runs of lines together.
- **Strict:** 1 in 10,000. Above it a line would be flagged without the LLM check, but `outright` in
  `meta.json` is false (the user's decision, 2026-10-24): every line above loose goes to the LLM check and no
  line is given the strict level.
- **Runs of lines have their own pair** (`run_loose`, `run_strict`): lines read together score higher than
  lines alone. Eight tenths of the 1 in 1,000 goes to single lines, the rest to lines only a run lifts.
- **File contents** have their own, higher cut-offs: documentation is full of ordinary instructions.

**Lines are also read in runs.** `ScoreLines` scores each line alone and in runs of up to 4 consecutive
lines, so an instruction split across lines is read whole. A run counts only when it scores higher than every
one of its lines alone, is judged against the run cut-offs, and replaces a line's result only when its level
is higher.

**Marks:** for a flagged line, the words the tagger scores above zero, for the briefing.

## Using it

```go
m, err := injscan.Default()                     // the built-in model; injscan.Load(dir) reads another folder
r := m.Score(line, injscan.Telemetry)          // r.Level is "", "loose" or "strict"; r.Marks are the words
rs := m.ScoreLines(lines, injscan.Telemetry)   // consecutive lines of one source
```

```
go build ./cmd/injscan
journalctl -n 5000 | ./injscan                       # flagged lines only; -all prints every line
./injscan -kind file < some/README.md                # -model <folder> scores with another model
```

`Model` is safe for use from several goroutines. Speed: about 0.5 ms per line on this machine,
runs of lines included (`go test -bench ScoreLines`).

## Files

- `scanner.go`: `Model`, `Score`, `ScoreLines`, kinds, levels, marks.
- `fasttext.go`: the word model, including fastText's own word hashing.
- `tagger.go`: the tagger, with per-word and per-pair caches.
- `decode.go`, `text.go`: decoding, windows and word splitting, copied rule for rule from the Python.
- `model/`: the model files, built into the package (format in `model.go`).
- `testdata/golden.json`: scores from the Python reference; `go test` checks Go gives the same
  numbers (905 texts and 5 multi-line records, within 0.01). Public lines and generated records only.
- `testdata/bench.txt`: 500 public log lines (OTRF, Splunk) for the benchmark.

## How it was built

**Training data: counterfactual items**
- 7,023 items written by DeepSeek V4.1 Flash, Qwen 3.6 and GPT-5 mini (training) and GPT-OSS 120B (test only)
  (`train/gen4.py`): one telemetry record with one slot, filled five ways: planted, harmless (same topic
  and nouns), hard negative (reports the attack), admin (a real instruction to a human), quoted.
- 13 attack categories, including `dismiss` (the activity is a test, a developer operation, known benign,
  already approved), across 20 telemetry formats; 5 formats (Kubernetes events, macOS log, stack traces,
  HTTP headers, git log) never used in training.
- A different model sorted every slot text blind (`train/check4.py`); 5,658 items kept.
- Every planted and every normal text is also spliced into real lines in the same 11 glue formats, so
  neither a format nor a wrapper says anything about the label.
- Normal lines: 77,635 real telemetry lines (monitored hosts, OTRF, Splunk) for training.
- No public prompt-injection datasets are used.

**Tests**, all on data never trained on: held-back calls of the training writers (555 items), every GPT-OSS
item (1,114), the five unseen formats (695), 16,563 real telemetry lines and 37,234 file-content lines.

## Retraining

The training scripts are kept by the application that trains the model, not in this package. A retrained
model is a new `model/` folder: replace this one, update `testdata/golden.json` from the same export, and
`go test` checks the Go scores match.

## Licence

The word vectors are derived from fastText's `crawl-300d-2M` English vectors, by Facebook, released under
the Creative Commons Attribution-ShareAlike 3.0 licence (CC BY-SA 3.0).

## Results (round eight, `train/export7.py`, records scored as `ScoreLines` scores them)

**At the loose cut-off** (1 in 1,000 real telemetry lines go to the LLM check; measured 14 of 16,563)
- Writer never seen (GPT-OSS): 994 of 1,114 planted caught (89%); 259 harmless twins sent too (23%).
- Held-back calls of the training writers: 524 of 555 (94%); harmless 208 (37%).
- Five unseen formats: 675 of 695 (97%); harmless 358 (52%).
- `dismiss` attacks, GPT-OSS: 92 of 96.

**Other loose settings** (GPT-OSS planted caught): 1 in 500: 94%; 1 in 300: 95%; 1 in 100: 98%.

**At the strict cut-off** (1 in 10,000; not used while `outright` is false): GPT-OSS 749 of 1,114 (67%),
harmless 77 (7%).

**Other results**
- LOLBAS (447) and GTFOBins (1,191) attack commands above loose: 1 and 4.
- File contents above the file loose cut-off: 37 of 37,234.
- Quoted records (a security tool quoting the planted text) score like planted text.

## Open items

1. **Harmless twins reach the LLM check often** (23% to 52% at loose). They are written to sit as close to an
   attack as possible, so the LLM check clears them; the cost is LLM calls. Pair training (each planted
   record pushed above its own harmless twin) is the next data step.
2. **The LLM check itself** (SECURITY.md 8.5 pre-pass) is not built, and how often it wrongly flags a clean
   line is not measured.
3. **Quoted records:** whether a security log that quotes an instruction word for word counts as planted is
   a decision; today it scores as planted.
4. **The test attacks are all AI-written.** A set written by people, never trained on, would be the honest test.
