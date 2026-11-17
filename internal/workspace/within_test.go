package workspace

import (
	"path/filepath"
	"testing"
)

// A folder whose name starts with the root's name is not inside the root. A
// bare prefix test let "../w-other/x" through, because "/w" is a prefix of
// "/w-other".
func TestWithin_ASiblingSharingTheRootsNameIsOutside(t *testing.T) {
	root := filepath.Join(t.TempDir(), "w")
	for _, rel := range []string{"../w-other/x", "..", "a/../../b"} {
		if got, err := Within(root, rel); err == nil {
			t.Errorf("Within(%q) = %q, want a refusal", rel, got)
		}
	}
}

func TestWithin_PathsInsideResolveUnderTheRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "w")
	cases := map[string]string{
		"":              root,
		".":             root,
		"project/a.md":  filepath.Join(root, "project", "a.md"),
		"a/../b":        filepath.Join(root, "b"),
		"/project/x.js": filepath.Join(root, "project", "x.js"), // a URL path's leading slash
	}
	for rel, want := range cases {
		got, err := Within(root, rel)
		if err != nil || got != want {
			t.Errorf("Within(%q) = %q, %v; want %q", rel, got, err, want)
		}
	}
}

func TestWithin_NoRootIsRefused(t *testing.T) {
	if _, err := Within("", "a"); err == nil {
		t.Fatal("Within with no root should refuse")
	}
}
