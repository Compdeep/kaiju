package api

import "testing"

func TestStripAttachmentBlock(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{
			name: "single file",
			in:   "[attached files]\n- uploads/s/deck.pptx (application/…, 1234 bytes)\n\n[query]\nsummarize this deck",
			want: "summarize this deck",
		},
		{
			name: "multiple files + multiline text",
			in:   "[attached files]\n- a.pdf (…)\n- b.docx (…)\n\n[query]\nline one\nline two",
			want: "line one\nline two",
		},
		{
			name: "no block — plain message untouched",
			in:   "just a normal question about [query] syntax",
			want: "just a normal question about [query] syntax",
		},
		{
			name: "empty",
			in:   "",
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripAttachmentBlock(c.in); got != c.want {
				t.Fatalf("stripAttachmentBlock:\n got  %q\n want %q", got, c.want)
			}
		})
	}
}
