// Package wiki tests — TDD discipline per dvystrcil/homelab#211 Phase 3 ACs.
//
// The store reads pages from a filesystem layout matching the upstream
// `llm-wiki` shape (multi-domain: wiki/<domain>/<type>/*.md, where type ∈
// {entities, concepts, sources, syntheses}). Tests use t.TempDir() to
// build fixture trees per-test so we're not coupled to any specific
// real wiki state.
package wiki

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---- helpers ----------------------------------------------------------------

func writePage(t *testing.T, root, domain, typeDir, slug, body string) {
	t.Helper()
	dir := filepath.Join(root, domain, typeDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, slug+".md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fixtureTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writePage(t, root, "one-ring", "entities", "del", `---
type: entity
domain: one-ring
aliases: ["Del the Messenger"]
tags: [hero, player-character]
created: 2026-05-23
updated: 2026-05-23
source_count: 0
---

# Del

## Identity

A young Bree-lander messenger.

## Related

- [[strider]]
- [[../syntheses/campaign-chronicle]]

## Open Questions

- What does the whistle do?
`)
	writePage(t, root, "one-ring", "entities", "strider", `---
type: entity
domain: one-ring
aliases: ["Aragorn"]
tags: [npc]
created: 2026-05-23
updated: 2026-05-23
source_count: 0
---

# Strider

## Identity

A Ranger of the North.

## Related

- [[del]]

## Open Questions

- What's in the message?
`)
	writePage(t, root, "one-ring", "syntheses", "campaign-chronicle", `---
type: synthesis
domain: one-ring
tags: [campaign-log]
created: 2026-05-23
updated: 2026-05-23
---

# Campaign Chronicle

## Status

Paused at South Downs.

## Open threads

- The whistle.
`)
	writePage(t, root, "homelab", "entities", "owui", `---
type: entity
domain: homelab
aliases: ["Open WebUI"]
tags: [service]
created: 2026-05-23
updated: 2026-05-23
source_count: 1
---

# OWUI

## Identity

Chat UI platform.

## Related

- [[mcp-client]]

## Open Questions

- backup cadence?
`)
	return root
}

// ---- store basics -----------------------------------------------------------

func TestNewStore_HappyPath(t *testing.T) {
	root := fixtureTree(t)
	s, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if s.Root() != root {
		t.Errorf("Root() = %q; want %q", s.Root(), root)
	}
}

func TestNewStore_RejectsMissingDir(t *testing.T) {
	_, err := NewStore(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Error("expected error for missing root")
	}
}

// ---- listing ----------------------------------------------------------------

func TestListDomains(t *testing.T) {
	root := fixtureTree(t)
	s, _ := NewStore(root)
	got, err := s.ListDomains()
	if err != nil {
		t.Fatalf("ListDomains: %v", err)
	}
	want := map[string]bool{"one-ring": true, "homelab": true}
	if len(got) != len(want) {
		t.Errorf("got %d domains, want %d: %v", len(got), len(want), got)
	}
	for _, d := range got {
		if !want[d] {
			t.Errorf("unexpected domain %q in list", d)
		}
	}
}

func TestListPages_OneDomain(t *testing.T) {
	root := fixtureTree(t)
	s, _ := NewStore(root)
	pages, err := s.ListPages("one-ring")
	if err != nil {
		t.Fatalf("ListPages: %v", err)
	}
	if len(pages) != 3 {
		t.Errorf("one-ring has %d pages; want 3 (del, strider, campaign-chronicle): %+v", len(pages), pages)
	}
}

func TestListPages_UnknownDomain(t *testing.T) {
	root := fixtureTree(t)
	s, _ := NewStore(root)
	pages, err := s.ListPages("nonexistent")
	if err != nil {
		t.Fatalf("ListPages(nonexistent) should not error, got %v", err)
	}
	if len(pages) != 0 {
		t.Errorf("ListPages(nonexistent) = %d pages; want 0", len(pages))
	}
}

// ---- lookup -----------------------------------------------------------------

func TestLookup_BySlug(t *testing.T) {
	root := fixtureTree(t)
	s, _ := NewStore(root)
	p, err := s.Lookup("one-ring", "del")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if p.Slug != "del" {
		t.Errorf("Slug = %q; want del", p.Slug)
	}
	if p.Domain != "one-ring" {
		t.Errorf("Domain = %q; want one-ring", p.Domain)
	}
	if p.Type != "entity" {
		t.Errorf("Type = %q; want entity", p.Type)
	}
	if !strings.Contains(p.Body, "Bree-lander") {
		t.Errorf("Body missing 'Bree-lander': %q", p.Body[:80])
	}
}

func TestLookup_NotFound(t *testing.T) {
	root := fixtureTree(t)
	s, _ := NewStore(root)
	_, err := s.Lookup("one-ring", "nobody")
	if err == nil {
		t.Error("expected error for missing slug")
	}
}

// ---- search -----------------------------------------------------------------

func TestSearch_MatchesContent(t *testing.T) {
	root := fixtureTree(t)
	s, _ := NewStore(root)
	hits, err := s.Search("one-ring", "Bree-lander")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("Search for 'Bree-lander' should match del")
	}
	if hits[0].Slug != "del" {
		t.Errorf("top hit Slug = %q; want del", hits[0].Slug)
	}
}

func TestSearch_MatchesAliasFrontmatter(t *testing.T) {
	root := fixtureTree(t)
	s, _ := NewStore(root)
	hits, err := s.Search("one-ring", "Aragorn")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("Search for 'Aragorn' should match strider via aliases frontmatter")
	}
}

func TestSearch_CaseInsensitive(t *testing.T) {
	root := fixtureTree(t)
	s, _ := NewStore(root)
	hits, err := s.Search("one-ring", "BREE-LANDER")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 {
		t.Error("Search should be case-insensitive")
	}
}

// ---- neighbors --------------------------------------------------------------

func TestNeighbors_DirectLinks(t *testing.T) {
	root := fixtureTree(t)
	s, _ := NewStore(root)
	neighbors, err := s.Neighbors("one-ring", "del")
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	// del links to [[strider]] and [[../syntheses/campaign-chronicle]]
	wantSlugs := map[string]bool{"strider": true, "campaign-chronicle": true}
	gotSlugs := map[string]bool{}
	for _, n := range neighbors {
		gotSlugs[n.Slug] = true
	}
	for w := range wantSlugs {
		if !gotSlugs[w] {
			t.Errorf("Neighbors missing %q (got %+v)", w, gotSlugs)
		}
	}
}

// ---- write ------------------------------------------------------------------

func TestWrite_CreatesPage(t *testing.T) {
	root := fixtureTree(t)
	s, _ := NewStore(root)
	body := `---
type: entity
domain: one-ring
aliases: []
tags: []
created: 2026-05-23
updated: 2026-05-23
source_count: 0
---

# Gimli

## Identity

Dwarf.

## Related

- [[del]]

## Open Questions

- N/A yet.
`
	if err := s.Write("one-ring", "entities", "gimli", body); err != nil {
		t.Fatalf("Write: %v", err)
	}
	p, err := s.Lookup("one-ring", "gimli")
	if err != nil {
		t.Fatalf("Lookup after Write: %v", err)
	}
	if p.Type != "entity" {
		t.Errorf("Type after Write = %q; want entity", p.Type)
	}
}

func TestWrite_RejectsBadDomain(t *testing.T) {
	root := fixtureTree(t)
	s, _ := NewStore(root)
	// Path traversal attempt
	if err := s.Write("../escape", "entities", "evil", "body"); err == nil {
		t.Error("Write should reject domain containing ..")
	}
}

func TestWrite_RejectsBadSlug(t *testing.T) {
	root := fixtureTree(t)
	s, _ := NewStore(root)
	for _, bad := range []string{"../escape", "with/slash", "with.dots.md"} {
		if err := s.Write("one-ring", "entities", bad, "body"); err == nil {
			t.Errorf("Write should reject slug %q", bad)
		}
	}
}

func TestWrite_RejectsRawSourcesPath(t *testing.T) {
	root := fixtureTree(t)
	s, _ := NewStore(root)
	// Trying to write to raw/sources/ instead of wiki/
	if err := s.Write("raw", "sources", "anything", "body"); err == nil {
		t.Error("Write should reject domain=raw (raw/ is immutable per AGENTS.md)")
	}
}

// ---- audit -----------------------------------------------------------------

// fixtureTree has:
//   one-ring/entities/del.md          → links [[strider]] + [[../syntheses/campaign-chronicle]]
//   one-ring/entities/strider.md      → links [[del]]
//   one-ring/syntheses/campaign-chronicle.md (no wikilinks)
//   homelab/entities/owui.md          → links [[mcp-client]] (dangling)
//
// Expected audit on one-ring: 3 pages, no orphans (every page has an incoming
// link), no dangling, hubs include all 3 with incoming=1.
// Expected audit on homelab: 1 page, 1 orphan (owui), 1 dangling (mcp-client).
func TestAudit_OneRingDomain(t *testing.T) {
	root := fixtureTree(t)
	s, _ := NewStore(root)
	report, err := s.Audit("one-ring")
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if report.Domain != "one-ring" {
		t.Errorf("Domain = %q; want one-ring", report.Domain)
	}
	if report.TotalPages != 3 {
		t.Errorf("TotalPages = %d; want 3", report.TotalPages)
	}
	if len(report.Orphans) != 0 {
		t.Errorf("Orphans = %v; want none", report.Orphans)
	}
	if len(report.Dangling) != 0 {
		t.Errorf("Dangling = %v; want none", report.Dangling)
	}
}

func TestAudit_HomelabDomainHasOrphanAndDangling(t *testing.T) {
	root := fixtureTree(t)
	s, _ := NewStore(root)
	report, err := s.Audit("homelab")
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if report.TotalPages != 1 {
		t.Errorf("TotalPages = %d; want 1", report.TotalPages)
	}
	if len(report.Orphans) != 1 || report.Orphans[0] != "owui" {
		t.Errorf("Orphans = %v; want [owui]", report.Orphans)
	}
	if len(report.Dangling) != 1 || report.Dangling[0].Slug != "mcp-client" {
		t.Errorf("Dangling = %+v; want one entry for mcp-client", report.Dangling)
	}
	if len(report.Dangling) > 0 {
		got := report.Dangling[0]
		if len(got.ReferencedFrom) != 1 || got.ReferencedFrom[0] != "owui" {
			t.Errorf("Dangling[0].ReferencedFrom = %v; want [owui]", got.ReferencedFrom)
		}
	}
}

// DanglingInBody returns the wikilink targets in `body` that don't
// resolve to any existing page in any domain — used by wiki_write to
// surface broken references back to the caller at the moment of the
// write, so the model can offer stubs without a separate audit call.
func TestDanglingInBody_FlagsUnresolved(t *testing.T) {
	root := fixtureTree(t)
	s, _ := NewStore(root)
	// fixture has: one-ring/entities/{del,strider}, one-ring/syntheses/campaign-chronicle
	body := `# Foo

Some prose.

## Related

- [[strider]]   # resolves in one-ring
- [[ghost-shard]]  # nowhere
- [[../syntheses/campaign-chronicle]]  # cross-section, resolves
- [[bogus-target]] # nowhere
`
	dangling, err := s.DanglingInBody("one-ring", body)
	if err != nil {
		t.Fatalf("DanglingInBody: %v", err)
	}
	want := map[string]bool{"ghost-shard": true, "bogus-target": true}
	if len(dangling) != 2 {
		t.Fatalf("dangling = %v; want 2 entries", dangling)
	}
	for _, slug := range dangling {
		if !want[slug] {
			t.Errorf("unexpected dangling slug %q (want only ghost-shard, bogus-target)", slug)
		}
	}
}

func TestDanglingInBody_ResolvedReturnsEmpty(t *testing.T) {
	root := fixtureTree(t)
	s, _ := NewStore(root)
	body := "## Related\n\n- [[strider]]\n"
	dangling, err := s.DanglingInBody("one-ring", body)
	if err != nil {
		t.Fatalf("DanglingInBody: %v", err)
	}
	if len(dangling) != 0 {
		t.Errorf("dangling = %v; want empty", dangling)
	}
}

// A cross-domain link should NOT be flagged as dangling: del.md links
// [[../syntheses/campaign-chronicle]] which resolves within one-ring.
// This test pins that the audit's dangling check respects cross-domain
// resolution (same as Neighbors does).
func TestAudit_DoesNotFlagCrossDomainResolved(t *testing.T) {
	root := fixtureTree(t)
	s, _ := NewStore(root)
	report, _ := s.Audit("one-ring")
	for _, d := range report.Dangling {
		if d.Slug == "campaign-chronicle" {
			t.Errorf("campaign-chronicle should resolve cross-section in one-ring, but flagged as dangling")
		}
	}
}
