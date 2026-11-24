package mcpsrv

import "testing"

// A client that asks before any tool which doesn't say it is read-only
// (TinyWebUI's toolApproval 'writes') asked the author to approve every
// wiki_search: no wiki-mcp tool carried annotations, while story-mcp marks
// all of its tools readOnlyHint. The five read tools say so; the two that
// change something (wiki_write, wiki_import) must not.
func TestToolAnnotations_ReadToolsAreReadOnly(t *testing.T) {
	sess := lookupSession(t)
	res, err := sess.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	want := map[string]bool{
		"wiki_lookup": true, "wiki_search": true, "wiki_neighbors": true,
		"wiki_list_domains": true, "wiki_audit": true,
		"wiki_write": false, "wiki_import": false,
	}
	seen := map[string]bool{}
	for _, tool := range res.Tools {
		readOnly, known := want[tool.Name]
		if !known {
			t.Errorf("%s: new tool, decide whether it is read-only and add it here", tool.Name)
			continue
		}
		seen[tool.Name] = true
		got := tool.Annotations != nil && tool.Annotations.ReadOnlyHint
		if got != readOnly {
			t.Errorf("%s: readOnlyHint = %v, want %v", tool.Name, got, readOnly)
		}
		if !readOnly && tool.Annotations != nil && tool.Annotations.DestructiveHint != nil && !*tool.Annotations.DestructiveHint {
			t.Errorf("%s: a writing tool must not claim destructiveHint false", tool.Name)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("%s: not registered", name)
		}
	}
}
