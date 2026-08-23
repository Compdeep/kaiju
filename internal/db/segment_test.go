package db

import (
	"context"
	"strings"
	"testing"
)

// The pairs are what the index is written with, and English passes through
// whole so it keeps its stemming.
func TestSegmentForIndex(t *testing.T) {
	cases := []struct{ in, want string }{
		{"会議", "会議"},
		{"タイムアウト", "タイ イム ムア アウ ウト"},
		{"我们决定", "我们 们决 决定"},
		{"the timeout", "the timeout"},
		{"犬", "犬"},
		// A space on either side, or unicode61 reads the CJK and the letters
		// beside it as one token nothing can match.
		{"abc犬猫def", "abc 犬猫 def"},
		{"設定 is wrong", "設定 is wrong"},
		{"", ""},
	}
	for _, c := range cases {
		if got := segmentForIndex(c.in); got != c.want {
			t.Errorf("segmentForIndex(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A query asks for the pairs in order, so the characters have to be adjacent.
func TestSegmentForQuery(t *testing.T) {
	cases := []struct{ in, want string }{
		{"タイムアウト", `"タイ イム ムア アウ ウト"`},
		{"会議", `"会議"`},
		{"犬", "犬*"},
		{"timeout", "timeout"},
		{"apache 設定", `apache "設定"`},
		{"timeout AND 会議", `timeout AND "会議"`},
	}
	for _, c := range cases {
		if got := segmentForQuery(c.in); got != c.want {
			t.Errorf("segmentForQuery(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Words are found in Japanese, Chinese and Korean, which is what the whole
// segmentation is for: before it, every one of these returned nothing.
func TestSearchMessagesFindsCJK(t *testing.T) {
	d := newSearchDB(t, "s1",
		"会議で決めたタイムアウトの値は三十秒でした",
		"我们决定把超时设置为三十秒",
		"우리는 삼십초로 정했습니다",
		"the timeout we agreed was thirty seconds",
		"apache の設定ファイルを編集しました",
	)
	cases := []struct {
		query string
		want  int
	}{
		{"タイムアウト", 1},
		{"会議", 1},
		{"決めた", 1},
		{"三十秒", 2}, // in the Japanese and in the Chinese
		{"超时", 1},
		{"我们决定", 1},
		{"삼십초", 1},
		{"設定", 1},
		{"timeout", 1},
		{"apache", 1},
		{"ワニ", 0}, // never mentioned
	}
	for _, c := range cases {
		found, err := d.SearchMessages(context.Background(), c.query, "", 10)
		if err != nil {
			t.Errorf("query %q: %v", c.query, err)
			continue
		}
		if len(found) != c.want {
			t.Errorf("query %q found %d, want %d", c.query, len(found), c.want)
		}
	}
	// 三十秒 is in both the Japanese and the Chinese message.
	if found, _ := d.SearchMessages(context.Background(), "三十", "", 10); len(found) != 2 {
		t.Errorf("三十 found %d, want 2 (it is in the Japanese and the Chinese)", len(found))
	}
}

// The pairs have to be adjacent and in order, or a query for one word answers
// with messages that only ever used those characters apart.
func TestSearchMessagesCJKRequiresAdjacency(t *testing.T) {
	d := newSearchDB(t, "s1",
		"タイヤの交換とムアツ布団の話", // holds タイ and ムア, never タイムアウト
		"タイムアウトの設定",
	)
	found, err := d.SearchMessages(context.Background(), "タイムアウト", "", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("want only the message that used the word, got %d: %+v", len(found), found)
	}
	if !strings.Contains(found[0].Content, "タイムアウトの設定") {
		t.Errorf("matched the wrong message: %q", found[0].Content)
	}
}

// Editing and deleting a message keep the segmented index in step.
func TestSearchMessagesCJKTracksChanges(t *testing.T) {
	d := newSearchDB(t, "s1", "会議で決めたタイムアウト")
	live, _ := d.GetMessages("s1", 10)
	id := live[0].ID

	d.conn.Exec(`UPDATE messages SET content = ? WHERE id = ?`, "打ち合わせで決めた再試行", id)
	if found, _ := d.SearchMessages(context.Background(), "タイムアウト", "", 10); len(found) != 0 {
		t.Error("the old Japanese is still indexed after an edit")
	}
	if found, _ := d.SearchMessages(context.Background(), "再試行", "", 10); len(found) != 1 {
		t.Error("the new Japanese was not indexed after an edit")
	}
	d.conn.Exec(`DELETE FROM messages WHERE id = ?`, id)
	if found, _ := d.SearchMessages(context.Background(), "再試行", "", 10); len(found) != 0 {
		t.Error("a deleted message is still indexed")
	}
}

// An index built before the segmentation is replaced, not left in place: it
// holds whole sentences as single tokens and would answer nothing for CJK.
func TestMigrationReplacesTheUnsegmentedIndex(t *testing.T) {
	d := newSearchDB(t, "s1", "会議で決めたタイムアウト")

	// Put the old shape back.
	for _, s := range []string{
		`DROP TRIGGER messages_fts_insert`, `DROP TRIGGER messages_fts_delete`,
		`DROP TRIGGER messages_fts_update`, `DROP TABLE messages_fts`,
		`CREATE VIRTUAL TABLE messages_fts USING fts5(content, content='messages', content_rowid='id', tokenize='porter unicode61')`,
		`INSERT INTO messages_fts(messages_fts) VALUES ('rebuild')`,
	} {
		if _, err := d.conn.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if found, _ := d.SearchMessages(context.Background(), "タイムアウト", "", 10); len(found) != 0 {
		t.Fatal("the old index found Japanese; the fixture is wrong")
	}

	if err := d.migrateMessageSearch(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	found, err := d.SearchMessages(context.Background(), "タイムアウト", "", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("want the message found after the index is replaced, got %d", len(found))
	}
}
