package wiki

import (
	"regexp"
	"strings"
)

// unescapedAliasLink matches [[target|display]] whose pipe is not already
// escaped as \|.
var unescapedAliasLink = regexp.MustCompile(`(\[\[[^\]|\\]+)\|([^\]]*\]\])`)

// EscapeTableWikilinks rewrites [[slug|Display]] to [[slug\|Display]] on
// markdown table rows (lines starting with "|", outside ``` fences) and
// returns the new body and how many links it changed. In a table row the
// unescaped pipe is a column separator, so the link renders as two broken
// cells on Quartz; the escaped form renders as one aliased link.
func EscapeTableWikilinks(body string) (string, int) {
	lines := strings.Split(body, "\n")
	inFence := false
	total := 0
	for i, l := range lines {
		t := strings.TrimLeft(l, " \t")
		if strings.HasPrefix(t, "```") {
			inFence = !inFence
			continue
		}
		if inFence || !strings.HasPrefix(t, "|") {
			continue
		}
		n := len(unescapedAliasLink.FindAllStringIndex(l, -1))
		if n > 0 {
			lines[i] = unescapedAliasLink.ReplaceAllString(l, `$1\|$2`)
			total += n
		}
	}
	return strings.Join(lines, "\n"), total
}
