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

Sign in as `costdeck` (installs from before 1.4 keep `costdeck-admin`; set `adminUsername` to rename it). Without an Ingress, use a port-forward:

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
| admin | Also create, edit and delete schedules, change settings, manage users and API tokens |

## Users

There are three ways to sign in, and they can be combined:

- **The built-in admin** (`costdeck`) is break-glass access managed by the chart. It keeps
  working when single sign-on is down or hidden, and its password lives only in the
  `costdeck-operator-admin-credentials` Secret.
- **Local users** are added by an admin under **Settings → Users** for people without single
  sign-on. Each has a role. A password the admin chooses or generates must be changed at the
  first sign-in, and passwords have at least 12 characters. Disabling or deleting a user, or
  resetting their password, ends their sessions; a new role applies within 30 seconds.
  Passwords are stored as bcrypt hashes in the `costdeck-users` Secret.
- **Single sign-on** users get their role from group mappings (below). They need no account
  in Cost Deck.

**Settings → Users** also lists the last sign-in of everyone, single sign-on users included.
Local users change their own password from the menu under their name.

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

## Single sign-on with Keycloak and other OpenID Connect providers

**Settings → Single sign-on → OpenID Connect** signs users in with any OpenID Connect
provider (`spec.auth.oidc`): authorization code flow with PKCE and a nonce, and the ID token
checked against the issuer's keys, audience and expiry.

1. **In Keycloak**, create a confidential OpenID Connect client:
   - standard flow;
   - redirect URI `https://costdeck.example.com/api/auth/oidc/callback`;
   - a **Group Membership** mapper with the claim name `groups`, added to the ID token.
2. **In Cost Deck**, click **Keycloak defaults** (groups claim `groups`, roles claim
   `realm_access.roles`), then fill in:
   - the issuer `https://keycloak.example.com/realms/<realm>`, which must be HTTPS;
   - the client ID and the client secret, stored in the `costdeck-oidc-credentials`
     Secret.
3. **Map groups to roles.** Keycloak sends full paths (`/costdeck-admins`); a mapping
   matches with or without the leading slash. Roles named `viewer`, `operator` or `admin`
   in the roles claim count as they are; client roles are at
   `resource_access.<client>.roles`.
4. **Choose who else gets in.** Turn off auto-provisioning to admit only users with a
   mapped group or role.

For Okta, Auth0, Dex and others, set their issuer and the claims that carry groups and
roles; dotted paths reach nested claims. Extra scopes (such as `groups`) are requested
when a provider sends a claim only for its scope. **Skip TLS verification** is for a
provider whose certificate the operator cannot verify. **Single sign-on only** hides the
password form while Entra ID or OpenID Connect sign-in is on.

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
- **Lookback window**: how many days the namespace usage charts can show (Namespace
  Insights → Cards: 1h, 24h, 7d …) and the history behind right-sizing advice (at most
  14 days). Requests and limits in the charts come from kube-state-metrics
  (`kube_pod_container_resource_requests`/`_limits`); without it the current ones are shown.
- Bearer token, basic auth or a custom CA, as needed.

**Test connection** reports both reachability and whether container metrics actually
exist; "connected but no series" is the most common setup mistake. The connection status
is also shown in `kubectl get costdeckconfig default -n costdeck -o yaml`.

Without VictoriaMetrics, everything falls back to metrics-server: live numbers, charts of
the last hour only, and right-sizing advice based on a single reading.

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
- Otherwise an estimate is used, and every cost figure says which basis it came from: per
  core-hour and GiB-hour $0.033 and $0.00375 on AWS and Azure (what their general-purpose
  instances work out at), and the OpenCost and Kubecost reference rates of $0.031611 and
  $0.004237 on Google Cloud and clusters without a cloud. Without a cloud price list,
  network volumes are estimated at $0.08 a GiB-month, like a general-purpose cloud SSD, and
  load balancers at nothing.

### GPUs

GPUs are priced apart from CPU and memory, so a GPU node does not make every other pod
look expensive:

- **With cloud list prices**, a GPU costs its node's price less the node's CPU and memory
  at the cluster's rates, divided by its GPUs. The CPU and memory rates come from the nodes
  without GPUs.
- **Otherwise** a GPU is estimated by its model, from the `nvidia.com/gpu.product` label
  GPU feature discovery sets: T4 $0.33, L4 $0.61, A10G $0.81, L40S $1.61, A100 $1.81
  (80 GB $2.50), V100 $2.57, H100 $5.13, H200 $6.16, B200 $12.49 an hour. These are AWS
  on-demand prices in us-east-1 (October 2026) less CPU and memory. An unknown model is
  priced like an A10G.
- **A custom GPU rate** (Settings → Prices, `spec.pricing.gpuHour`) applies to every GPU.

A namespace pays for the GPUs its pods request (`nvidia.com/gpu`, `amd.com/gpu`, MIG
devices `nvidia.com/mig-<n>g.<mem>gb` as n sevenths of a GPU, time-sliced or MPS shares as
1/replicas). That cost is part of its compute cost everywhere: overview, budgets, digests,
schedule candidates.

With VictoriaMetrics scraping NVIDIA's [DCGM exporter](https://github.com/NVIDIA/dcgm-exporter)
in Kubernetes mode, Cost Deck reads how busy each namespace's GPUs were over the last day
(`DCGM_FI_DEV_GPU_UTIL`) and the most memory they held over the last week
(`DCGM_FI_DEV_FB_USED`). It accepts the pod namespace as `namespace`, `pod_namespace`, or
`exported_namespace` when a ServiceMonitor does not honour the exporter's labels. It then
flags:

- GPUs that are nearly idle (below 10%);
- GPUs that several workloads could share through MIG or time-slicing (below 50%);
- a cheaper model with enough memory for what the workload holds.

### AI costs

**AI Costs** (`GET /api/finops/ai`) shows what the cluster's applications pay LLM APIs,
per team and model, and what the models it serves itself cost per million tokens. The
token counts of the last seven days come from VictoriaMetrics, extrapolated to a month:

- **LiteLLM** (`callbacks: ["prometheus"]`) reports its spend and tokens per LiteLLM team
  (`litellm_spend_metric`, `litellm_input_tokens_metric`, `litellm_output_tokens_metric`).
- **OpenTelemetry GenAI** metrics from your applications: the `gen_ai.client.token.usage`
  histogram, or the `gen_ai.client.inference.usage.input_tokens` and `output_tokens`
  counters that replace it. They are charged to the team of the namespace
  (`k8s.namespace.name` through a collector, else the scrape's `namespace`) at the model
  prices you set under **Settings → Prices** (`spec.pricing.llmModels`, per million input
  and output tokens; globs such as `claude-sonnet-*` work). Models without a price are
  listed so you can add one.
- **vLLM** (`vllm:prompt_tokens`, `vllm:generation_tokens`): the run rate of the
  namespace serving a model, GPUs included, divided by the tokens it served gives the cost
  per million tokens.

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

A **monthly token budget** (`spec.integrations.ai.monthlyTokenBudget`) caps what the
assistant and AI reports may use in a calendar month (UTC). Usage is counted from what the
provider reports, or estimated from the text when an OpenAI-compatible endpoint does not
report it, and kept in the `costdeck-ai-usage` ConfigMap, so every replica counts against
the same budget. Once the budget is spent, requests are refused (HTTP 429) until the next
month or until it is raised. Settings and AI Costs show the month's usage, priced at the
model's price under Settings → Prices.

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
  any stage start last and stop first. With **Don't wait forever** (Advanced), a stage
  that is not there after the set minutes stops holding back the next one; its
  namespaces are shown as skipped and keep being scaled.
- **Dependencies**: a schedule that starts after another waits until it is fully up, and
  keeps it up while running. An **on-demand** schedule has no hours of its own.
- **Overrides**: Start now and Scale down now ignore the schedule until the next scheduled
  change, for a chosen time, or until you click **Follow schedule**.
- **Suggest from usage** (schedule editor, one namespace picked): the hours the namespace
  was busy over the last week, read from VictoriaMetrics, with an hour of margin on either
  side, become its windows; the editor shows the share of the week and the saving.
- **Workload rules** (on the namespace page): workloads that never scale down, and the
  order workloads start in, in stages that start top to bottom and stop bottom to top;
  workloads in no stage start last and stop first. A workload goes to the stage of its
  most specific pattern (its exact name, then a glob such as `api-*`, then `*`), so `*` in
  stage 1 and an operator in stage 2 stops the operator before what it manages. They apply
  under any schedule.
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
back up. Cost Deck recognises the workloads Argo CD and Flux manage and names them in the
schedule's preview and in its `GitOps` condition. Either let schedules pause the tool
while they keep the workloads down, or tell the tool to ignore what Cost Deck changes.

**Pause it.** Install with `--set gitops.pause=true`, which grants reading and patching
Flux Kustomizations, HelmReleases and Argo CD Applications, and tick *Pause GitOps while
down* on the schedule (`spec.pauseGitOps`). Cost Deck suspends the Flux objects and turns
off Argo CD self-heal before scaling down, and resumes only what it paused once everything
is up again. A Kustomization that also applies elsewhere, and an application generated by
an ApplicationSet, are left running; the condition says which.

**Or ignore the replicas.** Cost Deck writes every change with the field manager
`costdeck`. For Argo CD:

```yaml
spec:
  ignoreDifferences:
    - group: apps
      kind: Deployment
      managedFieldsManagers: [costdeck]
    - group: apps
      kind: StatefulSet
      managedFieldsManagers: [costdeck]
  syncPolicy:
    syncOptions: [RespectIgnoreDifferences=true]
```

For Flux, leave `spec.replicas` out of the manifests in Git, as you would with an HPA.

### Databases and other operator-run services

Scaling the pods of a database an operator runs does not stop it: the operator brings them
back. Hibernate rules (`spec.hibernate`, *Services to stop* in a namespace's workload rules
or a group's Advanced tab) ask the operator instead. They stop the services once the
namespace's workloads are down and start them before the workloads, and leave the
workloads the operator runs for them alone. Install with `--set hibernation.enabled=true`,
which grants reading and patching the resources of the presets:

| Preset | Resources | How they stop |
|---|---|---|
| `cnpg` | CloudNativePG `Cluster` | declarative hibernation, volumes kept |
| `zalando-postgres` | Zalando `postgresql` | `numberOfInstances: 0`, earlier count restored |
| `strimzi` | `KafkaConnect`, `KafkaMirrorMaker2`, `KafkaBridge` | `spec.replicas: 0` |

A Strimzi `Kafka` cluster itself cannot be stopped: Strimzi refuses a cluster without
brokers and controllers. Zalando's `min_instances` setting raises clusters back unless they
carry its ignore-limits annotation.

For another operator, give its `apiVersion`, `kind`, `replicasField` and optionally
`readyField`, and grant access through `hibernation.extraRules`:

```yaml
hibernation:
  extraRules:
    - apiGroups: ["redis.example.com"]
      resources: ["redisclusters"]
```

Only what Cost Deck stopped is started again (it marks it with `costdeck.io/hibernated`).

### Sleep page

With **Show a sleep page while down** (`spec.sleepPage`), once a schedule has brought its
namespaces down, Cost Deck points their Gateway API HTTPRoutes at its own page. Whoever
opens the application then sees that it is asleep, when it starts again, and a link to wake
it, instead of a gateway error. The routes get their backends and filters back once the
namespaces are up again, so nothing of Cost Deck is in the path of the traffic while they
run.

- **Waking.** The link opens the dashboard's `/wake/<namespace>` page (set **Settings →
  Single sign-on → Dashboard address**). Someone with the operator role starts the
  namespace there for 1, 4 or 8 hours, a manual start that ends by itself. The sleep page
  checks every 20 seconds and shows the application once it is back. A person wakes it,
  not every request, so bots and health checks do not keep it up.
- **How the routes are changed.** Each rule's backends and filters are kept in the
  `costdeck.io/sleeping` annotation. Meanwhile the rule points at the `<release>-api`
  Service with a header naming the namespace. A ReferenceGrant `costdeck-sleep-page` in the
  operator namespace opens that Service to the sleeping routes.
- **Permissions.** Install with `--set sleepPage.enabled=true`, which grants read and patch
  on HTTPRoutes cluster-wide and ReferenceGrants in the operator namespace.
- **Ingress controllers.** Ingress-based routing is not covered. ingress-nginx, the usual
  one, was retired in March 2026, and Gateway API is its recommended successor.

### Sleep when unused

**Sleep when unused for** (`spec.idleAfter`, such as `1h`) scales the namespaces down
inside the schedule's hours once nobody has used them for that long. A test environment
that is up from 8 to 18 but nobody touched after lunch stops paying for the afternoon.

- **What counts as use.** Requests in VictoriaMetrics from the gateway or the mesh: Envoy
  Gateway (`envoy_cluster_upstream_rq_total`, by the `httproute/<namespace>/…` cluster),
  Istio (`istio_requests_total` from the callers' side) and ingress-nginx
  (`nginx_ingress_controller_requests`). The namespaces must have been up for the whole
  period. In a group, any of its namespaces having metrics is enough, and their requests
  are added up. Namespaces without such metrics are never treated as unused (condition
  `Idle`, reason `NoTrafficData`).
- **How long it sleeps.** Until the schedule next starts the namespaces, at most a week,
  with mode `Idle` and `status.idleUntil`. A manual start wakes them at once. Combined with
  the sleep page, whoever opens the application can wake it. Once the manual start is
  over, an unused environment goes back to sleep after the same period.
- **What it does not override.** Manual starts and stops, exceptions and the fail-safe
  keep their say, and a group stays up while groups that depend on it need it.

### Realized savings

The saving a schedule shows is what the stopped workloads request. The bill drops only
when Cluster Autoscaler or Karpenter removes the nodes they ran on. When a scale-down
begins, Cost Deck records those nodes and what they cost (`status.scaleDownNodes`). The
schedule's card then shows which nodes went away, and for each node still there what keeps
it:

- a PodDisruptionBudget that allows no disruption;
- `karpenter.sh/do-not-disrupt`, or `cluster-autoscaler.kubernetes.io/safe-to-evict: "false"`;
- pods Cluster Autoscaler will not move by default: local storage (`hostPath`, a
  disk-backed `emptyDir`), pods without a controller, pods of other controllers, and
  kube-system pods without a budget;
- node annotations that opt out (`scale-down-disabled`, `do-not-disrupt`);
- or pods of other namespaces that still need it.

The verdict comes 15 minutes after the scale-down begins, once autoscalers have had time to
act (Cluster Autoscaler waits 10 minutes by default). For this check the operator reads
PodDisruptionBudgets cluster-wide.

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
| `adminUsername` | empty | Built-in admin name; empty keeps the current one, `costdeck` on new installs |
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
| `gitops.pause` | `false` | Lets schedules with `spec.pauseGitOps` pause Flux and Argo CD (cluster-wide read and patch on their objects) |
| `hibernation.enabled` | `false` | Lets hibernate rules stop CloudNativePG and Zalando Postgres clusters and Strimzi Connect, MirrorMaker 2 and Bridge (cluster-wide read and patch on them) |
| `hibernation.extraRules` | `[]` | `apiGroups` and `resources` of other operators' kinds that hibernate rules stop |
| `sleepPage.enabled` | `false` | Lets schedules with `spec.sleepPage` point their HTTPRoutes at the sleep page (read and patch on HTTPRoutes, ReferenceGrants in the release namespace) |

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
