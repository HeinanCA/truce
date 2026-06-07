# truce

[![CI](https://github.com/HeinanCA/truce/actions/workflows/ci.yml/badge.svg)](https://github.com/HeinanCA/truce/actions/workflows/ci.yml)

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
`--tolerance`, `--no-color`, `--prometheus`, `--prometheus-window`,
`--cpu-quantile`. Inherits `--context` / `--kubeconfig`.

## Recommended values for one service (the payoff)

The table tells you *what will happen*; `--service` tells you *what to set* — a
single request value that is better than what VPA or HPA produce alone:

```sh
kubectl truce -n neteera --prometheus http://localhost:9090 \
  --service ml-management --values ./charts/ml-management/values.fda
```

```
RECOMMENDED VALUES — ml-management  (PROVISIONAL — VPA history < 48h)
  VPA target alone → HPA pegs the ceiling (1→10 replicas, -650m cpu). truce keeps it stable.

  ml-management
    cpu:    1 → 360m   (holds the HPA at 50% under peak load (no scale-out))
    memory: 2Gi → 1.7Gi   (floored at observed peak working set +15% (OOM-safe))

  ⚠ This workload hits its HPA ceiling — raise maxReplicas to ≥ 15.

📄 ./charts/ml-management/values.fda
  at ml-management:
    cpu:    1 → 360m
    memory: 2Gi → 1.7Gi
```

- **CPU** is sized to hold the HPA at its target *under peak load* — recovering
  the over-provisioning without triggering a scale-out (VPA's `35m` here would
  drive the HPA to 10 replicas).
- **Memory** is floored at the observed peak (plus margin) — never below real
  usage, so a downsize can't cause an OOM.
- It flags when **maxReplicas** is the real bottleneck — advice neither VPA nor
  HPA gives.
- `--values <file>` reads your env's values file (read-only) and shows the
  current committed requests next to the recommendation, PR-ready. truce never
  writes the file.

## Peak-aware verdicts (recommended)

By default truce predicts from the HPA's **instantaneous** utilization — a single
snapshot. If you run it at a quiet moment, a downsizing rec can look `SAFE` while a
later client spike would drive the HPA to scale out hard (or starve/OOM the pod
before it reacts). truce labels this clearly and warns in the header.

Point it at Prometheus and verdicts are computed from **peak** usage instead:

```sh
kubectl port-forward -n monitoring svc/prometheus 9090:9090 &
kubectl truce -A --prometheus http://localhost:9090 --window 7d
```

- **CPU** uses the P95 of per-pod usage over the window (`--cpu-quantile` to tune)
  → feeds the scale-out prediction.
- **Memory** uses the **max** working set over the window → feeds the OOM check
  (memory is non-compressible, so the worst moment is what matters, not a percentile).

truce runs once and finishes in seconds — the history lives in Prometheus, which
already scrapes your cluster; truce just issues read-only instant queries with the
window baked into the PromQL. Pods are matched by name pattern per workload kind
(so historical, since-replaced pods are still counted) using the standard cAdvisor
metrics `container_cpu_usage_seconds_total` and `container_memory_working_set_bytes`.

**Exit codes:** `0` clean · `1` operational error · `3` `--fail-on` matched.

## Cost estimate (built-in pricing)

truce translates the freed CPU/memory into a dollar figure. Pricing is built in
and provider-pluggable — **no manual price entry on AWS**:

- **AWS nodes + resolvable AWS credentials → the AWS backend (default).** Each
  node is priced by its `karpenter.sh/capacity-type`: on-demand from the EC2
  **Price List API** (`pricing:GetProducts`, keyed on instance type + region),
  spot from **`ec2:DescribeSpotPriceHistory`** (latest price for the type + AZ).
  Lookups are cached on disk (`--pricing-cache-ttl`, default 24h).
- **Otherwise → static.** `--pricing-file` (an `instanceType: USD/hr` map) or a
  flat `--node-cost`.
- **No price at all → PRICE-MISSING.** truce still reports node/resource savings;
  only the dollar figure drops out. `--no-pricing` forces this path.

The cluster bottom line is headlined *"up to $X/month (estimated, assumes
consolidation; spot priced at current)"*. Spot is variable and per-AZ, so
spot-derived numbers are dated; the blended `$/node-hr` notes how many nodes are
spot vs on-demand. A type that fails to price is flagged, not fatal.

**AWS IAM (read-only, granted via IRSA or the node instance role — NOT Kubernetes
RBAC):**

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Action": ["pricing:GetProducts", "ec2:DescribeSpotPriceHistory"],
    "Resource": "*"
  }]
}
```

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
- **Snapshot mode is blind to traffic peaks.** Without `--prometheus`, verdicts use
  the HPA's instantaneous utilization, so a verdict measured at low traffic can
  understate spike-time scale-out and OOM risk. truce warns when in this mode; use
  the peak-aware mode above for verdicts that survive your traffic. Pod matching in
  peak mode relies on standard naming conventions.
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
- **KEDA is recognized but not predicted.** truce detects KEDA-managed workloads
  (via the generated HPA / `ScaledObject`), labels them `KEDA:<trigger>`, and
  notes that request changes are safe because scaling is external. It cannot
  predict KEDA's replica count (driven by the external trigger it doesn't read)
  or its scale-to-zero behavior (invisible to the HPA). A KEDA `cpu`/`memory`
  trigger, however, surfaces as a `Resource` metric and is predicted normally.
- **Live requests are read from a representative running pod.** If pods within one
  workload have divergent requests, the representative may not capture every case.

## License

[Apache-2.0](LICENSE).
