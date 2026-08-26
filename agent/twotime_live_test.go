//go:build live

package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

// Does the shell fixer actually fix a broken command? Run with:
//
//	go test ./agent/ -tags live -run TestLiveTwotimeFix -v
//
// Behind a build tag because it calls a real model and costs money.
//
// Cases are real shell mistakes: a wrong flag, a wrong predicate, a quoting
// fault, a missing pipe stage. Plus errors no rewrite can fix, where the
// correct answer is to return the command unchanged — the fixer must not
// "succeed" by narrowing the question.

func askShellFix(t *testing.T, a *Agent, objective, command, errMsg string) string {
	t.Helper()
	resp, err := a.completeLight(context.Background(), &llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: shellFixSystemPrompt},
			{Role: "user", Content: shellFixPrompt(objective, command, errMsg)},
		},
		Temperature: 0.0,
		MaxTokens:   256,
	})
	if err != nil || len(resp.Choices) == 0 {
		t.Fatalf("model call failed: %v", err)
	}
	return cleanShellFix(resp.Choices[0].Message.Content)
}

func TestLiveTwotimeFix(t *testing.T) {
	a := liveAgent(t)

	cases := []struct {
		name, objective, command, errMsg string
		wantAll                          []string
		wantNone                         []string
		unchanged                        bool
	}{
		// ── networking: nmap / nc / lsof / ss / dig / whois ───────────────
		{"nmap bad flag", "scan the top 100 ports on 10.0.0.5",
			`nmap -top-ports 100 10.0.0.5`, "nmap: unrecognized option '-top-ports'",
			[]string{"nmap", "10.0.0.5", "100"}, nil, false},
		{"nmap syn without privilege flag order", "syn-scan port 443 on 10.0.0.5",
			`nmap 10.0.0.5 -sS -p443 --privileged=yes`, "nmap: unrecognized option '--privileged=yes'",
			[]string{"nmap", "10.0.0.5"}, nil, false},
		{"nc missing -z", "check whether port 22 on 10.0.0.5 is open",
			`nc 10.0.0.5 22 --scan`, "nc: invalid option -- '-scan'",
			[]string{"nc", "10.0.0.5", "22"}, nil, false},
		{"nc no timeout flag", "test port 443 without hanging",
			`nc -z -timeout 3 10.0.0.5 443`, "nc: invalid option -- 't'",
			[]string{"nc", "443"}, nil, false},
		{"lsof bad long flag", "find what is listening on port 8080",
			`lsof -i :8080 -P -n --listen`, "lsof: unknown option (--listen)",
			[]string{"lsof", "8080"}, nil, false},
		{"lsof wrong pid flag", "list open files for pid 4242",
			`lsof --pid 4242`, "lsof: unknown option (--pid)",
			[]string{"lsof", "4242"}, nil, false},
		{"ss bad long flag", "list listening tcp sockets with their processes",
			`ss -tlnp --processes`, "ss: unrecognized option '--processes'",
			[]string{"ss", "-t"}, nil, false},
		{"dig wrong record syntax", "look up the MX records for example.com",
			`dig example.com -type=MX`, "dig: invalid option: -type=MX",
			[]string{"dig", "example.com", "MX"}, nil, false},
		{"whois bad flag", "look up the registrar for example.com",
			`whois --domain example.com`, "whois: unrecognized option '--domain'",
			[]string{"whois", "example.com"}, nil, false},

		// ── system: ps / df / free / du / top ─────────────────────────────
		{"ps bad format key", "show pid, user and rss for every process",
			`ps -eo pid,user,rrs`, `ps: error: unknown user-defined format specifier "rrs"`,
			[]string{"rss", "pid"}, nil, false},
		{"ps BSD/GNU mix", "list processes sorted by memory",
			`ps -aux --sort=-rss`, "ps: error: conflicting format options",
			[]string{"ps"}, nil, false},
		{"df wrong human flag", "show disk usage in human units",
			`df --human /`, "df: unrecognized option '--human'",
			[]string{"df", "-h"}, nil, false},
		{"du missing summarise", "show the total size of /var/log",
			`du --total-only /var/log`, "du: unrecognized option '--total-only'",
			[]string{"du", "/var/log"}, nil, false},
		{"free wrong unit flag", "show memory in megabytes",
			`free --mega`, "free: unrecognized option '--mega'",
			[]string{"free", "-m"}, nil, false},
		{"top not batch", "capture one snapshot of top for a script",
			`top -n 1`, "top: failed tty get",
			[]string{"top", "-b"}, nil, false},

		// ── web: curl / wget ──────────────────────────────────────────────
		{"curl bad header syntax", "fetch the api with a bearer token",
			`curl -H Authorization: Bearer abc123 https://api.example.com/v1/x`, "curl: (3) URL rejected: Bad hostname",
			[]string{"curl", "api.example.com"}, nil, false},
		{"curl missing -L", "fetch a url that redirects",
			`curl https://example.com/redirect -o out.html`, "curl: saved 0 bytes; response was 301 with no body",
			[]string{"curl", "-L"}, nil, false},
		{"wget wrong output flag", "download the tarball to /tmp",
			`wget https://example.com/x.tar.gz --output /tmp/x.tar.gz`, "wget: unrecognized option '--output'",
			[]string{"wget", "/tmp/x.tar.gz"}, nil, false},

		// ── files / text ──────────────────────────────────────────────────
		{"find wrong predicate", "list every .conf under /etc",
			`find /etc -nam '*.conf'`, "find: unknown predicate `-nam'",
			[]string{"-name", "/etc"}, nil, false},
		{"find missing -exec terminator", "delete every .tmp under /var/tmp",
			`find /var/tmp -name '*.tmp' -exec rm {}`, "find: missing argument to `-exec'",
			[]string{"-exec", "rm"}, nil, false},
		{"grep on a directory", "search the whole tree for TODO",
			`grep TODO /home/user/project`, "grep: /home/user/project: Is a directory",
			[]string{"grep", "TODO"}, nil, false},
		{"uniq without sort", "count the unique users in the auth log",
			`cut -d' ' -f1 /var/log/auth.log | uniq -c`, "uniq: counts are wrong; input is not sorted",
			[]string{"sort", "uniq", "auth.log"}, nil, false},
		{"tar wrong flag", "extract archive.tar.gz into /tmp",
			`tar -xzf archive.tar.gz -D /tmp`, "tar: invalid option -- 'D'",
			[]string{"tar", "archive.tar.gz", "/tmp"}, nil, false},

		// ── must NOT be rewritten ─────────────────────────────────────────
		{"permission denied", "read the shadow file",
			`cat /etc/shadow`, "cat: /etc/shadow: Permission denied",
			nil, []string{"sudo", "/tmp"}, true},
		{"host unresolvable", "fetch the config from the build host",
			`curl http://buildhost.internal/config.json`, "curl: (6) Could not resolve host: buildhost.internal",
			nil, []string{"localhost", "127.0.0.1"}, true},
		{"docker daemon down", "list running containers",
			`docker ps`, "Cannot connect to the Docker daemon at unix:///var/run/docker.sock",
			nil, []string{"sudo"}, true},
	}

	var fixed, held, wrong int
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := askShellFix(t, a, tc.objective, tc.command, tc.errMsg)
			t.Logf("%s\n      → %s", tc.command, got)

			if tc.unchanged {
				// Empty and unchanged are the same answer: the caller does
				// `if fixed == "" || fixed == command` and keeps the original
				// error either way. Both mean "no rewrite fixes this".
				if got != "" && got != tc.command {
					wrong++
					t.Errorf("rewrote an unfixable command\n  was: %s\n  got: %s", tc.command, got)
					return
				}
				held++
				return
			}
			if got == "" || got == tc.command {
				wrong++
				t.Errorf("no fix produced for: %s", tc.command)
				return
			}
			for _, w := range tc.wantAll {
				if !strings.Contains(got, w) {
					wrong++
					t.Errorf("fix dropped %q — it no longer serves the objective %q\n  got: %s", w, tc.objective, got)
					return
				}
			}
			for _, w := range tc.wantNone {
				if strings.Contains(got, w) {
					wrong++
					t.Errorf("fix introduced %q, changing what the command asks\n  got: %s", w, got)
					return
				}
			}
			fixed++
		})
	}
	t.Logf("fixed %d, correctly held %d, wrong %d, of %d", fixed, held, wrong, len(cases))
}
