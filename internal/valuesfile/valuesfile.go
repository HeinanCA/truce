// Package valuesfile reads a Helm-style values file and locates the
// resources.requests blocks inside it, so truce can show a service's current
// committed requests next to its recommendation as a PR-ready diff. It only
// reads — it never writes the file.
package valuesfile

import (
	"fmt"
	"os"
	"strings"

	"sigs.k8s.io/yaml"
)

// Block is a resources.requests location found in the file.
type Block struct {
	// Path is the dotted key path to the resources block (e.g.
	// "ml-management.deployment"), for display and disambiguation.
	Path string
	// CPU / Mem are the request strings as written in the file ("1", "500m",
	// "2Gi"); empty when not set.
	CPU string
	Mem string
}

// Load parses a YAML values file into a generic tree.
func Load(path string) (map[string]interface{}, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading values file: %w", err)
	}
	var tree map[string]interface{}
	if err := yaml.Unmarshal(raw, &tree); err != nil {
		return nil, fmt.Errorf("parsing values file %s: %w", path, err)
	}
	return tree, nil
}

// FindRequests walks the tree and returns every resources.requests block it
// finds, with the key path that leads to it. A per-service values file usually
// yields exactly one; a shared file yields one per service.
func FindRequests(tree map[string]interface{}) []Block {
	var out []Block
	walk(tree, nil, &out)
	return out
}

func walk(node interface{}, path []string, out *[]Block) {
	m, ok := node.(map[string]interface{})
	if !ok {
		return
	}
	// Is this node a "resources" map with a "requests" child?
	if res, ok := m["resources"].(map[string]interface{}); ok {
		if req, ok := res["requests"].(map[string]interface{}); ok {
			*out = append(*out, Block{
				Path: strings.Join(path, "."),
				CPU:  scalarString(req["cpu"]),
				Mem:  scalarString(req["memory"]),
			})
		}
	}
	// Recurse into children (sorted-agnostic; order not needed for matching).
	for k, v := range m {
		walk(v, append(path, k), out)
	}
}

func scalarString(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		// Helm numbers like cpu: 1 parse as float64.
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%g", t)
	case int:
		return fmt.Sprintf("%d", t)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}
