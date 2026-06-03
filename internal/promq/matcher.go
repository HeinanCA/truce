// Package promq queries Prometheus for peak resource usage over a window and
// enriches collected workloads with it, so the engine can predict against
// traffic peaks (P95 CPU, max memory) instead of a single instantaneous
// snapshot. It is read-only: it issues PromQL instant queries only.
package promq

import (
	"regexp"

	"github.com/heinanca/truce/internal/model"
)

// podRegex builds a Prometheus pod-name matcher for a workload from the standard
// naming conventions, so historical pods (which have since been replaced) are
// still matched over the query window. Prometheus label regexes are fully
// anchored (RE2 whole-string match), so these match only pods of the workload's
// own shape — "api" will not match "api-gateway-..." because the segment count
// differs.
//
//	Deployment:  <name>-<replicaset-hash>-<pod-suffix>
//	StatefulSet: <name>-<ordinal>
//	DaemonSet:   <name>-<pod-suffix>
func podRegex(w model.Workload) string {
	name := regexp.QuoteMeta(w.Name)
	switch w.Kind {
	case model.KindStatefulSet:
		return name + "-[0-9]+"
	case model.KindDaemonSet:
		return name + "-[a-z0-9]{5}"
	default: // Deployment (and anything ReplicaSet-backed)
		return name + "-[a-z0-9]+-[a-z0-9]{5}"
	}
}
