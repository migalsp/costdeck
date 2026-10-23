<p align="center">
  <img src="docs/assets/brand/cost-deck-wordmark-640.png" width="320" alt="Cost Deck">
</p>

<p align="center">
  <strong>Kubernetes FinOps operator: see what every namespace costs, and stop paying for idle infrastructure.</strong>
</p>

<p align="center">
  <a href="https://github.com/migalsp/costdeck/actions/workflows/ci.yml"><img src="https://github.com/migalsp/costdeck/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/migalsp/costdeck/releases"><img src="https://img.shields.io/github/v/release/migalsp/costdeck" alt="Release"></a>
  <a href="https://opensource.org/licenses/Apache-2.0"><img src="https://img.shields.io/badge/License-Apache_2.0-blue.svg" alt="License"></a>
</p>

<br />

Cost Deck runs in your cluster. It shows what the cluster and each namespace cost and
waste, sets budgets that warn you before the month runs over, advises on requests and node
shapes, and scales non-production environments to zero when nobody needs them, then brings
them back on time. There is no external service: one operator in your cluster does it all.

![Cost overview: the node bill split into used, idle and unrequested capacity, the month to date, savings and the daily cost by namespace](docs/assets/screenshots/overview.png)

<table>
  <tr>
    <td width="33%" valign="top"><img src="docs/assets/screenshots/insights.png" alt="Namespace Insights"><br><sub><b>Namespace Insights</b>: cost, usage, trend and what could be saved, per namespace</sub></td>
    <td width="33%" valign="top"><img src="docs/assets/screenshots/budgets.png" alt="Budgets and alerts"><br><sub><b>Budgets &amp; Alerts</b>: monthly limits per team, environment or set of namespaces</sub></td>
    <td width="33%" valign="top"><img src="docs/assets/screenshots/scaling.png" alt="Scaling schedules"><br><sub><b>Scaling Schedules</b>: environments on working hours, platforms on demand</sub></td>
  </tr>
</table>

## Features

### See the cost

- **Cost overview.** The node bill split into what pods use, what they request but leave
  idle, and what nobody requests; the month to date with a forecast; week-over-week
  changes; cost by team and environment; and the savings opportunities, ranked.
- **Namespace Insights.** Every namespace with its cost, usage, efficiency, trend and
  findings, filtered by environment, team, schedule or finding, and exported as CSV.
- **Storage, network and the real bill.** Persistent volumes and load balancers are priced
  and charged to their namespace, and unused volumes and idle load balancers are found.
  Costs can be reconciled with the AWS, Azure or Google Cloud bill, so discounts,
  reservations, savings plans and spot prices show in every figure.
- **Prices you can check.** AWS and Azure list prices per instance type, your own rates,
  or an estimate in line with cloud list prices. Every figure says which basis it uses.

### Act on it

- **Scaling schedules.** Pick namespaces and when they should run: working hours, the work
  week non-stop, or any custom windows, in any time zone. Outside those hours workloads go
  to zero; their replica counts are restored afterwards.
- **Shared platforms on demand.** A schedule can depend on another one. An on-demand
  platform (databases, Kafka, logging) starts only while an environment that needs it is
  up, and environments wait until it is fully up.
- **Schedules suggested from usage.** One click reads a week of a namespace's CPU usage and
  fills in the hours it is busy, with the saving.
- **Overrides that end on their own.** "Start now" or "Scale down now" holds until the next
  scheduled change, or for a time you choose, then the schedule takes over again.
- **Holidays and release weekends.** Dated exceptions and shared holiday calendars keep a
  schedule down (or up) on given days without editing its hours.
- **Namespaces by label.** A schedule can take every namespace with a label, so preview
  environments join as they are created and leave when the label goes.
- **Preview and dry run.** See what a schedule would stop, when, and what it saves before
  saving it, or let it run in a dry run that decides but scales nothing.
- **GitOps-aware.** Workloads managed by Argo CD or Flux are named in the preview, and a
  schedule can pause the tool while it keeps them down and resume it afterwards.
- **Argo Rollouts.** Canary and blue-green Rollouts scale to zero and back like
  Deployments, including those that take their pods from a Deployment (`workloadRef`).
- **Databases with their namespace.** CloudNativePG and Zalando Postgres clusters, Strimzi
  Kafka Connect, MirrorMaker 2 and Bridge, or any operator's resource with a replica field,
  stop after the namespace's workloads and start before them.
- **GPUs.** GPUs are priced apart from CPU and memory (their share of the node's list price,
  or an estimate per model) and charged to the namespaces that request them. With NVIDIA's
  DCGM exporter in VictoriaMetrics, Cost Deck finds idle GPUs, GPUs workloads could share
  through MIG or time-slicing, and cheaper models with enough memory.
- **AI costs.** What applications pay LLM APIs by team and model (LiteLLM spend, or
  OpenTelemetry GenAI token counts at your model prices), and what the models you serve
  with vLLM cost per million tokens.
- **A sleep page while down.** A sleeping environment's Gateway API routes show a page that
  says it is asleep and links to waking it for an hour or two, instead of a gateway error.
- **Sleep when unused.** An environment nobody has sent a request to for an hour (Envoy
  Gateway, Istio or ingress-nginx metrics), or whose GPUs sat idle (DCGM), goes down
  inside its schedule's hours too.
- **Preview cleanup.** Namespaces of preview environments are deleted some time after
  they were created, with a warning first and a way to keep one longer. Off unless you
  turn it on, twice.
- **Savings that reach the bill.** After a scale-down, each schedule shows which nodes the
  autoscaler removed and what keeps the others: a PodDisruptionBudget, `do-not-disrupt`,
  `safe-to-evict: "false"`, local storage, or other namespaces still using them.
- **Cloud resources too.** Namespaces start stage by stage and stop in reverse, and the
  stages can include AWS Aurora and EC2, Azure VMs and PostgreSQL/MySQL flexible servers,
  and Google Cloud Compute Engine and Cloud SQL. CronJobs are suspended and KEDA
  ScaledObjects paused while a namespace is down.
- **Right-sizing and node advice.** Read-only advice on which requests can shrink (p95
  usage when VictoriaMetrics is connected), and per node pool the instance type and count
  that fit the requests, arm64 alternatives and spot capacity for non-production
  workloads. Cost Deck never edits your requests or nodes.
- **Cost in pull requests.** A GitHub Action (or the image in any CI) comments on a pull
  request with what its manifests change will cost a month, at your cluster's rates and
  schedules, and can fail the check above a limit.
- **Several clusters.** One installation shows the others side by side, read with a viewer
  API token each: cost, savings, this month so far and totals.
- **Showback and chargeback.** A period's cost by team, environment, namespace or any label
  such as `cost-center`, with shared namespaces and idle capacity charged by your policy,
  adding up to the bill, as a table and a CSV for finance.
- **Budgets and alerts.** Monthly budgets per cluster, team, environment or any set of
  namespaces, alerting at thresholds and when the month is heading over; daily cost
  anomaly alerts; and a weekly or monthly cost digest, sent to Webex, Slack, Microsoft
  Teams or email.

### Fits your team

- **Access.** Local users with viewer, operator and admin roles, single sign-on with
  Microsoft Entra ID, Keycloak or any OpenID Connect provider with group-to-role mapping,
  API tokens, and least-privilege RBAC: Secrets are
  only read in the operator's own namespace.
- **Where you already work.** Notifications in Slack, Microsoft Teams, email and Webex, a
  Webex bot, an AI assistant (Claude, OpenAI-compatible or
  Gemini) that proposes actions for you to confirm, an MCP server for AI clients, and
  Prometheus metrics.

## Quick start

```bash
helm upgrade --install costdeck-operator \
  oci://ghcr.io/migalsp/costdeck/charts/costdeck-operator \
  --version <version> --namespace costdeck --create-namespace

kubectl get secret costdeck-operator-admin-credentials -n costdeck \
  -o jsonpath='{.data.password}' | base64 -d; echo
kubectl port-forward -n costdeck svc/costdeck-operator-api 8082:8082
```

Open http://localhost:8082 and sign in as `costdeck`. Pick the latest version from
the [releases](https://github.com/migalsp/costdeck/releases). The
[installation guide](docs/installation.md) covers Ingress, SSO, cloud accounts, monitoring
and every chart value.

## Documentation

- **In the dashboard**: the **Documentation** page has step-by-step guides (schedules,
  cloud resources, cost and right-sizing, access, AI, Webex, troubleshooting) and a REST API
  reference generated from the live OpenAPI document.
- **[Installation guide](docs/installation.md)**: chart values, Ingress, single sign-on,
  cloud permissions, monitoring, upgrades.
- **API**: Swagger UI at `/api/docs`, the specification at `/api/openapi.yaml` and
  `/api/openapi.json`. Every operation names the role it needs.

## How it fits together

```mermaid
graph TD
    subgraph Clients
        UI[Dashboard]
        AI[AI clients via MCP]
        WX[Webex space]
        PR[Prometheus]
    end

    subgraph Operator["Cost Deck operator (one binary)"]
        API[REST API, sign-in, MCP]
        CTRL[Controllers: schedules, dependencies, discovery, usage]
        FIN[FinOps: cost ledger, budgets, digests, bill reconciliation]
        BOT[Webex bot]
        MET[metrics endpoint]
    end

    subgraph Cluster["Kubernetes"]
        CRD[CRDs: ScalingGroup, ScalingConfig, NamespaceFinOps, CostDeckConfig]
        WL[Deployments, StatefulSets, Argo Rollouts, CronJobs, KEDA]
        INF[Nodes, volumes, load balancers]
        MS[metrics-server]
    end

    subgraph External
        VM[VictoriaMetrics]
        CLOUD[AWS, Azure, Google Cloud: resources, prices, bill]
        LLM[AI provider]
        ENTRA[Microsoft Entra ID]
    end

    UI --> API
    AI --> API
    WX <--> BOT
    FIN --> WX
    PR --> MET
    API --> CRD
    CTRL --> CRD
    CTRL --> WL
    CTRL --> MS
    CTRL --> VM
    CTRL --> CLOUD
    FIN --> INF
    FIN --> CLOUD
    API --> LLM
    API --> ENTRA
```

## Development

```bash
make test        # unit and envtest tests
make lint        # golangci-lint
make ui-lint     # TypeScript and ESLint
make ui-test     # dashboard unit and component tests (Vitest)
make ui build    # dashboard + manager binary with the dashboard embedded
make test-e2e    # Kind cluster + Helm install + the regression scenarios in hack/e2e
```

The end-to-end suite runs in CI on every pull request. Every scenario in `hack/e2e`
covers behaviour users depend on (scheduling, dependencies, stage timeouts, workload
rules, holiday exceptions, namespace selectors, preview and dry run, GitOps pausing, operator-run services, realized savings, auth, API, VictoriaMetrics history, MCP), so a change that breaks one fails the
build. A feature or fix that changes behaviour adds or extends a scenario. To run it
against an existing test cluster instead of a new Kind cluster:

```bash
SKIP_INSTALL=1 ./hack/e2e-smoke.sh                 # skips the settings-changing scenarios
SKIP_INSTALL=1 DISPOSABLE=1 E2E_ONLY='30|40' ./hack/e2e-smoke.sh
```

## License

[Apache 2.0](LICENSE)
