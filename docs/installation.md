# Cost Deck Installation Guide

## Target Audience
This guide is intended for **DevOps Engineers, Platform Engineers, and Cluster Administrators** who want to deploy the Cost Deck Operator to manage and optimize their Kubernetes environments.

Cost Deck is designed to be as simple to install and maintain as possible, leveraging standard Helm and OCI artifacts.

---

## Prerequisites

Before installing Cost Deck, ensure you have:
1. **Kubernetes Cluster** (v1.22+ recommended).
2. **Helm** (v3.13+ installed locally).
3. **Metrics Server**: Ensure the Kubernetes Metrics Server is installed in your cluster (required for Namespace Optimization and Cluster Insights).
    ```bash
    kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
    ```

---

## Installation Strategy

Cost Deck is distributed as an OCI-compliant Helm chart hosted directly on GitHub Container Registry.

### Step 1: Create Namespace
It is highly recommended to install Cost Deck in its own dedicated namespace.
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

### Step 3: Verify Deployment
Ensure the operator pod is running and healthy:
```bash
kubectl get pods -n costdeck
# NAME                              READY   STATUS    RESTARTS   AGE
# costdeck-operator-5b8cb4b8b6-x4jz2   1/1     Running   0          45s
```

---

## Custom Configuration (values.yaml)

Cost Deck ships with sane defaults, requiring minimal resource requests (100m CPU / 128Mi RAM). However, you can deeply customize the deployment.

To view default values:
```bash
helm show values oci://ghcr.io/migalsp/costdeck-operator/charts/costdeck-operator --version 1.x.x
```

**Common Overrides (`my-values.yaml`):**
```yaml
# deploy/helm/costdeck-operator/values.yaml

replicaCount: 1

image:
  repository: ghcr.io/migalsp/costdeck-operator/costdeck-operator
  pullPolicy: IfNotPresent
  tag: "1.0.0"

resources:
  limits:
    cpu: 500m
    memory: 512Mi
  requests:
    cpu: 250m
    memory: 256Mi

# Enable AWS Provider for 3rd Party Cloud Database Scaling (e.g. Amazon Aurora)
# (If `enabled: false`, the operator will not attempt to discover AWS resources or query the EC2 IMDS metadata service)
providers:
  aws:
    enabled: true
    region: "us-east-1"
    # Option A: Provide explicit IAM credentials
    accessKeyId: "AKIA..."
    secretAccessKey: "..."
    # Option B (Recommended): Leave keys blank and use serviceAccount.annotations for AWS IRSA

# Node Scheduling Configuration
nodeSelector:
  kubernetes.io/os: linux
  # type: infra

tolerations:
  - key: "CriticalAddonsOnly"
    operator: "Exists"

# Ingress Configuration (Expose Dashboard)
ingress:
  enabled: true
  className: "nginx" # or alb, traefik, etc.
  annotations:
    kubernetes.io/ingress.class: nginx
    cert-manager.io/cluster-issuer: "letsencrypt-prod"
  hosts:
    - host: costdeck.your-company.com
      paths:
        - path: /
          pathType: Prefix
  tls:
    - secretName: costdeck-tls-secret
      hosts:
        - costdeck.your-company.com
```

Apply your overrides during installation:
```bash
helm upgrade --install costdeck-operator oci://ghcr.io/migalsp/costdeck-operator/charts/costdeck-operator \
  --version 1.x.x \
  --namespace costdeck \
  -f my-values.yaml
```

---

## Exposing the UI Dashboard

The Cost Deck Operator provides a stunning real-time React dashboard. By default, it is exposed as a `ClusterIP` service to prevent unauthorized external access.

**Option A: Local Port-Forwarding (Recommended for quick view)**
```bash
kubectl port-forward svc/costdeck-operator 8082:8082 -n costdeck
```
*Open `http://localhost:8082` in your browser.*

**Option B: Ingress Configuration (For persistent team access)**
Create an Ingress resource to route traffic to the `costdeck-operator` service on port `8082`. Ensure you secure this route with appropriate authentication (e.g., OAuth2 Proxy or an internal VPN).

---

## Upgrading

When a new version of Cost Deck is released, upgrading is seamless using Helm:
```bash
helm upgrade costdeck-operator oci://ghcr.io/migalsp/costdeck-operator/charts/costdeck-operator \
  --version 1.x.x \
  --namespace costdeck
```

## Uninstalling

To completely remove the operator and all its components (Note: this **will not** delete namespaces, but it **will** delete the scaling and optimization CRDs applied to the cluster):

```bash
helm uninstall costdeck-operator -n costdeck
kubectl delete namespace costdeck
```
