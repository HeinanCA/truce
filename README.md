# truce

**A read-only Kubernetes rightsizing advisor that predicts what your HPA will do
*before* you apply a VPA recommendation — and shows the resulting footprint delta.**

Every other rightsizing tool tells you "shrink this container's CPU request from
1000m to 500m." None of them tell you that doing so will make your
HorizontalPodAutoscaler scale your Deployment from 4 replicas to 7 — turning a
predicted *saving* into a *net increase* in cluster footprint.

That backfire is exactly what truce catches:

```
WORKLOAD             HPA      NOW        VPA REC   PREDICT  Δ FOOTPRINT  VERDICT    FLAGS
Deployment/prod/api  cpu:70%  4 × 1 / —  500m / —  4→7      +100m / 0    SCALE-OUT  —

⚠ 1 backfire(s) — a rec that grows footprint (HPA scales out past the savings):
  • Deployment/prod/api: SCALE-OUT → 4→7 replicas (+100m / 0)
```

A VPA recommendation lowers the per-pod CPU request. The HPA divides usage by that
request to compute utilization — so a smaller request means *higher* utilization,
which pushes the HPA above its target and triggers a scale-out. truce models that
interaction and reports the verdict honestly: a prediction labeled **predicted**,
never **confirmed**.

## How the prediction works

For each HPA `Resource(Utilization)` or `ContainerResource(Utilization)` metric:

```
predicted_util     = current_util × (R_old / R_new)      # current_util from the HPA's own status
ratio              = predicted_util / target_util
predicted_replicas = clamp(ceil(N × ratio), min, max)

ratio > 1 + tol_up    → SCALE-OUT   (HITS CEILING if clamped to maxReplicas)
ratio < 1 − tol_down  → SCALE-IN
otherwise             → SAFE

Δ footprint = predicted_replicas × R_new − N × R_old     # the headline
```

- `R_old` / `R_new` are the request sums the metric considers: **all** containers
  for a pod-level `Resource` metric, or the **single named container** for a
  `ContainerResource` metric. `R_new` uses the VPA target.
- The usage basis is the **HPA's own `status.currentMetrics`** — not an
  independent metrics-server reading, and never derived from the VPA target
  (which is P90 + margin, not live usage).
- Tolerances come from the HPA's `spec.behavior.scaleUp/scaleDown.tolerance`
  (Kubernetes ≥ 1.33) when set, otherwise from `--tolerance` (default 0.10).

## Verdicts

| Verdict | Meaning |
|---------|---------|
| `SAFE` | Applying the rec keeps the HPA within tolerance. |
| `SCALE-OUT` | The smaller request pushes utilization above target → HPA adds replicas. |
| `HITS CEILING` | Predicted scale-out clamps at `maxReplicas`. |
| `SCALE-IN` | The larger request drops utilization below target → HPA removes replicas. |
| `OOM RISK` | VPA memory target is below current working set — applying may OOM-kill the pod (overrides the HPA verdict). |
| `DECOUPLED` | The HPA's metric (External/Object/Pods/AverageValue) does not depend on requests. |
| `NO HPA` | A VPA but no HPA — plain rightsizing, apply freely. |

Advisory flags layer on top: `LOW-CONF` (VPA younger than ~48h or wide bounds),
`UNRELIABLE` (an HPA-considered container has no request, so the basis can't be
trusted), `GITOPS` (Argo CD / Flux managed — live edits revert), `RESTART`
(in-place resize unavailable, so applying restarts the pod).

## Install

```sh
# build the kubectl plugin (binary name follows the krew convention)
go build -o kubectl-truce ./cmd/kubectl-truce
mv kubectl-truce /usr/local/bin/

# then invoke either way:
kubectl-truce -A
kubectl truce -A
```

Apply the read-only RBAC for the identity that runs it:

```sh
kubectl apply -f deploy/rbac.yaml
```

## Usage

```sh
kubectl truce                          # current namespace, table output
kubectl truce -A                       # all namespaces
kubectl truce -n prod -o wide          # per-container detail + util + tolerance
kubectl truce -A --problems-only       # only SCALE-OUT/IN, HITS CEILING, OOM
kubectl truce -A --only scale-out,oom  # filter to specific verdicts
kubectl truce -A -o json | jq          # full model for dashboards
kubectl truce -n prod -o diff          # apply-ready patches (printed, never applied)

# CI gate — fail the pipeline if applying recs would backfire:
kubectl truce -A --fail-on scale-out,hits-ceiling,oom-risk
```

Flags: `-n/--namespace`, `-A/--all-namespaces`, `-o table|wide|json|diff`,
`--sort delta|name|verdict`, `--only`, `--problems-only`, `--fail-on`,
`--tolerance`, `--no-color`. Inherits `--context` / `--kubeconfig`.

**Exit codes:** `0` clean · `1` operational error · `3` `--fail-on` matched.

## Read-only guarantee

truce uses only `get`/`list`. It contains no `create`, `update`, `patch`,
`delete`, or `apply` calls anywhere — the `-o diff` output is *printed* for you to
review and apply yourself. The shipped RBAC grants nothing more than read access.

## Limitations

Honest about what truce does **not** do:

- **It is a predictor, not an oracle.** The scale prediction is a first-order
  model of the HPA algorithm. It does not simulate stabilization windows, scaling
  policies, rate limits, or HPA behaviors beyond taking the max across metrics.
  Output is labeled `predicted`, never `confirmed`.
- **It needs the HPA's current utilization.** If the HPA hasn't reported
  `status.currentMetrics` for a metric (just created, or basis unreliable), that
  metric is skipped and flagged `UNRELIABLE` rather than guessed.
- **VPA recommendations are the input.** No VPA CRD or no VPA objects → nothing to
  advise; truce tells you so and how to install the VPA.
- **OOM check needs metrics-server.** Without it, the `OOM RISK` check is disabled
  (the HPA prediction itself is unaffected — it reads HPA status, not
  metrics-server). truce reports this in the header.
- **In-place-resize status is layered and partly inferred.** The capability tier
  is inferred from the server version; "enabled" and "in use" are evidence-backed.
  Node readiness is a heuristic (kubelet version) — a lagging kubelet *may* still
  restart on resize.
- **Workloads only, joined by owner references.** Deployments, StatefulSets, and
  DaemonSets. Bare pods are skipped (and counted). Matching is via ownerReferences,
  never label selectors, so unusual ownership chains may not resolve.
- **DaemonSets have no HPA**, so they only ever produce `NO HPA` rightsizing.
- **Live requests are read from a representative running pod.** If pods within one
  workload have divergent requests, the representative may not capture every case.

## License

[Apache-2.0](LICENSE).
