package mcpsrv

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// homelab#2297: a fiction consistency check made ~14 wiki_lookup calls on
// pages of 4.6k-26k chars and ran the model's context to 55k-76k tokens. The
// new arguments let a caller take one section or a capped page. They are
// opt-in: a lookup with no extra arguments must return the full body exactly
// as before, because a model that rewrites a page from a partial read would
// delete everything after the cut.

func lookupArgs(t *testing.T, sess *mcp.ClientSession, args map[string]any) (map[string]any, *mcp.CallToolResult) {
	t.Helper()
	b, _ := json.Marshal(args)
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "wiki_lookup", Arguments: json.RawMessage(b)})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	var out map[string]any
	if !res.IsError {
		if err := json.Unmarshal([]byte(resultText(res)), &out); err != nil {
			t.Fatalf("decode %q: %v", resultText(res), err)
		}
	}
	return out, res
}

func TestWikiLookup_DefaultIsFullBodyPlusOutline(t *testing.T) {
	sess := lookupSession(t)
	out, _ := lookupArgs(t, sess, map[string]any{"domain": "fiction", "slug": "alpha"})
	body, _ := out["body"].(string)
	if !strings.Contains(body, "A character.") || !strings.Contains(body, "[[beta]]") {
		t.Fatalf("default lookup must return the whole body, got %q", body)
	}
	if out["truncated"] != false {
		t.Fatalf("default lookup must not be truncated: %v", out["truncated"])
	}
	secs, _ := out["sections"].([]any)
	if len(secs) < 2 || secs[1] != "Identity" {
		t.Fatalf("sections: %v", out["sections"])
	}
	if tc, _ := out["total_chars"].(float64); int(tc) != len(body) {
		t.Fatalf("total_chars %v != len(body) %d", out["total_chars"], len(body))
	}
}

func TestWikiLookup_Section(t *testing.T) {
	sess := lookupSession(t)
	out, _ := lookupArgs(t, sess, map[string]any{"domain": "fiction", "slug": "alpha", "section": "identity"})
	body, _ := out["body"].(string)
	if !strings.Contains(body, "A character.") || strings.Contains(body, "[[beta]]") {
		t.Fatalf("section body: %q", body)
	}
	if out["section"] != "Identity" {
		t.Fatalf("section echo: %v", out["section"])
	}
}

func TestWikiLookup_UnknownSectionListsSections(t *testing.T) {
	sess := lookupSession(t)
	_, res := lookupArgs(t, sess, map[string]any{"domain": "fiction", "slug": "alpha", "section": "Nope"})
	if !res.IsError || !strings.Contains(resultText(res), "Identity") {
		t.Fatalf("want an error naming the sections, got isErr=%v %q", res.IsError, resultText(res))
	}
}

func TestWikiLookup_MaxCharsPages(t *testing.T) {
	sess := lookupSession(t)
	full, _ := lookupArgs(t, sess, map[string]any{"domain": "fiction", "slug": "alpha"})
	whole, _ := full["body"].(string)

	first, _ := lookupArgs(t, sess, map[string]any{"domain": "fiction", "slug": "alpha", "max_chars": 20})
	if first["truncated"] != true {
		t.Fatalf("want truncated, got %v", first)
	}
	got, _ := first["body"].(string)
	if len(got) == 0 || len(got) > 20 {
		t.Fatalf("first page %d chars: %q", len(got), got)
	}
	next := first["next_offset"].(float64)
	for first["truncated"] == true {
		first, _ = lookupArgs(t, sess, map[string]any{"domain": "fiction", "slug": "alpha", "max_chars": 20, "offset": next})
		b, _ := first["body"].(string)
		got += b
		if first["truncated"] == true {
			next = first["next_offset"].(float64)
		}
	}
	if got != whole {
		t.Fatalf("paged body differs from the full body:\n%q\n%q", got, whole)
	}
}
