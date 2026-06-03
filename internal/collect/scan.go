package collect

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

// vpaGVR is the dynamic-client coordinate for the VPA CRD. truce reads VPAs as
// unstructured objects so it does not import the VPA typed module.
var vpaGVR = schema.GroupVersionResource{
	Group:    "autoscaling.k8s.io",
	Version:  "v1",
	Resource: "verticalpodautoscalers",
}

// Scope describes which namespaces to scan.
type Scope struct {
	Namespace     string // honored when AllNamespaces is false; "" means default
	AllNamespaces bool
}

// namespace returns the namespace argument to pass to list calls.
func (s Scope) namespace() string {
	if s.AllNamespaces {
		return metav1.NamespaceAll
	}
	return s.Namespace
}

// RawCluster holds the unprocessed results of a cluster scan. join.go turns
// these into model.CollectedWorkload values.
type RawCluster struct {
	HPAs         []autoscalingv2.HorizontalPodAutoscaler
	Deployments  []appsv1.Deployment
	StatefulSets []appsv1.StatefulSet
	DaemonSets   []appsv1.DaemonSet
	ReplicaSets  []appsv1.ReplicaSet
	Pods         []corev1.Pod
	PodMetrics   []metricsv1beta1.PodMetrics
	Nodes        []corev1.Node
	VPAs         []unstructured.Unstructured

	ServerVersion string
	// VPACRDInstalled is false when the VPA CRD is absent (List returns a
	// NotFound/NoMatch error), in which case VPAs is empty and this is the
	// reason — distinct from "installed but none defined".
	VPACRDInstalled bool

	// ResizeSubresource is true when the core/v1 "pods/resize" subresource is
	// served — direct evidence in-place pod resize is enabled on the API server.
	ResizeSubresource bool

	// MetricsErr / VPAErr / VersionErr hold the actual error from each
	// best-effort read, so diagnostics can report why a capability is missing
	// rather than only that it is.
	MetricsErr error
	VPAErr     error
	VersionErr error
}

// Scan lists every resource truce needs, read-only. A missing VPA CRD is not a
// fatal error: it is recorded in RawCluster.VPACRDInstalled. Missing metrics
// (no metrics-server) is also tolerated — PodMetrics is left empty.
func Scan(ctx context.Context, c *Clients, scope Scope) (*RawCluster, error) {
	ns := scope.namespace()
	opts := metav1.ListOptions{}
	raw := &RawCluster{}

	hpaList, err := c.Typed.AutoscalingV2().HorizontalPodAutoscalers(ns).List(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("listing HPAs: %w", err)
	}
	raw.HPAs = hpaList.Items

	depList, err := c.Typed.AppsV1().Deployments(ns).List(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("listing deployments: %w", err)
	}
	raw.Deployments = depList.Items

	stsList, err := c.Typed.AppsV1().StatefulSets(ns).List(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("listing statefulsets: %w", err)
	}
	raw.StatefulSets = stsList.Items

	dsList, err := c.Typed.AppsV1().DaemonSets(ns).List(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("listing daemonsets: %w", err)
	}
	raw.DaemonSets = dsList.Items

	rsList, err := c.Typed.AppsV1().ReplicaSets(ns).List(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("listing replicasets: %w", err)
	}
	raw.ReplicaSets = rsList.Items

	podList, err := c.Typed.CoreV1().Pods(ns).List(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("listing pods: %w", err)
	}
	raw.Pods = podList.Items

	// Nodes are cluster-scoped; the namespace argument does not apply.
	nodeList, err := c.Typed.CoreV1().Nodes().List(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("listing nodes: %w", err)
	}
	raw.Nodes = nodeList.Items

	// Metrics are best-effort: a cluster without metrics-server should still
	// produce a (degraded) report rather than fail.
	if pmList, err := c.Metrics.MetricsV1beta1().PodMetricses(ns).List(ctx, opts); err == nil {
		raw.PodMetrics = pmList.Items
	} else {
		raw.MetricsErr = err
	}

	// VPA CRD may not be installed; treat list failure as "not installed".
	if vpaList, err := c.Dynamic.Resource(vpaGVR).Namespace(ns).List(ctx, opts); err == nil {
		raw.VPAs = vpaList.Items
		raw.VPACRDInstalled = true
	} else {
		raw.VPAErr = err
	}

	if ver, err := c.Discovery.ServerVersion(); err == nil {
		raw.ServerVersion = ver.GitVersion
	} else {
		raw.VersionErr = err
	}

	// Confirmed evidence in-place resize is enabled: the core/v1 pods/resize
	// subresource is served. Best-effort — discovery failure leaves it false.
	if rl, err := c.Discovery.ServerResourcesForGroupVersion("v1"); err == nil {
		for _, r := range rl.APIResources {
			if r.Name == "pods/resize" {
				raw.ResizeSubresource = true
				break
			}
		}
	}

	return raw, nil
}
