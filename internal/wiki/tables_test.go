package wiki

import "testing"

// In a markdown table row the alias pipe of [[slug|Display]] is a column
// separator: Quartz (wiki.sirddail.net) renders "[[slug" and "Display]]" in
// two cells. [[slug\|Display]] renders as one aliased link. The
// fiction-writer wrote such rows (loraine-millson, minna), so wiki_write
// escapes them on save (llm-wiki#67).
func TestEscapeTableWikilinks(t *testing.T) {
	cases := []struct {
		name, in, want string
		n              int
	}{
		{"table row", "| [[jen|Jen Chan]] | Sister |\n", "| [[jen\\|Jen Chan]] | Sister |\n", 1},
		{"two links in a row", "| [[a|A]] | [[b|B]] |", "| [[a\\|A]] | [[b\\|B]] |", 2},
		{"indented row", "  | [[a|A]] |", "  | [[a\\|A]] |", 1},
		{"already escaped", "| [[a\\|A]] |", "| [[a\\|A]] |", 0},
		{"not a table line", "- [[a|A]] in a list", "- [[a|A]] in a list", 0},
		{"bare link in table", "| [[a]] |", "| [[a]] |", 0},
		{"inside a code fence", "```\n| [[a|A]] |\n```\n", "```\n| [[a|A]] |\n```\n", 0},
	}
	for _, c := range cases {
		got, n := EscapeTableWikilinks(c.in)
		if got != c.want || n != c.n {
			t.Errorf("%s: got (%q, %d), want (%q, %d)", c.name, got, n, c.want, c.n)
		}
	}
}
