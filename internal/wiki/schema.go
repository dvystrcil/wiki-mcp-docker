package wiki

// Write-time schema check for wiki_write (wiki-mcp-docker#1).
//
// The schema is NOT defined here. llm-wiki's scripts/_schema.py is the source
// of truth; `_schema.py --write-json` emits scripts/schema.json, and
// llm-wiki's test_schema_json.py fails if that file drifts. This pod's init
// container clones the whole llm-wiki repo into /data and serves /data/wiki,
// so /data/scripts/schema.json is the same commit, synced by the same
// git-sync.
//
// Validate mirrors lint_schema.check_file rule for rule (same names, same
// order, same early returns) so a page wiki_write reports clean is a page the
// daily lint reports clean. It is LENIENT by design: the write still lands and
// the violations go back in the tool response, like the `dangling` list, so
// the model fixes them on its next turn instead of a human weeks later. 19 of
// the 23 HIGH violations on llm-wiki main (2026-10-09) came from wiki_write
// while the daily lint reported them and nothing acted.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Schema is scripts/schema.json from llm-wiki.
type Schema struct {
	TypeForDir          map[string]string              `json:"type_for_dir"`
	KnownDomains        []string                       `json:"known_domains"`
	RequiredFrontmatter map[string][]string            `json:"required_frontmatter"`
	DefaultSections     map[string][]string            `json:"default_sections"`
	DomainSections      map[string]map[string][]string `json:"domain_sections"`
	SourcePathPrefixes  []string                       `json:"source_path_prefixes"`
}

// Violation is one lint_schema finding.
type Violation struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Detail   string `json:"detail"`
}

// LoadSchema reads schema.json. A missing or unreadable file is an error;
// the caller decides whether that disables the check.
func LoadSchema(path string) (*Schema, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(s.RequiredFrontmatter) == 0 || len(s.TypeForDir) == 0 {
		return nil, fmt.Errorf("%s has no required_frontmatter/type_for_dir: not a schema.json", path)
	}
	return &s, nil
}

// unquote matches _schema.parse_frontmatter, which strips surrounding quotes;
// Go's parseFrontmatter keeps them.
func unquote(v string) string { return strings.Trim(strings.TrimSpace(v), `"'`) }

func (s *Schema) sectionsFor(domain, pageType string) []string {
	if m, ok := s.DomainSections[domain]; ok {
		if v, ok := m[pageType]; ok {
			return v
		}
	}
	return s.DefaultSections[pageType]
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// Validate checks a page body about to be written to wiki/<domain>/<typeDir>/.
func (s *Schema) Validate(domain, typeDir, body string) []Violation {
	out := []Violation{}
	add := func(rule, sev, detail string) { out = append(out, Violation{rule, sev, detail}) }

	fm, rest := parseFrontmatter(body)
	pageType := unquote(fm["type"])
	expected := s.TypeForDir[typeDir]

	if pageType == "" {
		add("missing_frontmatter", "high", "Missing frontmatter or `type` key.")
		return out
	}
	if expected != "" && pageType != expected {
		add("type_path_mismatch", "high", fmt.Sprintf(
			"frontmatter type `%s` doesn't match directory `%s/` (expected `%s`).", pageType, typeDir, expected))
	}
	required, ok := s.RequiredFrontmatter[pageType]
	if !ok {
		add("invalid_type", "high", fmt.Sprintf("Unsupported type `%s`.", pageType))
		return out
	}
	for _, key := range required {
		if _, present := fm[key]; !present {
			add("missing_frontmatter_key", "high", fmt.Sprintf(
				"Missing required frontmatter key `%s` for type `%s`.", key, pageType))
		}
	}
	fmDomain := unquote(fm["domain"])
	if domain != "" && fmDomain != domain {
		add("domain_mismatch", "high", fmt.Sprintf(
			"frontmatter domain `%s` doesn't match directory domain `%s`.", fmDomain, domain))
	}
	if fmDomain != "" && !contains(s.KnownDomains, fmDomain) {
		add("unknown_domain", "medium", fmt.Sprintf(
			"frontmatter domain `%s` is not a known domain (%v).", fmDomain, s.KnownDomains))
	}
	sectionDomain := domain
	if sectionDomain == "" {
		sectionDomain = fmDomain
	}
	for _, sec := range s.sectionsFor(sectionDomain, pageType) {
		if !strings.Contains(rest, sec) {
			label := sectionDomain
			if label == "" {
				label = "default"
			}
			add("missing_section", "high", fmt.Sprintf(
				"Missing required section heading `%s` for (%s, %s).", sec, label, pageType))
		}
	}
	if pageType == "source" {
		sp := unquote(fm["source_path"])
		okPrefix := false
		for _, p := range s.SourcePathPrefixes {
			if strings.HasPrefix(sp, p) {
				okPrefix = true
				break
			}
		}
		if !okPrefix {
			add("invalid_source_path", "high", fmt.Sprintf("`source_path` must be under %v.", s.SourcePathPrefixes))
		}
	}
	return out
}
