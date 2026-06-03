// Package collect performs all cluster I/O for truce. It is the only package
// that imports client-go, k8s.io/api, or the metrics client; everything it
// returns is expressed in internal/model types so the engine stays free of any
// Kubernetes dependency. Every call here is a read (get/list) — collect never
// creates, updates, patches, or deletes.
package collect

import (
	"fmt"

	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
)

// Clients bundles the read-only clients truce needs. Build it once with
// NewClients and pass it to Scan.
type Clients struct {
	Typed     kubernetes.Interface
	Dynamic   dynamic.Interface
	Metrics   metricsclient.Interface
	Discovery discovery.DiscoveryInterface
}

// NewClients builds the typed, dynamic, metrics, and discovery clients from a
// RESTClientGetter (satisfied by genericclioptions.ConfigFlags, which carries
// --context/--kubeconfig).
func NewClients(getter genericclioptions.RESTClientGetter) (*Clients, error) {
	cfg, err := getter.ToRESTConfig()
	if err != nil {
		return nil, fmt.Errorf("loading REST config: %w", err)
	}

	typed, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building typed client: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building dynamic client: %w", err)
	}
	mc, err := metricsclient.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building metrics client: %w", err)
	}
	disco, err := getter.ToDiscoveryClient()
	if err != nil {
		return nil, fmt.Errorf("building discovery client: %w", err)
	}

	return &Clients{
		Typed:     typed,
		Dynamic:   dyn,
		Metrics:   mc,
		Discovery: disco,
	}, nil
}
