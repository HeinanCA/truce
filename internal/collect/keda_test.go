package collect

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestParseScaledObject(t *testing.T) {
	so := unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"namespace": "neteera", "name": "alert-so"},
		"spec": map[string]interface{}{
			"scaleTargetRef": map[string]interface{}{"name": "alert"},
			"triggers": []interface{}{
				map[string]interface{}{"type": "kafka"},
				map[string]interface{}{"type": "prometheus"},
				map[string]interface{}{"type": "kafka"}, // dup, dropped
			},
		},
	}}
	got, ok := parseScaledObject(so)
	if !ok {
		t.Fatal("expected ok")
	}
	if got.TargetName != "alert" || got.TargetKind != "Deployment" || got.Namespace != "neteera" {
		t.Errorf("target = %+v, want alert/Deployment/neteera", got)
	}
	if len(got.Triggers) != 2 || got.Triggers[0] != "kafka" || got.Triggers[1] != "prometheus" {
		t.Errorf("triggers = %v, want [kafka prometheus]", got.Triggers)
	}
}

func TestParseScaledObject_NoTarget(t *testing.T) {
	so := unstructured.Unstructured{Object: map[string]interface{}{"spec": map[string]interface{}{}}}
	if _, ok := parseScaledObject(so); ok {
		t.Error("expected not ok with no scaleTargetRef")
	}
}
