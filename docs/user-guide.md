# Cost Deck User Guide

Cost Deck is a Kubernetes-native FinOps and Orchestration platform designed to provide full visibility into your cluster costs and automate infrastructure savings without affecting production reliability.

---

## 1. Namespace Insights

The **Namespace Insights** view provides a high-level cost breakdown for every namespace in your cluster. It helps you identify "cost-heavy" projects and automatically detects resource over-provisioning.

### Key Features

- **Cost Allocation**: See exactly which team or project is driving cluster spend. The UI breaks down costs by CPU, Memory, and even estimated Cloud Provider egress.
- **Waste Detection**: Automated identification of services with high CPU/Memory requests but low actual usage. Look for the "Overprovisioned" badges in the list.
- **Right-sizing Recommendations**: Suggested values for your resource limits to optimize performance and cost. These are calculated based on the 95th percentile of actual usage over the last 7 days.

![Namespace Insights](./assets/dashboard.png)
*Use the analytics dashboard to monitor daily spend and resource efficiency across all namespaces.*

---

## 2. Cluster Node Map

The **Cluster Node Map** offers a real-time heat map of your physical infrastructure, organized by node pools or availability zones.

### Why use it

- **Hotspots**: Quickly identify nodes that are running at >90% capacity, which might lead to pod evictions or scheduling delays.
- **Fragmentation**: See if your cluster has many under-utilized nodes that can be consolidated (bin-packing) to trigger cloud provider scaling and save money.
- **Topology Awareness**: Visualize where your critical workloads are physically running across AZs to ensure high availability.

![Cluster Node Map](./assets/nodemap.png)
*The Node Map provides a visual representation of cluster health and capacity utilization.*

---

## 3. Cost Deck Health

Transparency is key. The **Cost Deck Health** dashboard allows you to monitor the internal state of the Cost Deck operator itself.

### What's Monitored

- **Operator Performance**: Real-time CPU/Memory usage of the Cost Deck controllers.
- **Live Logs**: Stream real-time events from the operator to debug scaling sequences or detection logic directly in the UI.
- **Reconciliation Status**: Verify that the operator is successfully communicating with the Kubernetes API and that CRDs are being synchronized without errors.

![Cost Deck Health](./assets/health.png)
*Check the health status and internal logs to ensure continuous cost management.*

---

## 4. Workload Scaling

Cost Deck's core engine allows you to orchestrate workload availability based on schedules and dependencies.

### Core Concepts

#### Sequential Scaling (Stages)

Cost Deck organizes namespaces into **Stages**. This ensures that dependencies are respected during major environmental shifts.

**When a "Scale Down" is triggered:**

1. **Edge/Ingress (Stage 3)** scales down first to stop incoming traffic.
2. **Application (Stage 2)** follows once the ingress layer is dormant.
3. **Data/Core (Stage 1)** scales down last, ensuring data integrity is maintained until the very end.

*Scale Up follows the exact reverse order (Database → App → Ingress).*

#### Dependency Awareness

By grouping namespaces into stages, you prevent "orphan" requests and database connection errors during transition periods. The operator waits for `readyReplicas` to reach target states before proceeding to the next stage.

### Configuration Methods

#### Method A: Configuration via UI

The interactive dashboard provides the best experience for visualizing your infrastructure topology.

1. Navigate to **Workload Scaling** in the sidebar.
2. Click **+ New Group**.
3. Name your group (e.g., `non-prod-staging`).
4. **Define Stages**: Add namespaces to specific stages to set the scaling order. Use the "Add Stage" button to create sequential blocks.

![Scaling Dependencies](./assets/scaling_dependencies.png)
*Configuring sequential stages in the UI to manage namespace dependencies.*

#### Method B: Configuration via API

Perfect for triggering scaling from CI/CD pipelines (e.g., scale up a namespace before running E2E tests or down after a branch is deleted).

```bash
# Example: Triggering a scale-down for the 'staging' group
curl -i -X POST http://costdeck.cluster.internal/api/scaling/groups/staging/manual \
  -H "Authorization: Bearer $CD_TOKEN" \
  -d '{"active": false}'
```

#### Method C: Configuration via CRD (GitOps)

Define your scaling logic as code for maximum reproducibility and version control.

```yaml
apiVersion: costdeck.io/v1alpha1
kind: ScalingGroup
metadata:
  name: non-prod-sequence
spec:
  active: null # null means follow schedule
  schedules:
    - days: [1,2,3,4,5] # Mon-Fri
      startTime: "09:00"
      endTime: "18:00"
      timezone: "UTC"
  namespaces: ["app-db", "app-v1", "app-v2"]
  sequence: ["app-db", "app-v1 app-v2"] # Stage 1: DB, Stage 2: Apps
```

### Monitoring Pipelines

When a scaling action is triggered, you can monitor its progress in real-time. You'll see each stage move from "Pending" to "Active" and finally to "Ready".

![Scaling Pipeline Start](./assets/scaling_pipeline_start.png)
*Starting a scaling operation: Initial stage is triggered.*

![Scaling Pipeline Details](./assets/scaling_pipeline_details.png)
*Detailed view: Monitoring individual workload progress within a stage.*

![Scaling Pipeline Finish](./assets/scaling_pipeline_finish.png)
*Completion: All stages have reached the target state.*

---

## Best Practices

1. **Use Health Checks**: Ensure your applications have robust `readinessProbes` so Cost Deck knows exactly when a stage has completed its transition.
2. **Naming Conventions**: Use descriptive stage names (e.g., "Persistence", "Processing", "Routing") to make the pipeline visualization easier to read.
3. **Graceful Deletion**: Configure `terminationGracePeriodSeconds` for your pods to ensure they handle scale-down events correctly without dropping active connections.
4. **Dry Run First**: When creating a new complex sequence, set `active: true` manually and monitor the behavior before relying on the automated scheduler.
