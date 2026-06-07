package valuesfile

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "values.fda")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFindRequests_Nested(t *testing.T) {
	p := writeTemp(t, `
ml-management:
  replicaCount: 1
  resources:
    requests:
      cpu: "1"
      memory: "2Gi"
    limits:
      cpu: "2"
`)
	tree, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	blocks := FindRequests(tree)
	if len(blocks) != 1 {
		t.Fatalf("want 1 block, got %d: %+v", len(blocks), blocks)
	}
	b := blocks[0]
	if b.Path != "ml-management" || b.CPU != "1" || b.Mem != "2Gi" {
		t.Errorf("got %+v, want {ml-management 1 2Gi}", b)
	}
}

func TestFindRequests_NumericCPU(t *testing.T) {
	// Helm `cpu: 1` (unquoted) parses as a number; must render as "1".
	p := writeTemp(t, "resources:\n  requests:\n    cpu: 2\n    memory: 512Mi\n")
	tree, _ := Load(p)
	blocks := FindRequests(tree)
	if len(blocks) != 1 || blocks[0].CPU != "2" || blocks[0].Mem != "512Mi" {
		t.Errorf("got %+v, want cpu=2 memory=512Mi at root", blocks)
	}
}

func TestFindRequests_None(t *testing.T) {
	p := writeTemp(t, "image: foo\nreplicaCount: 3\n")
	tree, _ := Load(p)
	if blocks := FindRequests(tree); len(blocks) != 0 {
		t.Errorf("want no blocks, got %+v", blocks)
	}
}
