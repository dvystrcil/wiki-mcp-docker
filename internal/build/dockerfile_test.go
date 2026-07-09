// Package build holds build-shape regression tests.
//
// wiki_import POSTs to the n8n webhook over HTTPS. The prod image is
// FROM scratch, which has no root store, so the CA bundle has to be copied
// in explicitly. It was not, from v0.1.3 (when wiki_import landed) until
// the fix — every import failed with "x509: certificate signed by unknown
// authority", and nothing caught it because nothing called the tool.
package build

import (
	"os"
	"strings"
	"testing"
)

func prodStage(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	var stage []string
	in := false
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && strings.EqualFold(f[0], "FROM") {
			in = strings.HasSuffix(strings.ToLower(line), " as prod")
			continue
		}
		if in {
			stage = append(stage, line)
		}
	}
	if len(stage) == 0 {
		t.Fatal("no `FROM ... AS prod` stage found in Dockerfile")
	}
	return stage
}

// The scratch prod image must carry a CA bundle, or every outbound HTTPS
// call from wiki_import fails at handshake.
func TestProdStageCopiesCACertificates(t *testing.T) {
	for _, line := range prodStage(t) {
		if strings.Contains(line, "ca-certificates.crt") &&
			strings.HasPrefix(strings.ToUpper(strings.TrimSpace(line)), "COPY") {
			return
		}
	}
	t.Error("prod stage does not COPY ca-certificates.crt; " +
		"wiki_import's HTTPS call to the n8n webhook will fail with " +
		"x509: certificate signed by unknown authority")
}
