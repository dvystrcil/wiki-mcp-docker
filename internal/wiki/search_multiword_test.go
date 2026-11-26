package wiki

import "testing"

// wiki-mcp-docker#43: Search used to need the WHOLE query as one contiguous
// substring, so 'Brandywine river source' found nothing even though the page
// has the alias 'Brandywine'. These tests pin the per-word behaviour.

func brandywineTree(t *testing.T) string {
	t.Helper()
	root := fixtureTree(t)
	writePage(t, root, "one-ring", "entities", "brandywine-river", `---
type: entity
domain: one-ring
aliases: ["Brandywine River", "Brandywine", "Baranduin"]
tags: [river, place]
---

# Brandywine River

The river that bounds the Shire to the east.
`)
	// Mentions the word 'source' and 'river' once each in its body only.
	writePage(t, root, "one-ring", "sources", "session-notes", `---
type: source
domain: one-ring
tags: [session]
---

# Session Notes

We crossed a river. The source of the rumour is unknown.
`)
	return root
}

func slugs(ps []Page) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Slug
	}
	return out
}

func mustSearch(t *testing.T, root, domain, query string) []Page {
	t.Helper()
	s, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	hits, err := s.Search(domain, query)
	if err != nil {
		t.Fatalf("Search(%q): %v", query, err)
	}
	return hits
}

func TestSearch_MultiWordQueryMatchesAliasWord(t *testing.T) {
	for _, q := range []string{"Brandywine river source", "Brandywine source", "source of the Brandywine"} {
		hits := mustSearch(t, brandywineTree(t), "one-ring", q)
		if len(hits) == 0 || hits[0].Slug != "brandywine-river" {
			t.Errorf("Search(%q) = %v, want brandywine-river first", q, slugs(hits))
		}
	}
}

// Words are matched independently; a hyphenated query word splits too, so
// 'name-lookup' finds a page that says 'name lookup'.
func TestSearch_SplitsOnPunctuation(t *testing.T) {
	root := t.TempDir()
	writePage(t, root, "homelab", "entities", "coredns", `---
type: entity
domain: homelab
aliases: ["CoreDNS"]
tags: [dns, service]
---

# CoreDNS

The cluster name lookup server. Managed by kubeadm, not GitOps.
`)
	hits := mustSearch(t, root, "homelab", "name-lookup server DNS managed GitOps namespace")
	if len(hits) == 0 || hits[0].Slug != "coredns" {
		t.Errorf("got %v, want coredns", slugs(hits))
	}
}

// Stop words and one-letter words must not match every page: bodies are
// full of 'the' and 'of'.
func TestSearch_StopWordsDoNotMatch(t *testing.T) {
	hits := mustSearch(t, brandywineTree(t), "one-ring", "the nonexistent of a")
	if len(hits) != 0 {
		t.Errorf("got %v, want no hits", slugs(hits))
	}
}

// A query made only of stop words still searches (otherwise 'The One'
// style lookups of a short title would be impossible to express).
func TestSearch_AllStopWordsFallsBackToThem(t *testing.T) {
	root := t.TempDir()
	writePage(t, root, "one-ring", "concepts", "the-one", `---
type: concept
domain: one-ring
---

# The One
`)
	hits := mustSearch(t, root, "one-ring", "the")
	if len(hits) != 1 {
		t.Errorf("got %v, want the-one", slugs(hits))
	}
}

func TestSearch_NoQueryWordAnywhereReturnsNothing(t *testing.T) {
	hits := mustSearch(t, brandywineTree(t), "one-ring", "nonexistent zzyzx")
	if len(hits) != 0 {
		t.Errorf("got %v, want no hits", slugs(hits))
	}
}

// A page matching more of the query words, in stronger fields, ranks first;
// a body-only mention of every word ranks below an alias+slug match.
func TestSearch_RanksAliasMatchAboveBodyMentions(t *testing.T) {
	hits := mustSearch(t, brandywineTree(t), "one-ring", "Brandywine river source")
	got := slugs(hits)
	if len(got) != 2 || got[0] != "brandywine-river" || got[1] != "session-notes" {
		t.Errorf("got %v, want [brandywine-river session-notes]", got)
	}
}

// The doc comment always said the title (first h1) is searched; a title
// match must outrank a body-only mention.
func TestSearch_TitleOutranksBody(t *testing.T) {
	root := t.TempDir()
	writePage(t, root, "fiction", "concepts", "aaa-mentions", "# Notes\n\nSee the Lantern Guild.\n")
	writePage(t, root, "fiction", "concepts", "zzz-hall", "# Lantern Guild\n\nA hall.\n")
	hits := mustSearch(t, root, "fiction", "lantern guild")
	if len(hits) != 2 || hits[0].Slug != "zzz-hall" {
		t.Errorf("got %v, want zzz-hall first", slugs(hits))
	}
}

// Existing single-word behaviour is unchanged: case-insensitive substring.
func TestSearch_SingleWordSubstringStillMatches(t *testing.T) {
	hits := mustSearch(t, brandywineTree(t), "one-ring", "BARAND")
	if len(hits) != 1 || hits[0].Slug != "brandywine-river" {
		t.Errorf("got %v, want brandywine-river", slugs(hits))
	}
}

// Covering more of the query beats one strong hit: a page aliased
// 'Guild of the Lantern' outranks a page that is only titled and aliased
// 'Lantern', for the query 'lantern guild'.
func TestSearch_MoreWordsMatchedOutranksOneStrongField(t *testing.T) {
	root := t.TempDir()
	writePage(t, root, "fiction", "concepts", "aaa-lantern", "---\naliases: [\"Lantern\"]\n---\n\n# Lantern\n")
	writePage(t, root, "fiction", "concepts", "zzz-notes", "---\naliases: [\"Guild of the Lantern\"]\n---\n\nNotes.\n")
	hits := mustSearch(t, root, "fiction", "lantern guild")
	if len(hits) != 2 || hits[0].Slug != "zzz-notes" {
		t.Errorf("got %v, want zzz-notes first", slugs(hits))
	}
}

// The query appearing verbatim breaks a tie between pages matching the same
// words in the same fields.
func TestSearch_ExactPhraseBreaksTie(t *testing.T) {
	root := t.TempDir()
	writePage(t, root, "fiction", "concepts", "aaa-scattered", "---\naliases: [\"Road by the River\"]\n---\n\nNotes.\n")
	writePage(t, root, "fiction", "concepts", "zzz-exact", "---\naliases: [\"River Road\"]\n---\n\nNotes.\n")
	hits := mustSearch(t, root, "fiction", "river road")
	if len(hits) != 2 || hits[0].Slug != "zzz-exact" {
		t.Errorf("got %v, want zzz-exact first", slugs(hits))
	}
}
