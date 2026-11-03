# Roadmap 2026-2027

Cost Deck already shows what a cluster costs, switches off what nobody uses and right-sizes
what is oversized. The next step is control: every namespace and every token with a price,
an owner and a limit that holds, and automation people trust because they can see it and
undo it.

The order comes from what users ask for: the most wanted capabilities in the FinOps
Foundation's State of FinOps 2026, the most upvoted issues of the open-source tools around
Cost Deck, and what Kubernetes itself shipped in 2026. It is a plan, not a promise. To push
an item up or propose one, open an [issue](https://github.com/migalsp/costdeck/issues).
Every item ships with its end-to-end scenario, like everything else in Cost Deck.

## Recently shipped

- **Right-sizing applied safely**: operators apply advice with a guard that puts the old
  requests back if new pods fail, or propose it as a pull request in GitHub, GitLab or
  Gitea for workloads a GitOps tool manages.
- **Java heap advice**: the heap a JVM takes under its container limit, worked out from its
  flags, and a recommended heap from what is left after garbage collection.
- **Performance risks**: CPU throttling, OOM kills, crash loops, evictions, memory near the
  limit, autoscalers at their maximum and full JVM heaps, with alerts on new critical ones.
- **Right-sizing policies**: a `RightsizingPolicy` per team bounds the advice, forbids
  applying it or leaves workloads out.
- **Prewarm**: environments start ahead of their schedule by a fixed lead or by how long
  they took to wake up.

## Now: Q4 2026 to Q1 2027

| Item | What it does | Why |
|---|---|---|
| Right-size in place | Apply new requests with in-place pod resize (stable since Kubernetes 1.35, pod-level resources in beta since 1.36), falling back to a rollout where a resize does not fit; the guard stays | Restarts are the main objection to applying advice, and in-place resize is now what users expect |
| Node pools to zero | When every namespace on a node pool sleeps, scale the pool itself down with them: EKS, AKS and GKE node pools and Karpenter NodePools, as opt-in schedule targets | Savings reach the bill only when nodes go; node hibernation is the most upvoted request around Karpenter |
| Right-sizing that respects autoscalers | Advice that accounts for HPA targets, and HPA targets, minimums and maximums tuned with the requests they depend on | Lowering the requests of a CPU-scaled workload makes its HPA add pods |
| Ready for Kubernetes 1.37 | Workloads an HPA scales to zero are recognised, neither fought nor counted twice; idle detection reads more Gateway API controllers now that ingress-nginx is retired | Kubernetes 1.37 turned on HPA scale to zero, and ingress-nginx reached end of life in March 2026 |
| AI in the totals | AI spend in the cost history, team totals, chargeback and budgets, with alerts on token spikes | FinOps for AI is the top priority FinOps teams name for the future, and token monitoring the capability they want most |
| Approvals in Slack and Teams | Approve a wake-up, an override or a right-sizing from the message itself; findings as Jira issues | People act where they already work, and it is the base for an agent |
| Team-scoped access | Team leads see and act on their own namespaces, budgets and AI spend only | Shared clusters need it before teams get access |

## Next: H1 2027

| Item | What it does | Why |
|---|---|---|
| Right-sizing on autopilot | A policy mode that keeps requests tuned on its own: changes only above a threshold, small steps until there is enough history, applied at the next restart or right away within PodDisruptionBudgets | From advice someone applies to requests that stay right |
| Reactions to memory and startup trouble | More memory after an OOM kill and more headroom while startup probes fail, decaying back over a day; automation paused for a workload that keeps failing | Automation earns trust by backing off by itself |
| Unblock node scale-down | For every node the autoscaler could not remove, the PodDisruptionBudget, annotation or local volume that kept it, with the fix as a pull request | Cost Deck already names what keeps a node; the next step is removing it |
| GPU sharing advice | Which GPU workloads can share a GPU through MIG, time-slicing or DRA, with the profile and the saving | Kubernetes GPUs average 5% use |
| Network cost | NAT, cross-zone and egress traffic charged to the namespaces that cause it | The cost Cost Deck does not count today |
| FOCUS export | Allocation and chargeback in FOCUS 1.4, with the allocation columns finance tools read; tokens once FOCUS 1.5 defines them | Finance tools read FOCUS, and no open-source Kubernetes tool exports it yet |
| AI budgets that hold | Cost Deck budgets set LiteLLM team limits, so a runaway agent stops at its limit | A budget that stops spending, not only warns |
| Cheaper models and GPUs | The model that fits a team's token mix, and the break-even between self-hosting and an API | From seeing AI spend to lowering it |
| Cost per request and tenant | Unit costs from the gateway metrics Cost Deck already reads for sleep when unused | Unit economics for product teams |
| Extension points | External costs as FOCUS-shaped rows, compatible with OpenCost's plugin format, and a gRPC contract for schedule targets beyond the built-in clouds | Other clouds, on-premises platforms and SaaS costs without growing the core |
| More prices and one fleet view | Google Cloud list prices, more clouds and custom price lists; budgets and allocation across clusters in one place | Fleets keep growing, and every price source counts |

## Later: H2 2027

| Item | What it does | Why |
|---|---|---|
| Guardrails before deploy | Admission warnings for oversized requests, missing owners and teams over budget | Cost before the deploy is one of the most wanted FinOps capabilities |
| FinOps agent with approvals | Weekly proposals as pull requests and cards; low-risk actions within rules you set | The assistant and the MCP server already read everything; acting needs approval |
| Spot for non-production | Eligible non-production workloads moved to spot capacity with an on-demand fallback, as a pull request | Spot cuts compute by more than half where an interruption only costs a restart |
| Commitment coverage | Savings plan, reservation and spot coverage read from the bill, next to the namespaces that use them | Rate optimization context next to usage optimization |
| Startup boost | Extra CPU while a pod starts (JVM warm-up), taken back in place once it is ready | Java services start fast without paying for peak CPU all day |
| Volumes | Oversized volumes and storage performance tiers found and priced, with warnings before a volume fills | Storage is the cost that only grows |
| Carbon next to cost | Energy and CO₂ per namespace (Kepler and grid intensity), and the CO₂ schedules save | GreenOps reporting where it is required |
| More metrics sources | Datadog and other backends next to VictoriaMetrics and Prometheus-compatible APIs | Advice where the history already lives |
| Platform portals | A Backstage plugin with cost per component, and cost estimates in GitLab CI | Cost where developers already look |

## Not planned

Cost Deck works with the tools that own these, and does not rebuild them:

- **Node autoscaling and bin packing**: Karpenter and Cluster Autoscaler remove the nodes
  Cost Deck empties, and Cost Deck shows what kept the rest.
- **Buying reservations and savings plans**: commitment management belongs to the cloud
  accounts; Cost Deck shows their coverage.
- **Container live migration and database proxies**: outside a FinOps operator.
- **A general plugin runtime**: the extension points above stay narrow and stable instead.
