package wiki

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// A schema.json in the shape llm-wiki's `_schema.py --write-json` emits
// (wiki-mcp-docker#1). Trimmed to what the cases below need.
const testSchemaJSON = `{
  "_generated_by": "test",
  "type_for_dir": {"entities": "entity", "concepts": "concept", "sources": "source", "syntheses": "synthesis"},
  "known_domains": ["one-ring", "homelab", "fiction"],
  "required_frontmatter": {
    "entity": ["type", "domain", "aliases", "tags", "created", "updated", "source_count"],
    "concept": ["type", "domain", "aliases", "tags", "created", "updated", "source_count"],
    "source": ["type", "domain", "source_path", "title", "author", "date", "tags", "created"],
    "synthesis": ["type", "domain", "tags", "created", "updated"]
  },
  "default_sections": {
    "entity": ["## Identity", "## Aliases", "## Key Attributes", "## Evidence", "## Related", "## Open Questions"],
    "concept": ["## Definition"],
    "source": ["## Summary"],
    "synthesis": ["## Question / Purpose"]
  },
  "domain_sections": {"fiction": {"entity": ["## Identity", "## Related", "## Open Questions"]}},
  "source_path_prefixes": ["raw/sources/", "raw/assets/"]
}`

func loadTestSchema(t *testing.T) *Schema {
	t.Helper()
	p := filepath.Join(t.TempDir(), "schema.json")
	if err := os.WriteFile(p, []byte(testSchemaJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSchema(p)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

const goodFictionEntity = `---
type: entity
domain: fiction
aliases: []
tags: [character]
created: 2026-10-09
updated: 2026-10-09
source_count: 0
author_validated: false
---
## Identity
x
## Related
y
## Open Questions
z
`

func rules(vs []Violation) []string {
	out := []string{}
	for _, v := range vs {
		out = append(out, v.Rule)
	}
	sort.Strings(out)
	return out
}

func TestValidate_ConformingPageHasNoViolations(t *testing.T) {
	s := loadTestSchema(t)
	if vs := s.Validate("fiction", "entities", goodFictionEntity); len(vs) != 0 {
		t.Fatalf("want none, got %+v", vs)
	}
}

func TestValidate_QuotedValuesAreUnquotedLikeThePythonLint(t *testing.T) {
	// _schema.parse_frontmatter strips quotes; Go's parseFrontmatter keeps
	// them. Without normalising, `type: "entity"` reads as invalid_type.
	s := loadTestSchema(t)
	body := `---
type: "entity"
domain: 'fiction'
aliases: []
tags: []
created: 2026-10-09
updated: 2026-10-09
source_count: 0
---
## Identity
## Related
## Open Questions
`
	if vs := s.Validate("fiction", "entities", body); len(vs) != 0 {
		t.Fatalf("want none, got %+v", vs)
	}
}

func TestValidate_TheLiveFailureShapes(t *testing.T) {
	s := loadTestSchema(t)
	cases := []struct {
		name, domain, typeDir, body string
		want                        []string
	}{
		// 19 live HIGH pages: no frontmatter / no `type` at all.
		{"no frontmatter", "fiction", "entities", "# Jen\n\nSome prose.\n", []string{"missing_frontmatter"}},
		// The first fiction write: `title:` instead of `type:`.
		{"title not type", "fiction", "entities", "---\ntitle: Jen\n---\n## Identity\n", []string{"missing_frontmatter"}},
		// 4 live HIGH pages: required section missing.
		{"missing section", "fiction", "entities",
			goodFictionEntity[:len(goodFictionEntity)-len("## Open Questions\nz\n")], []string{"missing_section"}},
		{"type vs dir", "fiction", "concepts", goodFictionEntity, []string{"type_path_mismatch"}},
		{"domain vs dir", "one-ring", "entities", goodFictionEntity,
			[]string{"domain_mismatch", "missing_section", "missing_section", "missing_section"}},
		{"unsupported type", "fiction", "entities", "---\ntype: character\n---\n", []string{"invalid_type", "type_path_mismatch"}},
		{"missing key", "fiction", "entities", "---\ntype: entity\ndomain: fiction\n---\n## Identity\n## Related\n## Open Questions\n",
			[]string{"missing_frontmatter_key", "missing_frontmatter_key", "missing_frontmatter_key", "missing_frontmatter_key", "missing_frontmatter_key"}},
		{"bad source path", "homelab", "sources",
			"---\ntype: source\ndomain: homelab\nsource_path: notes/x.md\ntitle: t\nauthor: a\ndate: d\ntags: []\ncreated: c\n---\n## Summary\n",
			[]string{"invalid_source_path"}},
	}
	for _, c := range cases {
		got := rules(s.Validate(c.domain, c.typeDir, c.body))
		want := append([]string{}, c.want...)
		sort.Strings(want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %v, want %v", c.name, got, want)
		}
	}
}

func TestValidate_DomainWithoutOverrideUsesDefaultSections(t *testing.T) {
	// The issue's open question 3: a first write to a domain with no
	// override is checked against DEFAULT, exactly as lint_schema.py does.
	s := loadTestSchema(t)
	body := "---\ntype: entity\ndomain: homelab\naliases: []\ntags: []\ncreated: c\nupdated: u\nsource_count: 0\n---\n## Identity\n## Related\n## Open Questions\n"
	got := rules(s.Validate("homelab", "entities", body))
	if !reflect.DeepEqual(got, []string{"missing_section", "missing_section", "missing_section"}) {
		t.Fatalf("want 3 missing DEFAULT sections (Aliases, Key Attributes, Evidence), got %v", got)
	}
}

func TestLoadSchema_MissingFileIsAnError(t *testing.T) {
	if _, err := LoadSchema(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("want error")
	}
}
