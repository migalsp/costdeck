# Cost Deck Installation Guide

## Target Audience

This guide is intended for **DevOps Engineers, Platform Engineers, and Cluster Administrators** who want to deploy the Cost Deck Operator to manage and optimize their Kubernetes environments.

Cost Deck is designed to be as simple to install and maintain as possible, leveraging standard Helm and OCI artifacts.

---

## Prerequisites

Before installing Cost Deck, ensure you have:

1. **Kubernetes Cluster** (v1.22+ recommended).
2. **Helm** (v3.13+ installed locally).
3. **Metrics Server**: Ensure the Kubernetes Metrics Server is installed in your cluster. This is required for **Namespace Optimization** and **Cluster Insights**.

    ```bash
    kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
    ```

4. **Permissions**: You must have `cluster-admin` rights to install the necessary CRDs and ClusterRoles.

---

## Installation Strategy

Cost Deck is distributed as an OCI-compliant Helm chart hosted directly on GitHub Container Registry (GHCR).

### Step 1: Create Namespace

It is highly recommended to install Cost Deck in its own dedicated namespace for better isolation and security.

```bash
kubectl create namespace costdeck
```

### Step 2: Install via Helm

Install the operator directly from the OCI registry. This eliminates the need to manually add `.tgz` repositories to your local Helm cache.

```bash
helm upgrade --install costdeck-operator oci://ghcr.io/migalsp/costdeck-operator/charts/costdeck-operator \
  --version 1.0.0 \
  --namespace costdeck
```

*(Note: Replace `1.0.0` with the latest release tag found on the GitHub Releases page).*

---

## Post-Installation Verification

Once the Helm command finishes, verify that everything is running correctly:

### 1. Check Pod Status

```bash
kubectl get pods -n costdeck -l app.kubernetes.io/name=costdeck-operator
```

*Wait for the status to be `Running` and `READY 1/1`.*

### 2. Verify CRDs

Cost Deck relies on several Custom Resource Definitions. Ensure they are present:

```bash
kubectl get crds | grep costdeck.io
# scalinggroups.costdeck.io
# scalingconfigs.costdeck.io
# namespacefinops.costdeck.io
```

### 3. Check Operator Logs

Ensure there are no permission or API connection errors:

```bash
kubectl logs -n costdeck -l app.kubernetes.io/name=costdeck-operator -c manager
```

---

## Custom Configuration (values.yaml)

Cost Deck ships with sane defaults (100m CPU / 128Mi RAM requests). However, for larger clusters, you should customize the allocation.

### Common Parameters

| Parameter | Description | Default |
| :--- | :--- | :--- |
| `replicaCount` | Number of operator instances | `1` |
| `resources` | Resource requests/limits | `100m/128Mi` |
| `providers.aws.enabled` | Enable AWS RDS discovery | `false` |
| `ingress.enabled` | Expose the dashboard | `false` |

### Provider Configuration (AWS Example)

To enable cloud database scaling, you must configure the cloud provider. We recommend using **IAM Roles for Service Accounts (IRSA)** instead of static keys.

```yaml
# my-values.yaml
serviceAccount:
  annotations:
    eks.amazonaws.com/role-arn: arn:aws:iam::123456789012:role/costdeck-operator-role

providers:
  aws:
    enabled: true
    region: "us-east-1"
```

Apply changes:

```bash
helm upgrade --install costdeck-operator oci://ghcr.io/migalsp/costdeck-operator/charts/costdeck-operator \
  --version 1.0.0 \
  --namespace costdeck \
  -f my-values.yaml
```

---

## Exposing the UI Dashboard

### Option A: Local Port-Forwarding (Recommended)

Quickly access the dashboard without exposing it to the internet:

```bash
kubectl port-forward svc/costdeck-operator 8082:8082 -n costdeck
```

*Open `http://localhost:8082`.*

### Option B: Ingress

For team access, use an Ingress resource. **Important:** Always secure your Ingress with an authentication layer (like OAuth2 Proxy).

```yaml
ingress:
  enabled: true
  className: "nginx"
  hosts:
    - host: costdeck.internal.company.com
      paths:
        - path: /
          pathType: Prefix
```

---

## Upgrading

Upgrading is a single command:

```bash
helm upgrade costdeck-operator oci://ghcr.io/migalsp/costdeck-operator/charts/costdeck-operator \
  --version <new-version> \
  --namespace costdeck
```

## Uninstalling

```bash
helm uninstall costdeck-operator -n costdeck
kubectl delete namespace costdeck
```
