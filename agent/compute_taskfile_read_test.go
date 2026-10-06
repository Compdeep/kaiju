package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Compdeep/kaiju/internal/workspace"
)

// A task file outside the workspace must be readable.
//
// The coder decides between editing and rewriting by whether it can read the file
// it was given. That read used filepath.Join(workspace, taskFile), and Join does not
// honour an absolute second argument — it concatenates. So a file at
// /srv/app/platform/main.ts was looked for at
// <workspace>/srv/app/platform/main.ts, was not found, and the coder was
// told to write from scratch a file it was never shown.
//
// Measured on one session: 38 coder calls, not one of them shown the file it was
// changing. One replaced an Express server with its own idea of one and dropped four
// of its five routes.
//
// workspace.Resolve is the function the write path already used, and it returns an
// absolute path outside the workspace unchanged.
func TestTaskFileOutsideTheWorkspaceResolvesToItself(t *testing.T) {
	// Two separate trees: the sandbox, and a repository elsewhere on the machine.
	ws := t.TempDir()
	elsewhere := t.TempDir()
	target := filepath.Join(elsewhere, "main.ts")
	const body = "import express from \"express\";\napp.use(\"/api/objectives\", Objectives);\n"
	if err := os.WriteFile(target, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	// What the old code did.
	joined := filepath.Join(ws, target)
	if _, err := os.ReadFile(joined); err == nil {
		t.Fatalf("filepath.Join produced a readable path (%s) — this test no longer "+
			"demonstrates the failure it exists for", joined)
	}

	// What it does now.
	resolved, err := workspace.Resolve(ws, target)
	if err != nil {
		t.Fatalf("Resolve refused a path outside the workspace: %v", err)
	}
	if resolved != target {
		t.Errorf("Resolve returned %s, want the path unchanged at %s", resolved, target)
	}
	got, err := os.ReadFile(resolved)
	if err != nil {
		t.Fatalf("the resolved path cannot be read: %v", err)
	}
	if string(got) != body {
		t.Error("the content read back is not the file's own")
	}
}

// A path inside the workspace still goes through the sandbox rules, so spelling a
// workspace path in full is not a way around them.
func TestTaskFileInsideTheWorkspaceStillObeysTheSandbox(t *testing.T) {
	ws := t.TempDir()
	inside := filepath.Join(ws, "project", "app.go")
	if err := os.MkdirAll(filepath.Dir(inside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inside, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	resolved, err := workspace.Resolve(ws, inside)
	if err != nil {
		t.Fatalf("Resolve refused a path inside the workspace: %v", err)
	}
	if _, err := os.ReadFile(resolved); err != nil {
		t.Errorf("a workspace file spelled in full cannot be read: %v", err)
	}
}
