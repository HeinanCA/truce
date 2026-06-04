package collect

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// parsedScaledObject is the subset of a KEDA ScaledObject truce needs.
type parsedScaledObject struct {
	TargetKind string
	TargetName string
	Namespace  string
	Triggers   []string // distinct trigger types, in declared order
}

// parseScaledObject extracts the scale target and trigger types from an
// unstructured KEDA ScaledObject. Returns false when there is no usable target.
func parseScaledObject(u unstructured.Unstructured) (parsedScaledObject, bool) {
	obj := u.Object

	name, _, _ := unstructured.NestedString(obj, "spec", "scaleTargetRef", "name")
	if name == "" {
		return parsedScaledObject{}, false
	}
	kind, _, _ := unstructured.NestedString(obj, "spec", "scaleTargetRef", "kind")
	if kind == "" {
		kind = "Deployment" // KEDA's default scaleTargetRef kind
	}

	out := parsedScaledObject{
		TargetKind: kind,
		TargetName: name,
		Namespace:  u.GetNamespace(),
	}

	triggers, found, _ := unstructured.NestedSlice(obj, "spec", "triggers")
	if found {
		seen := map[string]bool{}
		for _, t := range triggers {
			tm, ok := t.(map[string]interface{})
			if !ok {
				continue
			}
			typ, _, _ := unstructured.NestedString(tm, "type")
			if typ == "" || seen[typ] {
				continue
			}
			seen[typ] = true
			out.Triggers = append(out.Triggers, typ)
		}
	}
	return out, true
}
