package collect

import (
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"

	"github.com/heinanca/truce/internal/model"
)

// CollectResult is the output of joining a RawCluster: the assembled workloads
// plus collection-level facts the header and detect layer consume.
type CollectResult struct {
	Workloads       []model.CollectedWorkload
	SkippedBarePods int
	VPACRDInstalled bool
	VPAPresent      bool // CRD installed AND at least one VPA matched a workload
}

// workloadAcc accumulates the inputs for one workload during the join.
type workloadAcc struct {
	wl         model.Workload
	template   []corev1.Container // pod-template containers, for template/drift requests
	pods       []corev1.Pod
	hpa        model.HPAInfo
	vpaRecs    map[string]model.VPARec
	vpaPicked  bool
	vpaCreated time.Time
}

func key(kind model.WorkloadKind, ns, name string) string {
	return string(kind) + "/" + ns + "/" + name
}

// Join turns raw cluster reads into per-workload CollectedWorkload values,
// joining purely on owner references and the workload triple — never on label
// selectors. Bare pods (no controller owner) are counted and skipped.
func Join(raw *RawCluster) CollectResult {
	accs := map[string]*workloadAcc{}

	// 1. Seed accumulators from the three workload kinds.
	for i := range raw.Deployments {
		d := raw.Deployments[i]
		k := key(model.KindDeployment, d.Namespace, d.Name)
		accs[k] = newAcc(model.Workload{
			Kind: model.KindDeployment, Namespace: d.Namespace, Name: d.Name,
			Replicas: d.Status.Replicas, Annotations: d.Annotations,
		}, d.Spec.Template.Spec.Containers)
	}
	for i := range raw.StatefulSets {
		s := raw.StatefulSets[i]
		k := key(model.KindStatefulSet, s.Namespace, s.Name)
		accs[k] = newAcc(model.Workload{
			Kind: model.KindStatefulSet, Namespace: s.Namespace, Name: s.Name,
			Replicas: s.Status.Replicas, Annotations: s.Annotations,
		}, s.Spec.Template.Spec.Containers)
	}
	for i := range raw.DaemonSets {
		ds := raw.DaemonSets[i]
		k := key(model.KindDaemonSet, ds.Namespace, ds.Name)
		accs[k] = newAcc(model.Workload{
			Kind: model.KindDaemonSet, Namespace: ds.Namespace, Name: ds.Name,
			Replicas: ds.Status.DesiredNumberScheduled, Annotations: ds.Annotations,
		}, ds.Spec.Template.Spec.Containers)
	}

	// 2. Index ReplicaSets for the Pod->RS->Deployment hop.
	rsIndex := map[string]appsv1.ReplicaSet{}
	for i := range raw.ReplicaSets {
		rs := raw.ReplicaSets[i]
		rsIndex[rs.Namespace+"/"+rs.Name] = rs
	}

	// 3. Attach pods via owner-reference walk.
	skipped := 0
	for i := range raw.Pods {
		pod := raw.Pods[i]
		k, ok := podWorkloadKey(pod, rsIndex)
		if !ok {
			if metav1.GetControllerOf(&pod) == nil {
				skipped++ // bare pod
			}
			continue
		}
		if acc := accs[k]; acc != nil {
			acc.pods = append(acc.pods, pod)
		}
	}

	// 4. Attach HPAs by scaleTargetRef.
	for i := range raw.HPAs {
		hpa := raw.HPAs[i]
		ref := hpa.Spec.ScaleTargetRef
		k := key(model.WorkloadKind(ref.Kind), hpa.Namespace, ref.Name)
		if acc := accs[k]; acc != nil {
			acc.hpa = hpaToInfo(hpa)
		}
	}

	// 5. Attach VPA recommendations by targetRef.
	vpaMatched := false
	for i := range raw.VPAs {
		pv, ok := parseVPA(raw.VPAs[i])
		if !ok {
			continue
		}
		k := key(model.WorkloadKind(pv.TargetKind), pv.Namespace, pv.TargetName)
		if acc := accs[k]; acc != nil {
			acc.vpaRecs = pv.Recs
			acc.vpaPicked = true
			acc.vpaCreated = pv.Created.Time
			vpaMatched = true
		}
	}

	// 5b. Mark KEDA-managed workloads. KEDA generates the HPA matched above; this
	// adds the trigger context. When KEDA has scaled to zero there is no live
	// HPA, so we synthesize Present=true with no metrics → DECOUPLED, not NO HPA.
	for i := range raw.ScaledObjects {
		so, ok := parseScaledObject(raw.ScaledObjects[i])
		if !ok {
			continue
		}
		k := key(model.WorkloadKind(so.TargetKind), so.Namespace, so.TargetName)
		if acc := accs[k]; acc != nil {
			acc.hpa.Present = true
			acc.hpa.ManagedByKEDA = true
			acc.hpa.KEDATriggers = so.Triggers
		}
	}

	// 6. Index pod metrics by namespace/name for working-set lookup.
	pmIndex := map[string]metricsv1beta1.PodMetrics{}
	for i := range raw.PodMetrics {
		pm := raw.PodMetrics[i]
		pmIndex[pm.Namespace+"/"+pm.Name] = pm
	}

	// 7. Assemble per-container analyses.
	var out []model.CollectedWorkload
	for _, acc := range accs {
		out = append(out, model.CollectedWorkload{
			Workload:   acc.wl,
			HPA:        acc.hpa,
			Containers: buildContainers(acc, pmIndex),
			VPACreated: acc.vpaCreated,
		})
	}

	return CollectResult{
		Workloads:       out,
		SkippedBarePods: skipped,
		VPACRDInstalled: raw.VPACRDInstalled,
		VPAPresent:      raw.VPACRDInstalled && vpaMatched,
	}
}

func newAcc(wl model.Workload, template []corev1.Container) *workloadAcc {
	return &workloadAcc{wl: wl, template: template, vpaRecs: map[string]model.VPARec{}}
}

// podWorkloadKey resolves a pod to its owning workload key via owner references.
// Pod->ReplicaSet->Deployment is two hops; StatefulSet/DaemonSet is one. Returns
// false for bare pods or pods owned by untracked controllers (e.g. Jobs).
func podWorkloadKey(pod corev1.Pod, rsIndex map[string]appsv1.ReplicaSet) (string, bool) {
	ctrl := metav1.GetControllerOf(&pod)
	if ctrl == nil {
		return "", false
	}
	switch ctrl.Kind {
	case "ReplicaSet":
		rs, ok := rsIndex[pod.Namespace+"/"+ctrl.Name]
		if !ok {
			return "", false
		}
		rsCtrl := metav1.GetControllerOf(&rs)
		if rsCtrl == nil || rsCtrl.Kind != string(model.KindDeployment) {
			return "", false // bare ReplicaSet, not a tracked workload
		}
		return key(model.KindDeployment, pod.Namespace, rsCtrl.Name), true
	case string(model.KindStatefulSet):
		return key(model.KindStatefulSet, pod.Namespace, ctrl.Name), true
	case string(model.KindDaemonSet):
		return key(model.KindDaemonSet, pod.Namespace, ctrl.Name), true
	default:
		return "", false
	}
}

// buildContainers produces the per-container analyses for a workload: live
// requests from a representative running pod, template requests for drift, VPA
// recs matched by name, and the max memory working set across the pods.
func buildContainers(acc *workloadAcc, pmIndex map[string]metricsv1beta1.PodMetrics) []model.ContainerAnalysis {
	rep := representativePod(acc.pods)
	templateReq := map[string]model.Resources{}
	for _, c := range acc.template {
		templateReq[c.Name] = resourcesFromList(c.Resources.Requests)
	}
	maxWS := maxWorkingSet(acc.pods, pmIndex)

	// Container set: prefer the template ordering, fall back to live pod.
	names := containerNames(acc.template, rep)

	var out []model.ContainerAnalysis
	for _, name := range names {
		ca := model.ContainerAnalysis{
			Name:             name,
			Requests:         liveRequests(rep, name, templateReq[name]),
			TemplateRequests: templateReq[name],
		}
		if rec, ok := acc.vpaRecs[name]; ok {
			ca.VPA = rec
			ca.HasVPA = true
		}
		if ws, ok := maxWS[name]; ok {
			ca.CurrentMemWorkingSet = model.Int64(ws)
		}
		out = append(out, ca)
	}
	return out
}

// representativePod returns the first Running pod, or the first pod if none are
// Running, or the zero Pod when the workload has no pods.
func representativePod(pods []corev1.Pod) corev1.Pod {
	for _, p := range pods {
		if p.Status.Phase == corev1.PodRunning {
			return p
		}
	}
	if len(pods) > 0 {
		return pods[0]
	}
	return corev1.Pod{}
}

// containerNames returns the ordered, de-duplicated container names from the
// template, then any extra names seen only on the live pod.
func containerNames(template []corev1.Container, rep corev1.Pod) []string {
	seen := map[string]bool{}
	var names []string
	for _, c := range template {
		if !seen[c.Name] {
			seen[c.Name] = true
			names = append(names, c.Name)
		}
	}
	for _, c := range rep.Spec.Containers {
		if !seen[c.Name] {
			seen[c.Name] = true
			names = append(names, c.Name)
		}
	}
	return names
}

// liveRequests reads the actually-applied requests for a container from the live
// pod's container status (which reflects in-place resize), falling back to the
// pod spec, then to the template requests.
func liveRequests(pod corev1.Pod, name string, template model.Resources) model.Resources {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == name && cs.Resources != nil && len(cs.Resources.Requests) > 0 {
			return resourcesFromList(cs.Resources.Requests)
		}
	}
	for _, c := range pod.Spec.Containers {
		if c.Name == name && len(c.Resources.Requests) > 0 {
			return resourcesFromList(c.Resources.Requests)
		}
	}
	return template
}

// maxWorkingSet returns the maximum memory working set in bytes per container
// across all of the workload's pods.
func maxWorkingSet(pods []corev1.Pod, pmIndex map[string]metricsv1beta1.PodMetrics) map[string]int64 {
	out := map[string]int64{}
	for _, p := range pods {
		pm, ok := pmIndex[p.Namespace+"/"+p.Name]
		if !ok {
			continue
		}
		for _, c := range pm.Containers {
			if q, ok := c.Usage[corev1.ResourceMemory]; ok {
				v := q.Value()
				if cur, seen := out[c.Name]; !seen || v > cur {
					out[c.Name] = v
				}
			}
		}
	}
	return out
}
