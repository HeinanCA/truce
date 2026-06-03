package model

// WorkloadKind enumerates the workload types truce inspects. Bare pods are not
// a kind here: they are skipped during collection with a note.
type WorkloadKind string

const (
	KindDeployment  WorkloadKind = "Deployment"
	KindStatefulSet WorkloadKind = "StatefulSet"
	KindDaemonSet   WorkloadKind = "DaemonSet"
)

// Workload identifies a scalable workload. The triple (Kind, Namespace, Name)
// is the join key for HPAs, VPAs, and pods throughout truce.
type Workload struct {
	Kind      WorkloadKind
	Namespace string
	Name      string

	// Replicas is the current observed replica count (DaemonSets use the number
	// of scheduled nodes). For DaemonSets, HPA prediction does not apply.
	Replicas int32

	// Annotations is the workload's annotation set, used for GitOps detection.
	Annotations map[string]string
}

// Key returns the canonical join key "Kind/Namespace/Name".
func (w Workload) Key() string {
	return string(w.Kind) + "/" + w.Namespace + "/" + w.Name
}
