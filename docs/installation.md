# Installing and configuring Cost Deck

For platform engineers and cluster administrators who deploy Cost Deck and connect it to
SSO, cloud accounts, monitoring and chat.

## Prerequisites

- Kubernetes 1.26 or newer, and `cluster-admin` rights for the install (CRDs, ClusterRole).
- Helm 3.8+ or Helm 4 (OCI charts).
- A source of usage data: [metrics-server](https://github.com/kubernetes-sigs/metrics-server)
  for live numbers, and optionally VictoriaMetrics (or any Prometheus-compatible API that
  scrapes cAdvisor) for history-based cost and right-sizing.

## Install

### Helm (recommended)

```bash
helm upgrade --install costdeck-operator \
  oci://ghcr.io/migalsp/costdeck/charts/costdeck-operator \
  --version <version> --namespace costdeck --create-namespace -f my-values.yaml
```

Releases are listed on the [releases page](https://github.com/migalsp/costdeck/releases).
Images and charts are signed keyless with cosign. To verify before installing:

```bash
cosign verify ghcr.io/migalsp/costdeck/costdeck-operator:<version> \
  --certificate-identity-regexp 'https://github.com/migalsp/costdeck/.github/workflows/release.yml@.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

### Plain manifests

Every release also has an `install.yaml` asset (namespace `costdeck`):

```bash
kubectl apply -f https://github.com/migalsp/costdeck/releases/download/v<version>/install.yaml
```

The operator generates the admin password on first start and keeps it in the
`costdeck-admin-credentials` Secret (user `costdeck-admin`). The Helm chart is the
better-maintained path: it also supports Ingress, ServiceMonitor and pod security
settings, and keeps the CRDs on uninstall.

### Check the installation

```bash
kubectl get pods -n costdeck
kubectl get crds | grep finops.costdeck.io     # 5 CRDs
kubectl logs -n costdeck deploy/costdeck-operator
```

## Sign in

The chart creates a local admin account on first install:

```bash
kubectl get secret costdeck-operator-admin-credentials -n costdeck \
  -o jsonpath='{.data.password}' | base64 -d; echo
```

Sign in as `costdeck-admin`. Without an Ingress, use a port-forward:

```bash
kubectl port-forward -n costdeck svc/costdeck-operator-api 8082:8082
```

To give the team a URL, enable the Ingress (terminate TLS there):

```yaml
ingress:
  enabled: true
  className: nginx
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt
  hosts:
    - host: costdeck.example.com
      paths: [{ path: /, pathType: Prefix }]
  tls:
    - secretName: costdeck-tls
      hosts: [costdeck.example.com]
```

Everything below is configured in the dashboard under **Settings** (admin role) and
stored in the `CostDeckConfig` named `default` in the operator namespace. Credentials go to
Secrets in that namespace, never into the custom resource.

## Roles

| Role | Can |
| :--- | :--- |
| viewer | See everything; use the read-only assistant and MCP tools |
| operator | Also start and stop schedules, hand them back to the schedule, generate AI reports, read logs |
| admin | Also create, edit and delete schedules, change settings, manage API tokens |

## Single sign-on with Microsoft Entra ID

1. In the Entra admin center, create an **App registration**.
2. Under **Authentication → Add a platform → Web**, add the redirect URI that matches how you
   want to sign in:
   - `https://costdeck.example.com/api/auth/entra/callback` sends users straight back to
     the API (server-side flow), or
   - `https://costdeck.example.com/auth/callback` sends them back to the dashboard, which
     finishes the sign-in. Register it under **Web**, not *Single-page application*: the
     code is redeemed by the server with the client secret, and Entra refuses that for
     SPA redirect URIs (AADSTS9002327).
3. Under **Certificates & secrets**, create a client secret.
4. Under **Token configuration → Add groups claim**, choose security groups (or "Groups
   assigned to the application"). Users in more than ~200 groups get no groups claim;
   assign groups to the application or use app roles named `admin`, `operator` and
   `viewer` instead.
5. In Cost Deck, open **Settings → Single sign-on** and fill in:
   - **Tenant ID**: a directory ID or domain. `organizations` accepts work accounts from
     any tenant; then only the group mapping grants access. App roles and auto-provision
     are ignored, because any tenant's admins could abuse them.
   - **Client ID** and **Client secret**.
   - **Redirect URL**: optional, derived from the request host when empty.
   - **Group → role mapping**: Entra group object IDs mapped to roles. The most privileged
     match wins.
   - **Default role**: given to users who match no mapping.
   - **Auto-provision**: off means only users matched by a group or app role may sign in.
   - **Authority host**: only for sovereign clouds.
   - **Skip TLS verification**: only for a TLS-inspecting proxy; never on the open internet.

   **Test connection** checks the tenant and the client credentials. Once SSO works you
   can turn off local login. Keep the admin password: it is the way back in if SSO breaks.

## API tokens

**Settings → API tokens** issues tokens (`cdk_…`) bound to a role, for
scripts and MCP clients. Send them as `Authorization: Bearer cdk_…`. Only a hash is
stored; the token is shown once.

## Usage data and monitoring

**Settings → Usage metrics**:

- **VictoriaMetrics endpoint**: the base URL of a Prometheus-compatible query API, for
  example `http://vmselect:8481/select/0/prometheus` or `http://victoria-metrics:8428`.
  Cost Deck queries `container_cpu_usage_seconds_total` and
  `container_memory_working_set_bytes`, so cAdvisor must be scraped.
- **Label selector**: narrows a shared VictoriaMetrics down to this cluster, for example
  `cluster="prod-eu"`.
- Bearer token, basic auth or a custom CA, as needed.

**Test connection** reports both reachability and whether container metrics actually
exist; "connected but no series" is the most common setup mistake. The connection status
is also shown in `kubectl get costdeckconfig default -n costdeck -o yaml`.

Without VictoriaMetrics, everything falls back to metrics-server: live numbers only, and
right-sizing advice based on a single reading.

## Cost rates

**Settings → Prices**:

- **Custom rates** (CPU core-hour, memory GiB-hour, currency) always win: use them for
  negotiated prices, on-premises clusters or Google Cloud.
- **Volumes and load balancers** are priced at list prices per disk type (gp3, Premium
  SSD, pd-balanced, …) and per load balancer, or at your own storage and load balancer
  rates, which you need when the currency is not USD. Reading them needs list access to
  PersistentVolumeClaims, PersistentVolumes, StorageClasses, Services and EndpointSlices,
  which the chart's ClusterRole grants.
- **Cloud list prices** derive per-core and per-GiB rates from the list prices of the
  instance types your nodes actually run: the AWS Price List (needs
  `pricing:GetProducts`) or the public Azure Retail Prices API (no credentials). Spot
  nodes are counted at the regular rate. Google Cloud clusters keep the estimate.
- Otherwise a list-price heuristic is used, and every cost figure says which basis it came
  from.

## Reconciling with the cloud bill

**Settings → Cloud bill** reads what the cloud charged for
the cluster's nodes over the week ending two days ago, compares it with list prices for
the same days, and scales every compute cost by the ratio. It uses the credentials of the
matching cloud provider, or the pod identity, and needs read access to the bill:

| Cloud | Reads | Grant |
| :--- | :--- | :--- |
| AWS | Amortized EC2 cost from Cost Explorer, filtered by a tag (`aws:eks:cluster-name` by default) | `ce:GetCostAndUsage`; activate the tag as a cost allocation tag in the billing console |
| Azure | Amortized virtual machine cost of the AKS node resource group from Cost Management | Cost Management Reader on the node resource group |
| Google Cloud | Compute Engine cost with credits from the BigQuery billing export, by the `goog-k8s-cluster-name` label | BigQuery Job User in the provider's project, BigQuery Data Viewer on the export dataset |

The result, including why the last attempt failed, is in
`kubectl get costdeckconfig default -n costdeck -o jsonpath='{.status.billing}'`. A ratio
below 10% or above 300% of list price is never applied.

## AWS: Aurora, EC2 and pricing

**Settings → Cloud accounts → AWS**. Prefer IRSA (or EKS Pod Identity) over static keys:

```yaml
serviceAccount:
  annotations:
    eks.amazonaws.com/role-arn: arn:aws:iam::123456789012:role/costdeck-operator
```

Minimal IAM policy:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    { "Effect": "Allow", "Action": ["rds:DescribeDBClusters", "ec2:DescribeInstances", "pricing:GetProducts"], "Resource": "*" },
    { "Effect": "Allow", "Action": ["rds:StartDBCluster", "rds:StopDBCluster"], "Resource": "arn:aws:rds:*:123456789012:cluster:*" },
    { "Effect": "Allow", "Action": ["ec2:StartInstances", "ec2:StopInstances"], "Resource": "arn:aws:ec2:*:123456789012:instance/*" }
  ]
}
```

Narrow the start/stop resources with tags (`aws:ResourceTag/…`) to match the discovery
tags you configure. Discovered resources of every cloud are added to a schedule under
**Edit → Start order → Cloud resources**.

## Azure: virtual machines and flexible servers

**Settings → Cloud accounts → Microsoft Azure**: the subscription ID and either a
service principal (tenant ID, client ID, client secret) or nothing, to use the pod
identity: AKS workload identity or a managed identity.

The identity needs to read and start/stop the resources. The built-in **Virtual Machine
Contributor** role covers VMs; a narrower custom role:

```json
{
  "Name": "CostDeck scheduler",
  "Actions": [
    "Microsoft.Resources/subscriptions/read",
    "Microsoft.Compute/virtualMachines/read",
    "Microsoft.Compute/virtualMachines/instanceView/read",
    "Microsoft.Compute/virtualMachines/start/action",
    "Microsoft.Compute/virtualMachines/deallocate/action",
    "Microsoft.DBforPostgreSQL/flexibleServers/read",
    "Microsoft.DBforPostgreSQL/flexibleServers/start/action",
    "Microsoft.DBforPostgreSQL/flexibleServers/stop/action",
    "Microsoft.DBforMySQL/flexibleServers/read",
    "Microsoft.DBforMySQL/flexibleServers/start/action",
    "Microsoft.DBforMySQL/flexibleServers/stop/action"
  ],
  "AssignableScopes": ["/subscriptions/<subscription-id>"]
}
```

VMs are **deallocated**, not just powered off, so their compute stops billing. Azure
starts a stopped flexible server again automatically after seven days; the schedule
stops it again at its next down window.

## Google Cloud: Compute Engine and Cloud SQL

**Settings → Cloud accounts → Google Cloud**: a project ID and a service account key
(JSON), or nothing to use GKE workload identity. Only `service_account` keys are
accepted. The account needs `roles/compute.instanceAdmin.v1` and `roles/cloudsql.editor`,
or custom roles with `compute.instances.list|get|start|stop`, `compute.projects.get`,
`cloudsql.instances.list|get|update`.

Cloud SQL has no start/stop call: CostDeck sets the instance's activation policy to
`NEVER` to stop it and `ALWAYS` to start it.

## Webex

**Settings → Notifications → Webex**: a bot token from
[developer.webex.com](https://developer.webex.com/my-apps) and the ID of the space your team
uses for CostDeck. Scaling commands are accepted only in that space, so its members are who
may scale. Without a space the bot only answers `list` and `status`, because anyone on
Webex can message a bot directly.

- **Polling** (default): the operator reads new messages every 10 seconds. Nothing has to
  reach the cluster from outside. In group spaces the bot only sees messages that
  @mention it.
- **Webhook**: set a webhook secret and point a Webex webhook at
  `https://costdeck.example.com/api/webex/webhook`. Requests are verified with HMAC.
  Polling stops while a secret is set.
- **Notify transitions**: posts to the space when a schedule finishes scaling up or down.
  Needs a space ID.
- **Budget and anomaly alerts**: budgets (under **Budgets & Alerts**, stored in
  `spec.budgets`) alert at their thresholds and when the month is heading over, and
  anomaly alerts when a namespace's daily cost jumps. They are posted to the space when
  there is one and always listed in the dashboard.
- **Cost digest**: under **Reports → Scheduled digest**, a weekly (Mondays) or monthly
  (the 1st) summary of cost, savings and the top opportunities is posted to the same
  space. It is stored in `spec.reports.digest` of the `CostDeckConfig`.

Commands (`help` lists them):

```
list
status group <name>            status config <namespace>
scale group <name> up|down [for 4h | until next | forever]
scale config <namespace> up|down [...]
resume group <name>            resume config <namespace>
```

Without a duration a scale command holds until the next scheduled change. When several
clusters share one space, set `spec.clusterName` in the `CostDeckConfig` of each cluster
and start commands with that name.

## AI assistant and MCP

**Settings → AI assistant**: Anthropic (Claude), any OpenAI-compatible endpoint (OpenAI,
Azure OpenAI, vLLM, Ollama) or Gemini. The assistant reads live data through tools. Actions
(scaling, resuming, reverting) appear as confirmation cards and run only after a user with
the operator role confirms.

**Settings → MCP server** serves the same tools over MCP (Streamable HTTP) at
`https://costdeck.example.com/mcp`, authenticated with an API token:

```bash
claude mcp add --transport http costdeck https://costdeck.example.com/mcp \
  --header "Authorization: Bearer cdk_..."
```

## Scaling: what happens to workloads

- **Schedules** (ScalingGroups) scale Deployments and StatefulSets in their namespaces to
  zero outside their hours and restore the recorded replica counts afterwards. A workload
  scaled up with no recorded count gets one replica.
- **Start order**: stages start top to bottom and stop bottom to top. Namespaces not in
  any stage start last and stop first.
- **Dependencies**: a schedule that starts after another waits until it is fully up, and
  keeps it up while running. An **on-demand** schedule has no hours of its own.
- **Overrides**: Start now and Scale down now ignore the schedule until the next scheduled
  change, for a chosen time, or until you click **Follow schedule**.
- **Workload rules** (on the namespace page): workloads that never scale down, and the
  order workloads start in. They apply under any schedule.
- While a namespace is down, its **CronJobs are suspended** and **KEDA ScaledObjects are
  paused**. Both are restored on scale-up. An **HPA** stops acting on a workload with zero
  replicas and takes over again once replicas are restored.
- A namespace belongs to at most one schedule. If two claim it, the older one wins and
  the newer one reports the conflict.
- The replica count to restore is kept on each workload in the
  `costdeck.io/original-replicas` annotation, written in the same change that scales it
  down and removed once it is restored.

### GitOps (Argo CD, Flux)

A GitOps controller that syncs `spec.replicas` from Git scales the workloads straight
back up. Tell it to ignore the fields Cost Deck manages. For Argo CD:

```yaml
spec:
  ignoreDifferences:
    - group: apps
      kind: Deployment
      jsonPointers: [/spec/replicas, /metadata/annotations/costdeck.io~1original-replicas]
    - group: apps
      kind: StatefulSet
      jsonPointers: [/spec/replicas, /metadata/annotations/costdeck.io~1original-replicas]
  syncPolicy:
    syncOptions: [RespectIgnoreDifferences=true]
```

For Flux, leave `spec.replicas` out of the manifests in Git, as you would with an HPA.

## Right-sizing advice

The namespace page lists containers whose requests are well above what they use, with a
recommended value, the estimated monthly saving and a YAML snippet to apply through your
own pipeline. With VictoriaMetrics the advice uses p95 CPU and peak memory over up to 14
days plus 20% headroom; with metrics-server only, a single reading plus 50% headroom.
Cost Deck does not change requests itself.

## Prometheus metrics

The manager exports metrics on the `costdeck-operator-metrics` Service (port 8080):

| Metric | Labels | Meaning |
| :--- | :--- | :--- |
| `costdeck_scaling_desired_up` | kind, name | 1 while the schedule wants workloads up |
| `costdeck_scaling_ready` | kind, name | 1 when every target reached the desired state |
| `costdeck_scaling_override_active` | kind, name | 1 while a manual override ignores the schedule |
| `costdeck_estimated_hourly_savings` | kind, name, currency | Cost of what is currently kept down |
| `costdeck_cluster_estimated_monthly_cost` | part (nodes, storage, network, requested, used), currency | Cluster run rate |
| `costdeck_budget_spent_ratio` | budget | Spending this month over the budget's limit |
| `costdeck_budget_forecast_ratio` | budget | Forecast for the month over the budget's limit |
| `costdeck_namespace_estimated_monthly_cost` | namespace, currency | Cost of running pods' requests |
| `costdeck_namespace_cpu_usage_cores` | namespace | Observed CPU |
| `costdeck_namespace_memory_usage_bytes` | namespace | Observed working-set memory |

Plus the standard controller-runtime metrics. With the Prometheus Operator:

```yaml
metrics:
  serviceMonitor:
    enabled: true
    labels: { release: kube-prometheus-stack }
```

`metrics.secure: true` serves HTTPS and requires a bearer token authorized for
`GET /metrics`; bind the generated `costdeck-operator-metrics-reader` ClusterRole to
Prometheus' service account.

## One replica or more

One replica is enough for scaling. Schedules are reconciled from their state every
minute, so a restart only delays the next change briefly. While the pod restarts, the
dashboard, API, MCP and the Webex webhook are unavailable.

Run two replicas when teams or tools rely on the dashboard and API:

```bash
helm upgrade costdeck-operator oci://ghcr.io/migalsp/costdeck/charts/costdeck-operator \
  -n costdeck --reset-then-reuse-values --set replicaCount=2
```

- Every replica serves the dashboard, API and MCP. Sessions are signed cookies, so any
  replica accepts them.
- One leader (Lease `fdcd422b.costdeck.io`) runs the controllers, the Webex poller, the
  cost history and the scheduled digest. A standby takes over within about 15 seconds.
- With `replicaCount` above 1 the chart spreads the replicas over nodes and creates a
  PodDisruptionBudget, unless you set `topologySpreadConstraints` or `affinity` yourself.
- The health page lists the replicas and marks the leader; its logs are the leader's.
- Keep `leaderElection.enabled: true`, or every replica would scale the same workloads.

## Chart values

| Value | Default | Notes |
| :--- | :--- | :--- |
| `image.repository` | `ghcr.io/migalsp/costdeck/costdeck-operator` | |
| `image.tag` | chart appVersion | |
| `replicaCount` | `1` | More replicas serve the dashboard; one leader reconciles |
| `leaderElection.enabled` | `true` | Keep on whenever `replicaCount` > 1 |
| `serviceAccount.annotations` | `{}` | IRSA / Workload Identity |
| `service.port` | `8082` | Dashboard, API and MCP |
| `ingress.*` | disabled | See above |
| `metrics.enabled` / `port` / `secure` | `true` / `8080` / `false` | |
| `metrics.serviceMonitor.enabled` | `false` | Needs Prometheus Operator CRDs |
| `resources` | 100m / 128Mi requests, 200m / 256Mi limits | |
| `podSecurityContext`, `securityContext` | restricted (non-root, read-only root FS, no capabilities) | |
| `podDisruptionBudget.enabled` | `false` | Created anyway when `replicaCount` > 1 |
| `topologySpreadConstraints` | `[]` | With `replicaCount` > 1 and none set, replicas spread over nodes |
| `imagePullSecrets`, `podAnnotations`, `podLabels`, `priorityClassName` | empty | |
| `extraArgs`, `extraEnv` | `[]` | e.g. `--zap-log-level=debug` |
| `nodeSelector`, `tolerations`, `affinity` | empty | |

`values.schema.json` rejects malformed values at install time.

## Upgrading

```bash
helm upgrade costdeck-operator oci://ghcr.io/migalsp/costdeck/charts/costdeck-operator \
  --version <new-version> -n costdeck --reset-then-reuse-values
```

`--reset-then-reuse-values` keeps your overrides while picking up new chart defaults.
Plain `--reuse-values` ignores new defaults and can render an incomplete Deployment.

Upgrading from 1.3 or earlier:

- The default image is now on GHCR, and the Ingress is off unless you enable it. Set
  `image.repository` and `ingress.*` explicitly if you relied on the old defaults.
- Leader election and the metrics endpoint are on by default.
- If a release was changed with `kubectl set image`, Helm 4's server-side apply reports a
  field conflict on the image. Add `--force-conflicts` once.
- Cost Deck no longer changes requests: the Optimize button and
  `POST /api/namespaces/{ns}/optimize` are gone. Use the advice on the namespace page or
  `GET /api/namespaces/{ns}/recommendations`. Namespaces optimized by an earlier version
  show **Revert optimization** until reverted.

## Uninstalling

```bash
helm uninstall costdeck-operator -n costdeck
```

The CRDs, and with them your schedules and the `CostDeckConfig`, are kept, so a reinstall
picks up where you left off. To remove everything:

```bash
kubectl delete crd costdeckconfigs.finops.costdeck.io namespacefinops.finops.costdeck.io \
  namespaceoptimizations.finops.costdeck.io scalingconfigs.finops.costdeck.io \
  scalinggroups.finops.costdeck.io
kubectl delete namespace costdeck
```

Workloads keep the replica count they had at that moment; scale up anything that was down
before uninstalling if you need it running.
