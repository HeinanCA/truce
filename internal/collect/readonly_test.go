package collect

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestReadOnly is the guardrail lock: the collect package is the only one that
// holds Kubernetes clients, so a mutating call could only ever appear here. This
// test fails CI if any non-test source in the package contains a write-capable
// client verb, making the read-only promise impossible to regress silently.
func TestReadOnly(t *testing.T) {
	// Method-call patterns for the mutating verbs on client-go typed, dynamic,
	// and metrics interfaces.
	forbidden := regexp.MustCompile(`\.(Create|Update|UpdateStatus|Patch|Delete|DeleteCollection|Apply|ApplyStatus)\(`)

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		checked++
		for i, line := range strings.Split(string(src), "\n") {
			// Ignore comment lines so prose mentioning a verb is not a false hit.
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			if m := forbidden.FindString(line); m != "" {
				t.Errorf("%s:%d: forbidden mutating verb %q — collect must be read-only:\n\t%s",
					name, i+1, m, trimmed)
			}
		}
	}
	if checked == 0 {
		t.Fatal("scanned no source files — guardrail test is not actually running")
	}
}
