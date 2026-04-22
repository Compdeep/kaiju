package slug

import "testing"

func Slugify(s string) string {
	out := make([]rune, 0, len(s))
	prev := '-'
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out = append(out, r)
			prev = r
		case r >= 'A' && r <= 'Z':
			out = append(out, r+('a'-'A'))
			prev = r
		default:
			if prev != '-' {
				out = append(out, '-')
				prev = '-'
			}
		}
	}
	if len(out) > 0 && out[len(out)-1] == '-' {
		out = out[:len(out)-1]
	}
	return string(out)
}

func TestSlugify(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Hello World", "hello-world"},
		{"  many   spaces  ", "many-spaces"},
		{"a/b\\c", "a-b-c"},
	}
	for _, c := range cases {
		got := Slugify(c.in)
		if got != c.want {
			t.Errorf("Slugify(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
