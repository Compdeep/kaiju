package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── Test fixtures ───────────────────────────────────────────────────────────

const testGoFile = `package main

import "fmt"

// greet returns a greeting string.
func greet(name string) string {
	return fmt.Sprintf("Hello, %s!", name)
}

// Server handles HTTP requests.
type Server struct {
	port int
	name string
}

// Start begins listening on the configured port.
func (s *Server) Start() error {
	fmt.Printf("Listening on :%d\n", s.port)
	return nil
}

func main() {
	s := &Server{port: 8080, name: "kaiju"}
	s.Start()
	fmt.Println(greet("world"))
}
`

const testJSFile = `// Authentication module
const bcrypt = require('bcrypt');

/**
 * Hash a password with bcrypt.
 */
function hashPassword(password) {
  return bcrypt.hashSync(password, 10);
}

// Arrow function
const verifyToken = async (token) => {
  if (!token) {
    return { valid: false, error: "no token" };
  }
  // This brace } should not confuse the counter
  console.log("checking token: {" + token + "}");
  return { valid: true };
};

class AuthService {
  constructor(secret) {
    this.secret = secret;
  }

  login(email, password) {
    const hash = hashPassword(password);
    return { email, hash };
  }
}

module.exports = { hashPassword, verifyToken, AuthService };
`

const testPyFile = `import os

def greet(name):
    """Return a greeting."""
    return f"Hello, {name}!"

class UserModel:
    def __init__(self, db):
        self.db = db

    def find_by_email(self, email):
        """Find a user by email."""
        query = "SELECT * FROM users WHERE email = ?"
        return self.db.execute(query, (email,))

    def create(self, email, name):
        """Create a new user."""
        self.db.execute(
            "INSERT INTO users (email, name) VALUES (?, ?)",
            (email, name),
        )
        return True

def main():
    print(greet("world"))
`

const testVueFile = `<template>
  <div class="hero">
    <h1>{{ title }}</h1>
    <p>{{ subtitle }}</p>
  </div>
</template>

<script setup>
import { ref, computed } from 'vue'

const title = ref('Kaiju')
const subtitle = ref('AI Agent')

const fullTitle = computed(() => {
  return title.value + ' - ' + subtitle.value
})

function handleClick() {
  console.log("clicked")
}
</script>

<style scoped>
.hero { padding: 2rem; }
</style>
`

const testRubyFile = `class Animal
  def initialize(name)
    @name = name
  end

  def speak
    "..."
  end
end

class Dog < Animal
  def speak
    "Woof!"
  end

  def fetch(item)
    if item == "ball"
      "Fetching ball!"
    else
      "I don't fetch that"
    end
  end
end

def greet(animal)
  puts animal.speak
end
`

// ── ScanFunctionMap tests ───────────────────────────────────────────────────

func setupTestDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(path), 0755)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestScanFunctionMap_Go(t *testing.T) {
	dir := setupTestDir(t, map[string]string{"main.go": testGoFile})
	fm := ScanFunctionMap(dir, 5)

	decls, ok := fm["main.go"]
	if !ok {
		t.Fatal("main.go not found in function map")
	}

	names := map[string]bool{}
	for _, d := range decls {
		names[d.Name] = true
		if d.StartLine == 0 {
			t.Errorf("function %s has StartLine 0", d.Name)
		}
	}

	for _, want := range []string{"greet", "Server", "Start", "main"} {
		if !names[want] {
			t.Errorf("expected function %q not found, got: %v", want, names)
		}
	}
}

func TestScanFunctionMap_GoEndLines(t *testing.T) {
	dir := setupTestDir(t, map[string]string{"main.go": testGoFile})
	fm := ScanFunctionMap(dir, 5)
	decls := fm["main.go"]

	for _, d := range decls {
		if d.EndLine == 0 {
			t.Errorf("function %s has no EndLine (brace counting failed)", d.Name)
		}
		if d.EndLine > 0 && d.EndLine < d.StartLine {
			t.Errorf("function %s has EndLine %d < StartLine %d", d.Name, d.EndLine, d.StartLine)
		}
	}

	// greet: starts at line 6, should end at line 8
	for _, d := range decls {
		if d.Name == "greet" {
			if d.StartLine != 6 {
				t.Errorf("greet StartLine = %d, want 6", d.StartLine)
			}
			if d.EndLine != 8 {
				t.Errorf("greet EndLine = %d, want 8", d.EndLine)
			}
		}
	}
}

func TestScanFunctionMap_JS(t *testing.T) {
	dir := setupTestDir(t, map[string]string{"auth.js": testJSFile})
	fm := ScanFunctionMap(dir, 5)

	decls, ok := fm["auth.js"]
	if !ok {
		t.Fatal("auth.js not found in function map")
	}

	names := map[string]bool{}
	for _, d := range decls {
		names[d.Name] = true
	}

	for _, want := range []string{"hashPassword", "verifyToken", "AuthService"} {
		if !names[want] {
			t.Errorf("expected %q not found, got: %v", want, names)
		}
	}
}

func TestScanFunctionMap_JSBraceInString(t *testing.T) {
	// verifyToken has console.log("checking token: {" + token + "}");
	// The brace counter must skip those.
	dir := setupTestDir(t, map[string]string{"auth.js": testJSFile})
	fm := ScanFunctionMap(dir, 5)
	decls := fm["auth.js"]

	for _, d := range decls {
		if d.Name == "verifyToken" {
			if d.EndLine == 0 {
				t.Error("verifyToken has no EndLine — brace counter confused by string braces")
			}
			// verifyToken starts at line 13, should end around line 21
			if d.EndLine > 0 && d.EndLine < 18 {
				t.Errorf("verifyToken EndLine %d seems too early (brace in string confused counter)", d.EndLine)
			}
		}
	}
}

func TestScanFunctionMap_Python(t *testing.T) {
	dir := setupTestDir(t, map[string]string{"model.py": testPyFile})
	fm := ScanFunctionMap(dir, 5)

	decls, ok := fm["model.py"]
	if !ok {
		t.Fatal("model.py not found in function map")
	}

	names := map[string]bool{}
	for _, d := range decls {
		names[d.Name] = true
	}

	for _, want := range []string{"greet", "UserModel", "__init__", "find_by_email", "create", "main"} {
		if !names[want] {
			t.Errorf("expected %q not found, got: %v", want, names)
		}
	}

	// Check indentation-based end lines
	for _, d := range decls {
		if d.Name == "greet" && d.EndLine == 0 {
			t.Error("greet has no EndLine — indent detection failed")
		}
		if d.Name == "find_by_email" && d.EndLine == 0 {
			t.Error("find_by_email has no EndLine — indent detection failed")
		}
	}
}

func TestScanFunctionMap_Vue(t *testing.T) {
	dir := setupTestDir(t, map[string]string{"Hero.vue": testVueFile})
	fm := ScanFunctionMap(dir, 5)

	decls, ok := fm["Hero.vue"]
	if !ok {
		t.Fatal("Hero.vue not found in function map")
	}

	names := map[string]bool{}
	for _, d := range decls {
		names[d.Name] = true
	}

	// Vue scanner should find functions inside <script setup>
	if !names["fullTitle"] && !names["handleClick"] {
		t.Errorf("expected Vue functions, got: %v", names)
	}
}

func TestScanFunctionMap_Ruby(t *testing.T) {
	dir := setupTestDir(t, map[string]string{"animal.rb": testRubyFile})
	fm := ScanFunctionMap(dir, 5)

	decls, ok := fm["animal.rb"]
	if !ok {
		t.Fatal("animal.rb not found in function map")
	}

	names := map[string]bool{}
	for _, d := range decls {
		names[d.Name] = true
	}

	for _, want := range []string{"Animal", "initialize", "speak", "Dog", "fetch", "greet"} {
		if !names[want] {
			t.Errorf("expected %q not found, got: %v", want, names)
		}
	}

	// Check keyword-based end detection
	for _, d := range decls {
		if d.Name == "fetch" && d.EndLine == 0 {
			t.Error("fetch has no EndLine — keyword end detection failed")
		}
	}
}

func TestScanFunctionMap_SkipDirs(t *testing.T) {
	dir := setupTestDir(t, map[string]string{
		"src/main.go":                    testGoFile,
		"node_modules/pkg/index.js":      testJSFile,
		".git/config":                    "not code",
		"vendor/lib/util.go":             testGoFile,
	})
	fm := ScanFunctionMap(dir, 5)

	if _, ok := fm["node_modules/pkg/index.js"]; ok {
		t.Error("node_modules should be skipped")
	}
	if _, ok := fm["vendor/lib/util.go"]; ok {
		t.Error("vendor should be skipped")
	}
	if _, ok := fm[filepath.Join("src", "main.go")]; !ok {
		t.Error("src/main.go should be found")
	}
}

func TestScanFunctionMap_DepthLimit(t *testing.T) {
	dir := setupTestDir(t, map[string]string{
		"a/b/c/d/e/f/deep.go": testGoFile,
		"a/b/shallow.go":      testGoFile,
	})
	fm := ScanFunctionMap(dir, 3)

	if _, ok := fm[filepath.Join("a", "b", "shallow.go")]; !ok {
		t.Error("a/b/shallow.go should be found within depth 3")
	}
	if _, ok := fm[filepath.Join("a", "b", "c", "d", "e", "f", "deep.go")]; ok {
		t.Error("a/b/c/d/e/f/deep.go should be beyond depth 3")
	}
}


// ── ApplyEdits tests (text-match replacement) ──────────────────────────────

func TestApplyEdits_SingleMatch(t *testing.T) {
	content := "hello world\nfoo bar\nbaz qux"
	edits := []EditOp{{OldContent: "foo bar", NewContent: "FOO BAR"}}
	result, err := ApplyEdits(content, edits)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "hello world\nFOO BAR\nbaz qux" {
		t.Errorf("got %q", result)
	}
}

func TestApplyEdits_MultipleMatches(t *testing.T) {
	content := "aaa\nbbb\nccc\nddd"
	edits := []EditOp{
		{OldContent: "bbb", NewContent: "BBB"},
		{OldContent: "ddd", NewContent: "DDD"},
	}
	result, err := ApplyEdits(content, edits)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "aaa\nBBB\nccc\nDDD" {
		t.Errorf("got %q", result)
	}
}

func TestApplyEdits_NotFound(t *testing.T) {
	content := "hello world"
	edits := []EditOp{{OldContent: "not here", NewContent: "x"}}
	_, err := ApplyEdits(content, edits)
	if err == nil {
		t.Error("expected error for missing old_content")
	}
}

func TestApplyEdits_WhitespaceMismatchFails(t *testing.T) {
	// Edits are exact-match only (matching Claude Code's Edit tool).
	// Whitespace-drifted OldContent must fail — the coder is expected to
	// quote the file contents exactly as they appear on disk.
	content := "hello world\nfoo bar\nbaz"
	edits := []EditOp{{OldContent: "  foo bar  ", NewContent: "FOO"}}
	_, err := ApplyEdits(content, edits)
	if err == nil {
		t.Fatal("expected error for whitespace-drifted old_content, got nil")
	}
}

func TestApplyEdits_EmptyOldContent(t *testing.T) {
	content := "hello"
	edits := []EditOp{{OldContent: "", NewContent: "x"}}
	result, err := ApplyEdits(content, edits)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "hello" {
		t.Errorf("empty old_content should be skipped, got %q", result)
	}
}

func TestApplyEdits_FallbackFullRewrite(t *testing.T) {
	// ApplyFileEdits falls back to full rewrite when single edit doesn't match
	content := "original content"
	edits := []EditOp{{OldContent: "not found", NewContent: "completely new"}}
	// Direct ApplyEdits should error
	_, err := ApplyEdits(content, edits)
	if err == nil {
		t.Error("expected error from ApplyEdits")
	}
	// But ApplyFileEdits with single edit should fall back to full write
	// (tested implicitly through compute pipeline)
}

func TestApplyEdits_MultilineMatch(t *testing.T) {
	content := "function foo() {\n  return 1;\n}\n\nfunction bar() {\n  return 2;\n}"
	edits := []EditOp{{
		OldContent: "function foo() {\n  return 1;\n}",
		NewContent: "function foo() {\n  return 42;\n}",
	}}
	result, err := ApplyEdits(content, edits)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "return 42") {
		t.Errorf("multiline match failed, got %q", result)
	}
	if !strings.Contains(result, "return 2") {
		t.Errorf("bar should be preserved, got %q", result)
	}
}
