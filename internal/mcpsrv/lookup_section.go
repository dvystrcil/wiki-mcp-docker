package mcpsrv

import (
	"fmt"
	"strings"
)

// heading is one markdown heading line in a page body.
type heading struct {
	level int    // number of leading '#'
	text  string // heading text, trimmed
	start int    // byte offset of the heading line in the body
}

// headings lists the ATX headings in body, skipping fenced code blocks.
func headings(body string) []heading {
	var out []heading
	inFence := false
	off := 0
	for _, line := range strings.SplitAfter(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
		} else if !inFence && strings.HasPrefix(line, "#") {
			lvl := len(line) - len(strings.TrimLeft(line, "#"))
			rest := line[lvl:]
			if lvl <= 6 && (rest == "" || rest[0] == ' ' || rest[0] == '\t' || rest[0] == '\n') {
				out = append(out, heading{level: lvl, text: strings.TrimSpace(rest), start: off})
			}
		}
		off += len(line)
	}
	return out
}

func sectionNames(hs []heading) []string {
	names := make([]string, 0, len(hs))
	for _, h := range hs {
		names = append(names, h.text)
	}
	return names
}

// sectionBody returns the section whose heading matches name (case- and
// space-insensitive): from its heading line up to the next heading of the
// same or a higher level. ok is false when no heading matches.
func sectionBody(body string, hs []heading, name string) (text, matched string, ok bool) {
	want := strings.ToLower(strings.TrimSpace(strings.TrimLeft(name, "# ")))
	for i, h := range hs {
		if strings.ToLower(h.text) != want {
			continue
		}
		end := len(body)
		for _, n := range hs[i+1:] {
			if n.level <= h.level {
				end = n.start
				break
			}
		}
		return body[h.start:end], h.text, true
	}
	return "", "", false
}

// window cuts text to at most maxChars bytes from offset, backing up to the
// last newline when one falls in the second half of the window so a page
// splits between lines. It returns the slice and the next offset, or -1 when
// nothing is left. maxChars <= 0 means no cap.
func window(text string, offset, maxChars int) (string, int) {
	if offset >= len(text) {
		return "", -1
	}
	if maxChars <= 0 || offset+maxChars >= len(text) {
		return text[offset:], -1
	}
	end := offset + maxChars
	if nl := strings.LastIndex(text[offset:end], "\n"); nl >= maxChars/2 {
		end = offset + nl + 1
	}
	for end > offset && !utf8Start(text[end]) {
		end--
	}
	return text[offset:end], end
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

func unknownSection(name string, hs []heading) string {
	return fmt.Sprintf("no section %q on this page. Sections: %s", name, strings.Join(sectionNames(hs), " | "))
}
