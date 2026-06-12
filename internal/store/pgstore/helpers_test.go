package pgstore

import (
	"errors"
	"reflect"
	"testing"

	"github.com/hurtener/stowage/internal/store"
)

// These are pure helpers — no database required. splitStatements in
// particular guards the migration runner's comment-stripping behaviour
// (a ';' inside a SQL line comment must never split a statement).

func TestSplitStatements(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "two statements split on semicolon",
			in:   "CREATE TABLE a (id TEXT);\nCREATE INDEX b ON a(id);",
			want: []string{"CREATE TABLE a (id TEXT)", "\nCREATE INDEX b ON a(id)"},
		},
		{
			name: "semicolon inside line comment does not split",
			in:   "-- note: uses ';' deliberately\nCREATE TABLE a (id TEXT);",
			want: []string{"\nCREATE TABLE a (id TEXT)"},
		},
		{
			name: "trailing comment after statement is stripped",
			in:   "CREATE TABLE a (id TEXT); -- trailing; comment",
			want: []string{"CREATE TABLE a (id TEXT)"},
		},
		{
			name: "whitespace-only fragments dropped",
			in:   ";;\n  ;\n",
			want: nil,
		},
		{
			name: "empty input",
			in:   "",
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitStatements(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("splitStatements(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseCursor(t *testing.T) {
	ts, id, err := parseCursor("1718000000123:01HZX")
	if err != nil || ts != 1718000000123 || id != "01HZX" {
		t.Errorf("parseCursor round-trip: ts=%d id=%q err=%v", ts, id, err)
	}
	for name, in := range map[string]string{
		"missing colon":         "1718000000123",
		"non-numeric timestamp": "abc:01HZX",
		"empty id":              "1718000000123:",
	} {
		if _, _, err := parseCursor(in); !errors.Is(err, store.ErrBadCursor) {
			t.Errorf("%s: parseCursor(%q) err = %v, want ErrBadCursor", name, in, err)
		}
	}
}

func TestSplitCSV(t *testing.T) {
	if got := splitCSV(""); got != nil {
		t.Errorf("splitCSV(\"\") = %#v, want nil", got)
	}
	if got := splitCSV("a,b,c"); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("splitCSV(\"a,b,c\") = %#v", got)
	}
	if got := splitCSV("solo"); !reflect.DeepEqual(got, []string{"solo"}) {
		t.Errorf("splitCSV(\"solo\") = %#v", got)
	}
}
