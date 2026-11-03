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

## Where Kubernetes money leaks in 2026

Cloud waste rose for the first time in five years, to 29% of spend (Flexera State of the Cloud 2026). Waste reduction is the top priority of FinOps teams today, and AI is the next one (State of FinOps 2026).

- **Requests far above use.** CPU use averages 8% and memory 20% of what Kubernetes clusters reserve, and both fell in a year. Source: CAST AI, 2026 State of Kubernetes Optimization.
- **Environments that never sleep.** Dev and staging run 168 hours a week for about 50 hours of work. Source: Arithmetic of a working week.
- **GPUs bought for the peak.** GPUs in Kubernetes average 5% use, and 79% of companies say theirs are busy half the time or less. Source: CAST AI, 2026; VentureBeat, October 2026.
- **An AI bill nobody owns.** 98% of FinOps teams now manage AI spend, yet only 26% of large companies see what their AI costs in real time. Source: State of FinOps 2026; KPMG AI Pulse, Q2 2026.
- **Budgets that miss.** 46.9% of enterprises report AI spend over budget. Source: Futurum, September 2026.
- **Spikes found too late.** For one company in three, finding why a cost spiked takes a week or more. Source: Harness, State of AI in FinOps 2026.
- **No price per team.** Shared platforms and idle capacity are not split, so nobody owns their part; FinOps teams list shared cost and container allocation among the tools they want most. Source: State of FinOps 2026.
- **Cost found after the deploy.** Pricing a change before it ships is the second most wanted FinOps capability. Source: State of FinOps 2026.

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

## Built for FinOps in 2026

What changed this year, and how Cost Deck answers it.

- **FinOps for AI:** monitoring tokens, LLM requests and GPU use is the capability FinOps teams want most (State of FinOps 2026). Cost Deck shows LLM API and GPU spend by team and model today; AI spend in team totals and budgets is next on the roadmap.
- **Beyond dashboards:** "Dashboards are table stakes of yesterday," say practitioners, who ask for automated remediation they can trust (State of FinOps 2026). Cost Deck acts: schedules, and right-sizing applied with a rollback guard or sent as a pull request.
- **Shift left:** pre-deployment costing is the second most wanted capability. Pull requests show what a manifest change will cost a month, and can fail above a limit.
- **Kubernetes goes elastic:** Kubernetes 1.36 resizes pod-level resources in place (beta), and 1.37 lets an HPA scale one workload to zero on external metrics (beta). Cost Deck puts whole environments to sleep: in order, with their dependencies, databases and cloud resources, on a schedule.
- **Agents join FinOps:** MCP servers and agents arrived across FinOps tools in 2026. Cost Deck's MCP server answers cost questions for Claude, Cursor and other clients, and offers changes only to operators, after confirmation.
- **One pane for all spend:** nodes, volumes, load balancers, GPUs and LLM APIs in one place, reconciled with the bill; a FOCUS 1.4 export is on the roadmap.

## Safe by design

- **No schedule means always on:** nothing scales down unless a schedule says so.
- **What it changes:** replica counts (the original is kept on the workload), CronJob suspension, KEDA pausing; sleep-page routes and GitOps pausing only if you enable them. It never touches nodes.
- **What needs an admin:** applying right-sizing, deleting preview namespaces.
- **Deleting a schedule restores what it stopped**, cloud databases included.
- **Least privilege:** Secrets read only in its own namespace; risky permissions come as separate opt-in roles.
- **Supply chain:** cosign-signed multi-arch images and Helm chart, SLSA provenance, CodeQL, OpenSSF Scorecard, an end-to-end suite on every pull request.

## How it compares

Honest notes, checked against each tool's documentation in November 2026.

- **OpenCost and Kubecost** measure and allocate cost; OpenCost is the CNCF project for it. Cost Deck allocates too, and acts: schedules, guarded right-sizing and performance risks. If allocation is all you need, OpenCost is the more mature choice.
- **kube-green and kube-downscaler** scale workloads down on a schedule. Cost Deck adds dependencies and start order, cloud databases and VMs, holidays, a sleep page, and what each schedule saved.
- **HPA scale to zero (Kubernetes 1.37) and KEDA** scale one workload on demand. Cost Deck schedules whole environments and the resources behind them.
- **Karpenter and Cluster Autoscaler** remove empty nodes, and work with Cost Deck: it empties the nodes, they remove them, Cost Deck shows what kept the rest.
- **Goldilocks and the VPA recommender** suggest requests. Cost Deck adds the money, Java heap sizing, performance risks, and applying with a guard or a pull request.
- **Commercial platforms (CAST AI, Spot Ocean, ScaleOps)** automate nodes and pods as a service. Cost Deck runs in your cluster, is free, and changes nothing until you allow it.
- **Not the right tool** for node autoscaling, managing reserved instances and savings plans, or costs outside Kubernetes and the resources its schedules stop.

## Roadmap

From seeing the cost to controlling it, in the order users ask for. A plan, not a promise.

- **Now (Q4 2026 to Q1 2027):** right-sizing in place without restarts; node pools to zero with their environments; right-sizing that respects autoscalers; ready for Kubernetes 1.37; AI spend in team totals and budgets; approvals in Slack and Teams; team-scoped access.
- **Next (H1 2027):** right-sizing on autopilot; unblocking node scale-down; GPU sharing advice; network cost; FOCUS export; AI budgets that hold; extension points for other clouds and external costs.
- **Later (H2 2027):** guardrails before deploy; a FinOps agent that proposes changes for approval; spot for non-production; commitment coverage; carbon next to cost; Backstage and GitLab CI.

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

### Is it an open-source Kubecost alternative?

It covers cost allocation like OpenCost and Kubecost, and adds schedules to zero, right-sizing with a guard, budgets and performance risks in one operator. If allocation is all you need, OpenCost is the more mature choice.

### What does it cost?

Nothing. It is Apache 2.0 with no paid tier; you pay only for the small pod it runs in (100m CPU and 128 MiB requested).

## Find out what your cluster really costs, today.

Install with Helm, sign in, and see cost per namespace and team in the first hour.

[Install with Helm](#install-in-one-command) · [View on GitHub](https://github.com/migalsp/costdeck)

---

Cost Deck · [Apache 2.0](https://github.com/migalsp/costdeck/blob/main/LICENSE) · [GitHub](https://github.com/migalsp/costdeck) · [Installation guide](https://github.com/migalsp/costdeck/blob/main/docs/installation.md) · [Roadmap](https://github.com/migalsp/costdeck/blob/main/docs/roadmap.md) · [Changelog](https://github.com/migalsp/costdeck/blob/main/CHANGELOG.md) · [Security policy](https://github.com/migalsp/costdeck/blob/main/SECURITY.md) · [Releases](https://github.com/migalsp/costdeck/releases)

No cookies. No tracking. Screenshots from a demo cluster. Last updated: 2026-11-03.
