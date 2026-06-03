package collect

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/heinanca/truce/internal/model"
)

// parsedVPA is the subset of a VerticalPodAutoscaler truce needs, extracted from
// the unstructured object returned by the dynamic client.
type parsedVPA struct {
	TargetKind string
	TargetName string
	Namespace  string
	Created    metav1.Time
	// Recs maps container name to its recommendation.
	Recs map[string]model.VPARec
}

// parseVPA extracts targetRef and the per-container recommendations from an
// unstructured VPA. It returns false when the object lacks a usable targetRef.
func parseVPA(u unstructured.Unstructured) (parsedVPA, bool) {
	obj := u.Object

	targetRef, found, err := unstructured.NestedMap(obj, "spec", "targetRef")
	if err != nil || !found {
		return parsedVPA{}, false
	}
	kind, _, _ := unstructured.NestedString(targetRef, "kind")
	name, _, _ := unstructured.NestedString(targetRef, "name")
	if kind == "" || name == "" {
		return parsedVPA{}, false
	}

	out := parsedVPA{
		TargetKind: kind,
		TargetName: name,
		Namespace:  u.GetNamespace(),
		Created:    u.GetCreationTimestamp(),
		Recs:       map[string]model.VPARec{},
	}

	recs, found, err := unstructured.NestedSlice(obj, "status", "recommendation", "containerRecommendations")
	if err != nil || !found {
		return out, true // valid VPA, but no recommendation yet
	}

	for _, item := range recs {
		cr, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		cname, _, _ := unstructured.NestedString(cr, "containerName")
		if cname == "" {
			continue
		}
		out.Recs[cname] = model.VPARec{
			Target:         resourcesFromUnstructured(cr, "target"),
			LowerBound:     resourcesFromUnstructured(cr, "lowerBound"),
			UpperBound:     resourcesFromUnstructured(cr, "upperBound"),
			UncappedTarget: resourcesFromUnstructured(cr, "uncappedTarget"),
		}
	}
	return out, true
}

// resourcesFromUnstructured reads cpu/memory quantity strings from a named
// nested map (e.g. "target") on a container recommendation and returns them as
// model.Resources. Unparseable or absent values stay unset.
func resourcesFromUnstructured(cr map[string]interface{}, field string) model.Resources {
	m, found, err := unstructured.NestedMap(cr, field)
	if err != nil || !found {
		return model.Resources{}
	}
	var out model.Resources
	for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		s, ok, _ := unstructured.NestedString(m, string(name))
		if !ok || s == "" {
			continue
		}
		q, err := resource.ParseQuantity(s)
		if err != nil {
			continue
		}
		quantityToResources(name, q, &out)
	}
	return out
}
