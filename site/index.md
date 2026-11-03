# Cost Deck: see what Kubernetes costs. Stop paying for what nobody uses.

Open source · Apache 2.0 · Runs in your cluster

Cost Deck is an open-source Kubernetes FinOps operator. It prices every namespace, team, GPU and LLM token, scales idle environments and the cloud databases behind them to zero, and right-sizes what is oversized without hurting performance. One Helm chart, no external service.

[Install with Helm](#install-in-one-command) · [See it in action](#one-operator-closes-the-loop) · [GitHub](https://github.com/migalsp/costdeck)

Free and open source · Read-only until you allow changes · Kubernetes 1.26+

![Cost Deck cost overview: the node bill split into used, idle and unrequested capacity, the month to date, savings by schedules and the daily cost by namespace](https://migalsp.github.io/costdeck/assets/overview-1440.webp)

- **One Helm chart:** a single operator, dashboard, API and MCP server
- **Nothing leaves your cluster:** no vendor account, no agent on the nodes
- **Read-only by default:** changes are opt-in, with rollback
- **Signed and audited:** cosign-signed images and chart, OpenSSF Scorecard

## Where Kubernetes money leaks

Most clusters pay for capacity nobody uses, and the bill does not say whose it is.

- **Requests far above use.** Pods reserve CPU and memory for a peak that rarely comes, and every reserved core is paid for in full.
- **Environments that never sleep.** Dev and staging run 168 hours a week for about 50 hours of work.
- **GPUs bought for the peak.** GPUs wait idle most of the day, and an idle GPU costs as much as a busy one.
- **An AI bill nobody owns.** LLM tokens and GPU hours arrive on one invoice, with no team or model attached.
- **Budgets that miss.** Overspend shows up on the monthly bill, when it is too late to stop it.
- **Spikes found too late.** When a cost jumps, finding the namespace or the deploy behind it takes days.
- **No price per team.** Shared platforms and idle capacity are not split, so nobody owns their part of the bill.
- **Cost found after the deploy.** A change's price appears on next month's bill instead of in the pull request that made it.

Cost Deck gives every namespace and every token a price and an owner, and switches idle capacity off by itself.

## One operator closes the loop

See the cost, cut the waste, keep performance safe, and report to the people who pay.

### See what everything costs

Cost per namespace, team and environment, split into what pods use, what they reserve and leave idle, and what nobody requests.

- Month to date with a forecast, and savings ranked by size
- Volumes, load balancers and GPUs charged to their namespace
- List prices, your own rates, or reconciled with the AWS, Azure or Google Cloud bill
- Several clusters side by side

![Namespace Insights: every namespace with its cost, usage, trend and what could be saved](https://migalsp.github.io/costdeck/assets/insights-1440.webp)

### Switch off what nobody uses

Schedules scale non-production to zero outside working hours and bring it back on time, whole environments at once.

- Shared platforms start first, apps wait, cloud databases and VMs stop last: AWS Aurora and EC2, Azure VMs and flexible servers, Google Cloud SQL and Compute Engine
- Holiday calendars, dated exceptions, overrides that end on their own, prewarm before the day starts
- Sleep when unused, and a sleep page with a wake button instead of a gateway error
- Proof the saving reached the bill: which nodes the autoscaler removed, and what kept the rest

![Scaling Schedules: working hours, platforms on demand, holidays and exceptions, with each schedule's state and saving](https://migalsp.github.io/costdeck/assets/scaling-1440.webp)

![Sleep page: what people see on a scaled-down environment, with a button to wake it in Cost Deck](https://migalsp.github.io/costdeck/assets/sleep-page-1000.webp)

### Right-size without risking performance

Advice from two weeks of real use, applied with a guard that rolls back, or sent as a pull request.

- Performance risks first: CPU throttling, OOM kills, crash loops, autoscalers at their maximum
- Advice never cuts a resource under pressure
- Java heaps sized from the JVM's own flags and heap metrics
- Per-team policies bound the advice or forbid applying it

![Right-sizing: performance risks, what each workload could request and the Java heap it needs, applied with a guard or as a pull request](https://migalsp.github.io/costdeck/assets/rightsizing-1440.webp)

### Budgets, chargeback and reports

Owners and limits for every team, in the numbers finance uses.

- Budgets per team, environment or namespace, with forecasts and anomaly alerts
- Showback and chargeback with shared and idle cost, adding up to the bill, as CSV
- Weekly digests and AI-written reports for leadership, engineers and teams
- Cost estimates on pull requests, before anything ships

![Budgets and alerts: monthly limits, forecasts and alerts sent to Slack and Teams](https://migalsp.github.io/costdeck/assets/budgets-1440.webp)

![Cost allocation: a period's cost by team with shared and idle cost, as CSV](https://migalsp.github.io/costdeck/assets/allocation-1440.webp)

### AI and GPU spend

LLM API spend by team and model, next to the cluster it runs in.

- LiteLLM spend or OpenTelemetry GenAI tokens at your model prices
- What a self-hosted vLLM model costs per million tokens, GPUs included
- Idle GPUs, GPUs that could be shared through MIG, cheaper models with enough memory

![AI Costs: what each team pays LLM APIs, by model, and what self-hosted models cost per million tokens](https://migalsp.github.io/costdeck/assets/ai-costs-1440.webp)

### Works with your tools and your agents

- **AI:** MCP server for Claude Code, Claude Desktop, Cursor and other MCP clients; an assistant in the dashboard that asks before it acts
- **Chat:** Slack, Microsoft Teams, email, Webex bot
- **GitOps:** Argo CD and Flux, paused only while an environment sleeps
- **Workloads:** Deployments, StatefulSets, Argo Rollouts, CronJobs, KEDA, CloudNativePG, Zalando Postgres, Strimzi
- **Metrics:** metrics-server, VictoriaMetrics or any Prometheus-compatible API, DCGM
- **Sign-in:** Entra ID, Keycloak, any OpenID Connect provider; viewer, operator and admin roles
- **API:** REST with OpenAPI, Prometheus metrics

## Who it is for

### Platform and DevOps engineers

- Put every non-production environment, and the databases behind it, to sleep on a schedule that respects dependencies, holidays and GitOps
- Dry run and preview before anything stops; overrides that end on their own
- Scoped RBAC, signed images, CRDs and an API you can automate

[Schedules in the installation guide](https://github.com/migalsp/costdeck/blob/main/docs/installation.md#scaling-what-happens-to-workloads)

### Engineering managers and heads of platform

- Cost per team and environment, and what each schedule saved
- Budgets that warn before the month runs over
- Nothing changes in the cluster without an admin's approval

[Budgets and reports](https://github.com/migalsp/costdeck/blob/main/README.md#budgets-and-reports)

### FinOps and finance

- Showback and chargeback that add up to the cloud bill, with shared and idle cost split by your policy
- Anomaly alerts, forecasts and CSV exports
- AI and GPU spend by team, priced and owned

[Cost allocation](https://github.com/migalsp/costdeck/blob/main/docs/installation.md#cost-allocation-showback-and-chargeback)

## Built for how FinOps works now

What FinOps teams ask for, and how Cost Deck answers it.

- **FinOps for AI:** tokens, LLM requests and GPU hours are a growing part of every cloud bill. Cost Deck shows LLM API and GPU spend by team and model today; AI spend in team totals and budgets is next on the roadmap.
- **Beyond dashboards:** a dashboard shows the waste, and someone still has to remove it. Cost Deck acts: schedules, and right-sizing applied with a rollback guard or sent as a pull request.
- **Shift left:** a change is cheapest to fix before it ships. Pull requests show what a manifest change will cost a month, and can fail above a limit.
- **Kubernetes goes elastic:** Kubernetes resizes pods in place and scales single workloads to zero. Cost Deck puts whole environments to sleep: in order, with their dependencies, databases and cloud resources, on a schedule.
- **Agents join FinOps:** engineers ask an assistant before they open a dashboard. Cost Deck's MCP server answers cost questions for Claude, Cursor and other clients, and offers changes only to operators, after confirmation.
- **One pane for all spend:** nodes, volumes, load balancers, GPUs and LLM APIs in one place, reconciled with the bill; a FOCUS 1.4 export is on the roadmap.

## Safe by design

- **No schedule means always on:** nothing scales down unless a schedule says so.
- **What it changes:** replica counts (the original is kept on the workload), CronJob suspension, KEDA pausing; sleep-page routes and GitOps pausing only if you enable them. It never touches nodes.
- **What needs an admin:** applying right-sizing, deleting preview namespaces.
- **Deleting a schedule restores what it stopped**, cloud databases included.
- **Least privilege:** Secrets read only in its own namespace; risky permissions come as separate opt-in roles.
- **Supply chain:** cosign-signed multi-arch images and Helm chart, SLSA provenance, CodeQL, OpenSSF Scorecard, an end-to-end suite on every pull request.

## Roadmap

From seeing the cost to controlling it, in the order users ask for. A plan, not a promise.

- **Now:** right-sizing in place without restarts; node pools to zero with their environments; right-sizing that respects autoscalers; ready for Kubernetes 1.37; AI spend in team totals and budgets; approvals in Slack and Teams; team-scoped access.
- **Next:** right-sizing on autopilot; unblocking node scale-down; GPU sharing advice; network cost; FOCUS export; AI budgets that hold; extension points for other clouds and external costs.
- **Later:** guardrails before deploy; a FinOps agent that proposes changes for approval; spot for non-production; commitment coverage; carbon next to cost; Backstage and GitLab CI.

[Full roadmap](https://github.com/migalsp/costdeck/blob/main/docs/roadmap.md)

## Install in one command

```bash
helm upgrade --install costdeck-operator \
  oci://ghcr.io/migalsp/costdeck/charts/costdeck-operator \
  --namespace costdeck --create-namespace
```

Then:

```bash
kubectl get secret costdeck-operator-admin-credentials -n costdeck \
  -o jsonpath='{.data.password}' | base64 -d; echo
kubectl port-forward -n costdeck svc/costdeck-operator-api 8082:8082
```

Open http://localhost:8082 and sign in as `costdeck`.

**Requirements:** Kubernetes 1.26 or newer, Helm 3.8 or newer, metrics-server. VictoriaMetrics or any Prometheus-compatible API adds history, throttling and better advice.

**Steps:**

1. Install.
2. See cost per namespace, team and model in the first hour.
3. Put dev and staging on working hours.
4. Set budgets with alerts in Slack or Teams.
5. Right-size: advice first, then apply with the guard.

[Installation guide](https://github.com/migalsp/costdeck/blob/main/docs/installation.md) · [Releases](https://github.com/migalsp/costdeck/releases) · [Verify the signature](https://github.com/migalsp/costdeck/blob/main/docs/installation.md#helm-recommended)

## Frequently asked questions

### What is Cost Deck?

An open-source (Apache 2.0) Kubernetes FinOps operator. It shows what every namespace, team, GPU and LLM token costs, scales idle environments to zero, and right-sizes workloads with a rollback guard. It runs in your cluster, installed with one Helm chart.

### How do I see Kubernetes cost per namespace?

Cloud bills stop at the node. Cost Deck combines node prices with what each pod requests and uses, adds volumes and load balancers, and splits every namespace into used, requested but idle, and unrequested cost.

### How do I allocate shared Kubernetes costs to teams?

Label namespaces with their team or cost center. Cost Deck charges shared namespaces such as monitoring to the others, by what each requests or evenly, and shows capacity nobody requests as a line of its own or shares it out, so the rows add up to the bill. Finance gets the same table as CSV.

### How do I scale dev and staging environments to zero at night?

Create a schedule for their namespaces, in any time zone. Outside working hours Cost Deck scales their workloads to zero, suspends CronJobs, pauses KEDA and stops the cloud databases you add, then brings everything back on time, ahead of the day if you like. The bill drops when your autoscaler removes the emptied nodes, and Cost Deck shows what kept the rest.

### Kubernetes 1.37 lets an HPA scale to zero. Do I still need this?

The HPA scales one workload on external or object metrics, and nothing buffers HTTP requests while no pods are ready. Cost Deck puts whole environments to sleep on a schedule, with their dependencies and databases, and shows a sleep page with a wake button meanwhile.

### How do I right-size pods safely?

Cost Deck bases requests on two weeks of real use with headroom, never cuts a resource that is throttled or was OOM-killed, and lets an admin choose: apply with a guard that puts the old requests back if pods fail, or open a pull request.

### How should I size memory for Java pods?

By default the JVM takes a quarter of the container limit as its heap, and native memory comes on top. Cost Deck reads each container's JVM flags, keeps memory advice above heap plus native memory, and recommends a heap from what is left after garbage collection.

### How do I track LLM spend per team?

Point Cost Deck at LiteLLM or at OpenTelemetry GenAI token metrics in VictoriaMetrics; it prices tokens by model, charges them to the team of each namespace, and shows what self-hosted models cost per million tokens.

### Does any data leave my cluster?

No. There is no Cost Deck service outside your cluster. It calls only what you configure: cloud APIs for prices, bills and resources, your AI provider and your notification channels.

### Can AI assistants use it?

Yes. Its MCP server at `/mcp` works with Claude Code, Claude Desktop, Cursor and other MCP clients, with an API token; a viewer token gives read-only tools.

### What does it cost?

Nothing. It is Apache 2.0 with no paid tier; you pay only for the small pod it runs in (100m CPU and 128 MiB requested).

## Find out what your cluster really costs, today.

Install with Helm, sign in, and see cost per namespace and team in the first hour.

[Install with Helm](#install-in-one-command) · [Star on GitHub](https://github.com/migalsp/costdeck)

---

Cost Deck · [Apache 2.0](https://github.com/migalsp/costdeck/blob/main/LICENSE) · [GitHub](https://github.com/migalsp/costdeck) · [Installation guide](https://github.com/migalsp/costdeck/blob/main/docs/installation.md) · [Roadmap](https://github.com/migalsp/costdeck/blob/main/docs/roadmap.md) · [Changelog](https://github.com/migalsp/costdeck/blob/main/CHANGELOG.md) · [Security policy](https://github.com/migalsp/costdeck/blob/main/SECURITY.md) · [Releases](https://github.com/migalsp/costdeck/releases)
