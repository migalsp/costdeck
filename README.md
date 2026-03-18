<p align="center">
  <img src="docs/assets/logo.png" width="200" alt="Cost Deck Logo">
</p>

<h1 align="center">Cost Deck</h1>

<p align="center">
  <strong>Stop wasting money on empty pods. Start optimizing your Kubernetes clusters.</strong>
</p>

<p align="center">
  <a href="https://github.com/migalsp/Cost Deck/actions/workflows/ci.yml">
    <img src="https://github.com/migalsp/Cost Deck/actions/workflows/ci.yml/badge.svg" alt="Build Status">
  </a>
  <a href="https://github.com/migalsp/Cost Deck/releases">
    <img src="https://img.shields.io/github/v/release/migalsp/Cost Deck" alt="Latest Release">
  </a>
  <a href="https://goreportcard.com/report/github.com/migalsp/costdeck-operator">
    <img src="https://goreportcard.com/badge/github.com/migalsp/costdeck-operator" alt="Go Report Card">
  </a>
  <a href="https://opensource.org/licenses/Apache-2.0">
    <img src="https://img.shields.io/badge/License-Apache_2.0-blue.svg" alt="License">
  </a>
  <a href="https://app.codecov.io/gh/migalsp/costdeck-operator">
    <img src="https://img.shields.io/codecov/c/github/migalsp/costdeck-operator" alt="Coverage">
  </a>

<br />

**Cost Deck** is a lightweight Kubernetes Operator that automatically cuts cloud costs and simplifies resource management. It finds over-provisioned workloads, scales them down when not in use, and provides a beautiful real-time UI to manage it all.

![Cost Deck Dashboard](docs/assets/dashboard.png)

## Why use Cost Deck?

Relying on static, guesswork-based CPU and memory limits across hundreds of microservices is a recipe for waste. Developers over-provision "just in case", and cloud bills skyrocket.

**Cost Deck runs autonomously to fix this:**

- **Save Money:** Automatically identify namespaces that request too much CPU/Memory and right-size them with a single click.
- **Night & Weekend Savings:** Shut down Dev and Staging environments automatically outside of working hours using simple CRDs (`ScalingConfig` & `ScalingGroup`).
- **Cloud Database Scaling:** Stop paying for idle AWS RDS/Aurora databases by orchestrating their scaling alongside your Kubernetes workloads.
- **Visual Capacity Planning:** Instantly see which cluster nodes are burning hot (>90%) and which are sitting empty (<50%).
- **Zero Risk:** Revert optimization changes instantly if a workload underperforms.

## Quick Start

Drop Cost Deck into your cluster in under a minute via Helm:

```bash
helm upgrade --install costdeck-operator oci://ghcr.io/migalsp/costdeck-operator/charts/costdeck-operator --version 1.x.x -n costdeck --create-namespace
```

Open the Dashboard:

```bash
kubectl port-forward svc/costdeck-operator 8082:8082 -n costdeck
# Go to http://localhost:8082
```

## Learn More

- [**Installation Guide**](docs/installation.md)
- [**User Guide & Custom Resources**](docs/user-guide.md)

---
If Cost Deck saves you money, please **⭐️ star this repository!**