// The in-app user guide. Text may use `code` and **bold**; everything else is structure.
// Keep it task-oriented: what the user wants to do, then how, in the dashboard first.

export type Block =
  | { p: string }
  | { steps: string[] }
  | { list: string[] }
  | { tip: string }
  | { warn: string }
  | { code: string; label?: string }
  | { table: { head: string[]; rows: string[][] } }
  // Ways to do the same thing; rendered as tabs.
  | { ways: { dashboard?: string; api?: string; kubectl?: string } }

// proseOf is the readable text of a block, without code, for search snippets.
export function proseOf(block: Block): string {
  return 'code' in block || 'ways' in block ? '' : textOf(block)
}

// textOf flattens a block for search.
export function textOf(block: Block): string {
  if ('p' in block) return block.p
  if ('steps' in block) return block.steps.join(' ')
  if ('list' in block) return block.list.join(' ')
  if ('tip' in block) return block.tip
  if ('warn' in block) return block.warn
  if ('code' in block) return block.code
  if ('table' in block) return [...block.table.head, ...block.table.rows.flat()].join(' ')
  return Object.values(block.ways).join(' ')
}

export interface Topic {
  id: string
  title: string
  blocks: Block[]
}

export interface Guide {
  id: string
  title: string
  icon: 'start' | 'schedule' | 'cloud' | 'cost' | 'nodes' | 'storage' | 'budgets' | 'reports' | 'access' | 'ai' | 'mcp' | 'webex' | 'health' | 'help'
  summary: string
  topics: Topic[]
}

// Shared by every API example, so readers set them once.
export const apiSetup = `# Your Cost Deck URL, or http://localhost:8082 with a port-forward
export COSTDECK=https://costdeck.example.com
# An API token from Settings → API tokens
export TOKEN=cdk_...`

export const guides: Guide[] = [
  {
    id: 'start',
    title: 'Getting started',
    icon: 'start',
    summary: 'Install Cost Deck, sign in and put your first environment on a schedule.',
    topics: [
      {
        id: 'what',
        title: 'What Cost Deck does',
        blocks: [
          { p: 'Cost Deck runs inside your Kubernetes cluster. It does four things:' },
          {
            list: [
              '**Shows what each namespace costs** and how much of what it reserves it actually uses.',
              '**Scales non-production environments to zero** when nobody needs them, and brings them back on time. Cloud databases and VMs can stop and start with them.',
              '**Points out requests that are too large**, with the saving and the YAML to fix them. It never changes your workloads\' requests itself.',
              '**Works where your team works**: this dashboard, the REST API, a Webex bot, an AI assistant and MCP clients such as Claude Code.',
            ],
          },
        ],
      },
      {
        id: 'install',
        title: 'Install',
        blocks: [
          { p: 'You need Kubernetes 1.26 or newer, Helm 3.8+ and metrics-server. Pick the latest version from the GitHub releases page.' },
          {
            code: `helm upgrade --install costdeck-operator \\
  oci://ghcr.io/migalsp/costdeck/charts/costdeck-operator \\
  --version <version> --namespace costdeck --create-namespace`,
          },
          { tip: 'No Helm? Every release also ships an `install.yaml` for `kubectl apply -f`. The installation guide in the repository (`docs/installation.md`) covers Ingress, SSO, cloud accounts and every chart value.' },
        ],
      },
      {
        id: 'sign-in',
        title: 'Sign in',
        blocks: [
          { p: 'Cost Deck creates an admin account on first start. Read its password, open a port-forward and sign in at http://localhost:8082 as `costdeck-admin`.' },
          {
            code: `kubectl get secret costdeck-operator-admin-credentials -n costdeck \\
  -o jsonpath='{.data.password}' | base64 -d; echo
kubectl port-forward -n costdeck svc/costdeck-operator-api 8082:8082`,
          },
          { tip: 'To give your team a real URL, enable the chart\'s Ingress. To let people sign in with their work account, connect Microsoft Entra ID (see Access and sign-in).' },
        ],
      },
      {
        id: 'first-schedule',
        title: 'Your first schedule',
        blocks: [
          {
            steps: [
              'Open **Scaling Schedules** and click **New schedule**.',
              'Tick the namespaces of one environment, for example `dev-frontend` and `dev-backend`.',
              'Choose when they should run: **Working hours** is a good start. Check the time zone.',
              'Click **Create schedule**.',
            ],
          },
          { p: 'That is all. Outside those hours the workloads scale to zero; at the start of the next window they come back with the replica counts they had. The card always says what happens next ("Down · starts Mon 08:00") and how much the schedule saves.' },
        ],
      },
      {
        id: 'tour',
        title: 'Find your way around',
        blocks: [
          {
            table: {
              head: ['Page', 'What it is for'],
              rows: [
                ['Cost Overview', 'The start page: what the cluster costs, how much of it is used, what the schedules save, and what to do next.'],
                ['Namespace Insights', 'Every namespace with its cost, efficiency, schedule and savings: filter, group, export. Open one to see its pods and what can be reduced.'],
                ['Cluster Node Map', 'What every node costs, how well pods fill it, cheaper node shapes per pool, spot, and nodes that could be emptied.'],
                ['Storage & Network', 'Persistent volumes and load balancers with their cost, and the ones nothing uses.'],
                ['Budgets & Alerts', 'Monthly budgets for teams, namespaces and environments, and alerts when they are reached or spending jumps.'],
                ['Cost Deck Health', 'The operator itself: resource use, connections and logs.'],
                ['Scaling Schedules', 'Create schedules, start or stop environments now, and follow progress.'],
                ['Reports', 'The weekly or monthly cost digest (also posted to Webex) and AI-written reports for leadership, engineers and teams.'],
                ['Settings', 'Cloud accounts, monitoring, AI, Webex, MCP, sign-in and prices. Admins only.'],
              ],
            },
          },
        ],
      },
    ],
  },

  {
    id: 'schedules',
    title: 'Schedules',
    icon: 'schedule',
    summary: 'Scale environments to zero when nobody needs them and bring them back on time.',
    topics: [
      {
        id: 'how',
        title: 'What a schedule does',
        blocks: [
          { p: 'A schedule covers one or more namespaces and, optionally, cloud resources. Outside its hours Cost Deck scales every Deployment and StatefulSet in those namespaces to zero. When the next window starts it restores the replica count each one had.' },
          {
            list: [
              'CronJobs are suspended and KEDA ScaledObjects paused while a namespace is down, and restored afterwards.',
              'An HPA stops acting on a workload at zero replicas and takes over again once it is back.',
              'The count to restore is kept on the workload itself, in the `costdeck.io/original-replicas` annotation. A workload with no recorded count comes back with one replica.',
              'A namespace belongs to one schedule at a time. If two claim it, the older one wins and the newer one shows the conflict.',
            ],
          },
        ],
      },
      {
        id: 'when',
        title: 'Choose when it runs',
        blocks: [
          {
            table: {
              head: ['Choice', 'Runs'],
              rows: [
                ['Working hours', 'During the hours you pick on the days you pick, for example 08:00–19:00 Monday to Friday.'],
                ['Work week', 'Non-stop from Monday morning to Friday evening, so nothing restarts overnight.'],
                ['On demand', 'Never on its own: only while another schedule that depends on it is up. For shared platforms.'],
                ['Custom', 'Any number of windows: overnight shifts, weekends, several blocks a day.'],
              ],
            },
          },
          { p: 'Every schedule has a time zone, so "08:00" means 08:00 where your team is, including daylight-saving changes. A window whose end is earlier than its start runs overnight into the next morning.' },
          {
            ways: {
              kubectl: `schedules:
  # Daily window on the listed weekdays (0 = Sunday)
  - days: [1, 2, 3, 4, 5]
    startTime: "08:00"
    endTime: "19:00"
    timezone: Europe/Berlin

  # Overnight: ends the next morning
  - days: [1, 2, 3, 4, 5]
    startTime: "22:00"
    endTime: "06:00"
    timezone: UTC

  # Continuous: Monday 00:00 straight through Friday 23:59
  - startDay: 1
    startTime: "00:00"
    endDay: 5
    endTime: "23:59"
    timezone: UTC`,
            },
          },
          { tip: 'Changes take effect within about a minute. A schedule with no usable window keeps everything up: a broken schedule never scales anything to zero.' },
        ],
      },
      {
        id: 'status',
        title: 'Read a schedule card',
        blocks: [
          {
            table: {
              head: ['The card says', 'Meaning'],
              rows: [
                ['Up · scales down 19:00', 'Following its hours; the next change is shown.'],
                ['Down · starts Mon 08:00', 'Scaled to zero until the next window.'],
                ['Starting… / Scaling down…', 'Working through the stages. Click the progress bar to see where it is.'],
                ['Waiting for platform to start', 'It starts after another schedule, which is not fully up yet.'],
                ['Kept up manually / Kept down manually', 'Someone clicked Start now or Scale down now. It says until when.'],
                ['Up · needed by dev', 'On demand or otherwise down, but another schedule depends on it.'],
                ['Up · no schedule set', 'No usable hours, so it stays up to be safe.'],
                ['Applying changes…', 'You just saved; Cost Deck has not processed the change yet.'],
              ],
            },
          },
        ],
      },
      {
        id: 'override',
        title: 'Start now or scale down now',
        blocks: [
          { p: 'Need staging on a Saturday? Click **Start now** on its card. Want to free the cluster early? Click **Scale down now**. Then choose how long:' },
          {
            list: [
              '**Until the next scheduled change**: the schedule takes over again by itself. The usual choice.',
              '**For 1, 4, 8 or 24 hours**.',
              '**Until I hand it back**: the schedule stays ignored until someone clicks **Follow schedule**.',
            ],
          },
          { p: 'While an override is active the card shows it and offers **Follow schedule**, which ends it straight away. People with the operator role can do this; creating and editing schedules needs admin.' },
          {
            ways: {
              api: `# Start now, until the next scheduled change
curl -X POST "$COSTDECK/api/scaling/groups/dev/manual" \\
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \\
  -d '{"active": true, "until": "nextTransition"}'

# Scale down for 4 hours
curl -X POST "$COSTDECK/api/scaling/groups/dev/manual" \\
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \\
  -d '{"active": false, "until": "4h"}'

# Follow the schedule again
curl -X POST "$COSTDECK/api/scaling/groups/dev/manual" \\
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \\
  -d '{"active": null}'`,
              kubectl: `# Start now until 18:00 UTC
kubectl patch scalinggroup dev -n costdeck --type merge \\
  -p '{"spec":{"active":true,"activeUntil":"2026-10-05T18:00:00Z"}}'

# Follow the schedule again: remove the fields
kubectl patch scalinggroup dev -n costdeck --type merge \\
  -p '{"spec":{"active":null,"activeUntil":null}}'`,
            },
          },
          { warn: '`active: false` does not mean "follow the schedule": it keeps the namespaces down until the field is removed. To hand control back, set it to `null`.' },
        ],
      },
      {
        id: 'dependencies',
        title: 'Shared platforms and dependencies',
        blocks: [
          { p: 'Environments often share a platform: databases, Kafka, logging. Give the platform its own schedule and let the environments depend on it:' },
          {
            steps: [
              'Create a schedule for the platform namespaces and choose **On demand**.',
              'Edit each environment\'s schedule → **Advanced** → **Start only after** → select the platform.',
            ],
          },
          {
            list: [
              'An environment starts only once the platform is fully up.',
              'The platform stays up while any environment that needs it is up, and goes down after the last one has scaled down.',
              'The platform card shows who keeps it up ("needed by dev").',
            ],
          },
          {
            ways: {
              kubectl: `apiVersion: finops.costdeck.io/v1
kind: ScalingGroup
metadata: { name: platform, namespace: costdeck }
spec:
  category: Platform
  activation: OnDemand
  namespaces: [postgres, kafka]
---
apiVersion: finops.costdeck.io/v1
kind: ScalingGroup
metadata: { name: dev, namespace: costdeck }
spec:
  category: Environments
  namespaces: [dev-frontend, dev-backend]
  dependsOn: [platform]
  schedules:
    - { days: [1, 2, 3, 4, 5], startTime: "08:00", endTime: "19:00", timezone: Europe/Berlin }`,
            },
          },
        ],
      },
      {
        id: 'order',
        title: 'Start order',
        blocks: [
          { p: 'By default all namespaces of a schedule start together. To start them in steps, open the schedule → **Edit** → **Start order** and drag namespaces into stages. Stage 1 starts first and each stage must be ready before the next one starts. Scaling down runs in reverse.' },
          { p: 'Namespaces in no stage start after all stages and stop first. Cloud resources are placed in stages the same way (see Cloud resources).' },
          {
            ways: {
              kubectl: `spec:
  sequence:
    - dev-db                       # stage 1
    - dev-backend dev-worker       # stage 2: both together
    - dev-frontend                 # stage 3`,
            },
          },
          { p: 'If a namespace may hold up the whole start, use **Advanced** → **Don\'t wait forever**: after the time you set (up to 30 minutes) the schedule moves on without it.' },
        ],
      },
      {
        id: 'activity',
        title: 'Follow progress and spot problems',
        blocks: [
          { p: 'Click a schedule card to open it. **Overview** shows the hours, savings, namespaces, start order and dependencies. **Activity** shows the run as a pipeline:' },
          {
            list: [
              '**Starts after**: the schedules it waits for, with their state.',
              '**Stage 1, 2, …**: each namespace as done, running, waiting or with a problem. Click one to see its workloads and failing pods.',
              '**Kept up for**: the schedules that depend on this one.',
              '**Needs attention**: pods that cannot start (image pull errors, crash loops), missing dependencies, conflicts and warning events.',
            ],
          },
        ],
      },
      {
        id: 'rules',
        title: 'Keep some workloads running',
        blocks: [
          { p: 'Open a namespace from Namespace Insights → **Workload rules**:' },
          {
            list: [
              '**Keep running**: workloads that never scale down, by name or pattern such as `redis-*`. Their CronJobs and KEDA objects are left alone too.',
              '**Start order**: the order workloads start in within the namespace, for example the database before the API.',
            ],
          },
          { p: 'Rules apply under whichever schedule covers the namespace.' },
          {
            ways: {
              kubectl: `apiVersion: finops.costdeck.io/v1
kind: ScalingConfig
metadata: { name: dev-backend, namespace: costdeck }
spec:
  targetNamespace: dev-backend
  exclusions: ["redis-*", prometheus]
  sequence:
    - postgres
    - "api-* worker"`,
            },
          },
        ],
      },
      {
        id: 'create-api',
        title: 'Create schedules from code',
        blocks: [
          { p: 'Schedules are Kubernetes resources (`ScalingGroup`) in the operator\'s namespace, so you can keep them in Git next to everything else.' },
          {
            ways: {
              api: `curl -X POST "$COSTDECK/api/scaling/groups" \\
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \\
  -d '{
    "metadata": {"name": "dev"},
    "spec": {
      "category": "Environments",
      "namespaces": ["dev-frontend", "dev-backend"],
      "schedules": [{"days": [1,2,3,4,5], "startTime": "08:00", "endTime": "19:00", "timezone": "Europe/Berlin"}]
    }
  }'`,
              kubectl: `apiVersion: finops.costdeck.io/v1
kind: ScalingGroup
metadata:
  name: dev
  namespace: costdeck
spec:
  category: Environments
  namespaces: [dev-frontend, dev-backend]
  schedules:
    - days: [1, 2, 3, 4, 5]
      startTime: "08:00"
      endTime: "19:00"
      timezone: Europe/Berlin`,
            },
          },
          { warn: 'PUT replaces the whole spec. Read the schedule, change it and send it back complete, or fields you leave out are removed.' },
        ],
      },
      {
        id: 'gitops',
        title: 'Argo CD and Flux',
        blocks: [
          { p: 'A GitOps tool that syncs `spec.replicas` from Git scales workloads straight back up. Tell it to ignore the fields Cost Deck manages. For Argo CD:' },
          {
            code: `spec:
  ignoreDifferences:
    - group: apps
      kind: Deployment
      jsonPointers: [/spec/replicas, /metadata/annotations/costdeck.io~1original-replicas]
    - group: apps
      kind: StatefulSet
      jsonPointers: [/spec/replicas, /metadata/annotations/costdeck.io~1original-replicas]
  syncPolicy:
    syncOptions: [RespectIgnoreDifferences=true]`,
          },
          { p: 'For Flux, leave `spec.replicas` out of the manifests, as you would with an HPA.' },
        ],
      },
    ],
  },

  {
    id: 'cloud',
    title: 'Cloud resources',
    icon: 'cloud',
    summary: 'Stop the dev database and VMs together with the dev environment.',
    topics: [
      {
        id: 'supported',
        title: 'What can be scheduled',
        blocks: [
          {
            table: {
              head: ['Cloud', 'Resources', 'Stopped means'],
              rows: [
                ['AWS', 'Aurora clusters, EC2 instances', 'Stopped; EBS and storage still bill'],
                ['Azure', 'Virtual machines, PostgreSQL and MySQL flexible servers', 'VMs deallocated so compute stops billing; servers stopped'],
                ['Google Cloud', 'Compute Engine instances, Cloud SQL instances', 'Instances stopped; Cloud SQL activation policy NEVER'],
              ],
            },
          },
          { tip: 'Azure restarts a stopped flexible server by itself after seven days, and AWS does the same with Aurora. The schedule stops it again at its next down window.' },
        ],
      },
      {
        id: 'connect',
        title: 'Connect a cloud account',
        blocks: [
          {
            steps: [
              'Open **Settings** → **Cloud accounts** and switch the cloud on.',
              'Enter credentials, or leave them empty to use the pod\'s identity: IRSA or EKS Pod Identity on AWS, workload identity on AKS and GKE.',
              'Optionally limit discovery to resources with certain tags (AWS, Azure) or labels (Google Cloud), and choose the resource types.',
              'Click **Test connection**. It reports how many resources Cost Deck can see.',
            ],
          },
          { p: 'The installation guide (`docs/installation.md`) lists the exact IAM policy, Azure role and Google Cloud roles to grant.' },
        ],
      },
      {
        id: 'add',
        title: 'Add resources to a schedule',
        blocks: [
          {
            steps: [
              'Open the schedule → **Edit** → **Start order**.',
              'Under **Cloud resources**, pick a database or instance from the list.',
              'It is added to the first stage, where databases usually belong. Drag it to another stage if it should start later.',
              'Save.',
            ],
          },
          { p: 'Resources follow the stage order: they start before the namespaces of later stages and stop after them.' },
          {
            ways: {
              api: `# Everything every connected cloud can see
curl -H "Authorization: Bearer $TOKEN" "$COSTDECK/api/discovery"

# One type
curl -H "Authorization: Bearer $TOKEN" "$COSTDECK/api/discovery/azure/vm"`,
              kubectl: `spec:
  namespaces: [dev-frontend, dev-backend]
  externalTargets:
    - provider: aws
      type: aurora
      identifier: dev-db
      region: eu-central-1
    - provider: gcp
      type: gce
      identifier: europe-west1-b/dev-worker
      region: europe-west1-b
  sequence:
    - ext:dev-db
    - dev-backend ext:europe-west1-b/dev-worker
    - dev-frontend`,
            },
          },
          { p: 'Identifiers: the cluster identifier or instance ID on AWS; the full resource ID on Azure (`/subscriptions/…/virtualMachines/name`); `<zone>/<name>` for Compute Engine and the instance name for Cloud SQL. Discovery fills them in for you.' },
        ],
      },
    ],
  },

  {
    id: 'cost',
    title: 'Cost and savings',
    icon: 'cost',
    summary: 'Where the numbers come from, what the cost overview shows, and how to find requests that are too large.',
    topics: [
      {
        id: 'overview',
        title: 'The cost overview',
        blocks: [
          { p: '**Cost Overview** is the start page. It answers four questions: what does the cluster cost, how much of it is used, what do the schedules save, and what should we do next.' },
          {
            table: {
              head: ['Tile', 'Means'],
              rows: [
                ['Monthly run rate', 'What the nodes cost at today\'s rates, over an average month (730 hours). This is the bill.'],
                ['This month', 'What the nodes have cost since the 1st, from the cost history, and a forecast for the whole month at the current rate.'],
                ['Cost efficiency', 'What pods use as a share of the node bill. The line below splits it: how much of the nodes pods request, and how much of that they use.'],
                ['Saved by schedules', 'What the schedules keep down right now, as a monthly rate, and what they saved this month.'],
                ['Could save', 'Right-sizing advice plus what working hours would save on non-production namespaces that run around the clock.'],
              ],
            },
          },
          { p: '**Where the money goes** splits the node bill three ways. **Used** is what pods actually consume. **Requested, not used** is reserved by requests but idle: right-sizing recovers it. **Not requested** is node capacity no pod asked for: fewer or smaller nodes, or an autoscaler, recover it.' },
          { p: '**What to do next** ranks concrete actions by what they save: right-size a namespace, put a non-production namespace on a schedule, reduce unrequested capacity, look into a namespace whose cost jumped week over week, or set missing requests.' },
        ],
      },
      {
        id: 'insights',
        title: 'Namespace Insights: filter, group and export',
        blocks: [
          { p: '**Namespace Insights** lists every namespace with its monthly cost, a 14-day trend and the change against the week before, CPU and memory used against requested, efficiency, idle cost, what it could save and its schedule.' },
          {
            list: [
              'Filter by environment, schedule (scheduled, not scheduled, down right now), finding (could save, overprovisioned, missing requests, no limits) and team.',
              'Group by team, environment or schedule to see subtotals, and sort by any column.',
              '**Include unrequested capacity** shares the cost of capacity nobody requested out to namespaces in proportion to what they request, so the namespaces add up to the whole node bill. Use it for showback and chargeback.',
              '**Export CSV** downloads the rows as shown, for a spreadsheet or a monthly report.',
              '**Cards** switches to the per-namespace usage charts.',
            ],
          },
        ],
      },
      {
        id: 'history',
        title: 'Cost history',
        blocks: [
          { p: 'Kubernetes keeps no cost history, so Cost Deck keeps its own. Every five minutes it books the cluster\'s cost into daily totals per namespace and per schedule: UTC days, 90 days, in the `costdeck-cost-history` ConfigMap of the operator namespace. Charts, month to date and week-over-week changes come from it.' },
          { tip: 'History starts when Cost Deck is installed. While the operator is down nothing is booked, and the day shows fewer observed hours; it is never filled in by guessing.' },
        ],
      },
      {
        id: 'classify',
        title: 'Environments and teams',
        blocks: [
          { p: 'Cost Deck sorts namespaces into production, non-production, system and unclassified. A namespace label wins: `environment`, `env`, `app.kubernetes.io/environment`, `stage` or `tier` with a value such as `prod`, `staging` or `dev`. Without one, the words in the name decide: `shop-prod` is production, `dev-api` and `team-a-staging` are not.' },
          { p: 'The team is the first of these namespace labels that is set: `team`, `owner`, `app.kubernetes.io/team`, `cost-center`, `business-unit`, `department`, `app.kubernetes.io/part-of`. Label your namespaces to see cost by team.' },
          { code: 'kubectl label namespace dev-api team=payments environment=dev' },
        ],
      },
      {
        id: 'rates',
        title: 'Where the prices come from',
        blocks: [
          { p: 'Cost Deck turns CPU and memory requests into money with a price per core-hour and per GiB-hour. It uses the first of these that is available:' },
          {
            steps: [
              '**Custom rates** that you enter under Settings → Prices. Use them for negotiated prices or on-premises clusters.',
              '**Cloud list prices**, when switched on: every node is priced at its list price by instance type and region (AWS Price List, Azure Retail Prices), and the total is split into the two rates.',
              '**An estimate** per cloud when nothing better is available.',
            ],
          },
          {
            table: {
              head: ['Estimate for', 'Per core-hour', 'Per GiB-hour'],
              rows: [['AWS', '$0.040', '$0.004'], ['Azure', '$0.042', '$0.005'], ['Google Cloud', '$0.038', '$0.004'], ['Other', '$0.035', '$0.003']],
            },
          },
          { p: 'Every cost figure says which basis it uses, and Settings → Prices shows the rates in effect. Spot, reservations and savings plans are not applied, so treat the numbers as list prices.' },
          {
            ways: {
              api: `curl -X POST "$COSTDECK/api/costing" \\
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \\
  -d '{"targetType": "namespace", "targetName": "dev-backend"}'`,
              kubectl: `# CostDeckConfig "default" in the operator namespace
spec:
  pricing:
    cpuCoreHour: "0.031"
    memoryGiBHour: "0.0042"
    currency: EUR`,
            },
          },
        ],
      },
      {
        id: 'billing',
        title: 'Reconcile with the cloud bill',
        blocks: [
          { p: 'List prices ignore what you actually pay: Savings Plans, Reserved Instances, committed use discounts, enterprise agreements and spot. Reconciliation reads the real bill for the cluster\'s nodes and scales every compute cost by billed ÷ list price.' },
          {
            table: {
              head: ['Cloud', 'Reads', 'Needs'],
              rows: [
                ['AWS', 'Amortized EC2 cost from Cost Explorer, for instances with the cluster tag (aws:eks:cluster-name by default)', 'ce:GetCostAndUsage, and the tag activated as a cost allocation tag'],
                ['Azure', 'Amortized virtual machine cost of the AKS node resource group from Cost Management', 'Cost Management Reader on the node resource group'],
                ['Google Cloud', 'Compute Engine cost with credits for the cluster\'s nodes, from the billing export in BigQuery', 'BigQuery Job User, and Data Viewer on the export dataset'],
              ],
            },
          },
          {
            steps: [
              'Open **Settings** → **Cloud bill** and switch it on.',
              'Choose the cloud and fill in the tag, the resource group or the export table.',
              'Click **Reconcile now**. The result reads, for example, "Billed 72% of list price".',
            ],
          },
          { tip: 'The bill arrives a day or two late, so a week ending two days ago is compared, every six hours. A ratio below 10% or above 300% is not applied: it means the filter selects the wrong resources.' },
        ],
      },
      {
        id: 'savings',
        title: 'How savings are counted',
        blocks: [
          { p: 'A schedule\'s saving is what its namespaces would cost if they were running right now, counted while they are down. It is an estimate from the workloads\' requests at the rates above, not a figure from your cloud bill.' },
        ],
      },
      {
        id: 'reduce',
        title: 'What can be reduced',
        blocks: [
          { p: 'Open a namespace from Namespace Insights. **What can be reduced** lists containers that request much more than they use, with a suggested request, the monthly saving and a YAML snippet to apply through your own pipeline.' },
          {
            list: [
              'With VictoriaMetrics: the 95th percentile of CPU and the peak memory over up to 14 days, plus 20% headroom.',
              'With metrics-server only: a single current reading plus 50% headroom. Connect VictoriaMetrics for advice you can trust.',
              'Containers that use **more** than they request are flagged too: they compete for capacity they never reserved.',
            ],
          },
          { tip: 'Cost Deck only advises. It never changes requests or limits; you stay in control of your manifests.' },
          {
            ways: {
              api: `curl -H "Authorization: Bearer $TOKEN" "$COSTDECK/api/namespaces/dev-backend/recommendations"`,
            },
          },
        ],
      },
      {
        id: 'victoria',
        title: 'Connect VictoriaMetrics',
        blocks: [
          { p: 'Without history Cost Deck sees only the current minute. VictoriaMetrics, or any Prometheus-compatible API that scrapes cAdvisor, gives it weeks.' },
          {
            steps: [
              'Open **Settings** → **Usage metrics** and switch VictoriaMetrics on.',
              'Enter the query URL, for example `http://vmselect.monitoring:8481/select/0/prometheus` or `http://victoria-metrics:8428`.',
              'If several clusters share it, add a label selector such as `cluster="prod-eu"`.',
              'Click **Test connection**.',
            ],
          },
          { tip: '"Connected, but no container metrics found" means the URL works but cAdvisor is not scraped, or the label selector matches nothing. Cost Deck needs `container_cpu_usage_seconds_total` and `container_memory_working_set_bytes`.' },
          {
            ways: {
              kubectl: `spec:
  integrations:
    victoriaMetrics:
      enabled: true
      endpoint: http://vmselect.monitoring:8481/select/0/prometheus
      labelSelector: cluster="prod-eu"
      retentionDays: 14
      secretRef: vm-credentials   # optional: BEARER_TOKEN, or USERNAME and PASSWORD`,
            },
          },
        ],
      },
      {
        id: 'tags',
        title: 'Findings',
        blocks: [
          {
            table: {
              head: ['Tag', 'Meaning'],
              rows: [
                ['Missing Requests', 'Some containers request no CPU or memory, so the scheduler cannot plan for them and their cost is unknown.'],
                ['No limits', 'Some containers have no limits.'],
                ['Overprovisioned CPU / RAM', 'The namespace uses less than 30% of what it requests.'],
                ['Healthy', 'None of the above.'],
              ],
            },
          },
        ],
      },
      {
        id: 'legacy',
        title: 'Undo an automatic right-sizing from an earlier version',
        blocks: [
          { p: 'Cost Deck 1.3 and earlier could rewrite requests automatically. Namespaces changed that way show **Revert optimization**, which restores the values from before the change. Newer versions only advise.' },
        ],
      },
    ],
  },

  {
    id: 'nodes',
    title: 'Nodes and bin-packing',
    icon: 'nodes',
    summary: 'What the nodes cost, how well pods fill them, and which capacity nobody uses.',
    topics: [
      {
        id: 'map',
        title: 'The node map',
        blocks: [
          { p: '**Cluster Node Map** shows every node as a tile, grouped by node pool (or instance type, zone, spot or on-demand). Each tile has the node\'s monthly cost and two bars, CPU and memory, split three ways: **used**, **requested but not used**, and **not requested**.' },
          { p: 'The node pool comes from the usual labels: Karpenter `karpenter.sh/nodepool`, EKS `eks.amazonaws.com/nodegroup`, GKE `cloud.google.com/gke-nodepool`, AKS `kubernetes.azure.com/agentpool`. Click a node for its pods, the namespaces on it and its system details.' },
          {
            table: {
              head: ['Number', 'Means'],
              rows: [
                ['Requests fill the nodes', 'Bin-packing: what share of the allocatable CPU and memory pods request. Low values mean nodes are bigger or more numerous than the workloads ask for.'],
                ['Not requested', 'Node cost no pod asked for. An autoscaler with consolidation, or fewer and smaller nodes, recovers it.'],
                ['Pods', 'Pods placed against the most the nodes accept, and pods that cannot be scheduled.'],
              ],
            },
          },
        ],
      },
      {
        id: 'findings',
        title: 'Findings',
        blocks: [
          {
            table: {
              head: ['Finding', 'What to do'],
              rows: [
                ['Nodes could be emptied', 'Their pods would fit into what the other nodes have left. Let Cluster Autoscaler or Karpenter consolidate them; check affinity, local volumes and disruption budgets first.'],
                ['Memory (or CPU) runs out first', 'Requests fill one resource far more than the other, so nodes are added for it while the other idles. Choose instance types with a better CPU-to-memory ratio.'],
                ['Pods cannot be scheduled', 'No node has room for their requests, or their constraints match no node.'],
                ['Node not ready or under pressure', 'A node that still costs money but runs nothing new, or one evicting pods because requests are below real usage.'],
                ['No spot capacity', 'Spot or preemptible nodes cost 60–90% less; stateless and non-production workloads are good candidates.'],
              ],
            },
          },
          { tip: 'Node cost is its capacity at the rates in effect, so with cloud list prices switched on the nodes add up to the real list-price bill.' },
        ],
      },
      {
        id: 'shapes',
        title: 'Node shape recommendations',
        blocks: [
          { p: 'For every node pool Cost Deck adds up what its pods request (DaemonSets counted on every node), and finds the cheapest instance type and count that holds it at 85% of allocatable capacity, keeping at least two nodes where there were two. It recommends the change when it saves at least 10%.' },
          {
            list: [
              'The shape follows the workload: a pool whose pods ask for much memory and little CPU moves to a memory-optimised family, and the other way round.',
              'An **arm64** alternative (Graviton, Ampere, Axion) is shown separately: it is cheaper still, but only if your images are built for arm64.',
              'Prices are list prices of a reference region (us-east-1, eastus, us-central1). With cloud list prices on, they are scaled to your region by the live price of the current type.',
              'Spot pools are left alone: their price moves too much to compare.',
            ],
          },
        ],
      },
      {
        id: 'spot',
        title: 'Spot capacity',
        blocks: [
          { p: 'The node map shows how much of the node bill is already on spot, and what non-production namespaces request. Moving those to a spot node pool, with a taint only they tolerate, is estimated at 60% off, the low end of what the clouds quote.' },
        ],
      },
    ],
  },

  {
    id: 'storage',
    title: 'Storage and network',
    icon: 'storage',
    summary: 'Volumes and load balancers are billed apart from the nodes, and are where waste hides.',
    topics: [
      {
        id: 'what',
        title: 'What is counted',
        blocks: [
          {
            table: {
              head: ['Thing', 'Priced at'],
              rows: [
                ['Persistent volumes', 'Their size × the list price of their disk type per GiB-month: gp3, io2, Premium SSD, pd-balanced and so on, read from the StorageClass. Local volumes are part of the node and cost nothing extra.'],
                ['Load balancers', 'Every Service of type LoadBalancer at the cloud\'s base price per month (about $16–18), before traffic. In-cluster load balancers such as MetalLB cost nothing.'],
              ],
            },
          },
          { p: 'Both are charged to their namespace, so Namespace Insights, budgets and the digest include them. Set your own prices under Settings → Prices, which you must do when costs are not in USD.' },
          { warn: 'Traffic and data transfer are not counted: they need flow data that Kubernetes does not keep.' },
        ],
      },
      {
        id: 'waste',
        title: 'Finding waste',
        blocks: [
          {
            table: {
              head: ['State', 'Means'],
              rows: [
                ['Unused', 'A volume that is bound but mounted by no pod. Delete it if the data is no longer needed, after a snapshot if in doubt.'],
                ['Orphaned', 'A volume released by its claim but kept by its reclaim policy: the disk is still billed.'],
                ['Kept while down', 'Its namespace is scaled down by a schedule; the volume waits for it, as it should.'],
                ['Nothing behind it', 'A load balancer with no ready endpoint, still billed by the hour.'],
                ['Billed while down', 'A load balancer whose namespace a schedule keeps down. A shared Ingress in an always-on namespace avoids one per environment.'],
              ],
            },
          },
        ],
      },
    ],
  },

  {
    id: 'budgets',
    title: 'Budgets and alerts',
    icon: 'budgets',
    summary: 'Give teams a monthly limit, and hear about it before the month is over.',
    topics: [
      {
        id: 'create',
        title: 'Create a budget',
        blocks: [
          {
            steps: [
              'Open **Budgets & Alerts** and click **New budget**.',
              'Choose what it covers: a team (namespaces with that `team` label), a namespace, an environment or the whole cluster.',
              'Enter the monthly limit and the thresholds that alert, 80% and 100% by default.',
              'Keep **Also alert when the month is heading over** on to hear early: it alerts once when spending so far plus the current hourly cost for the rest of the month exceeds the limit.',
            ],
          },
          { p: 'A budget counts compute, volumes and load balancers of its namespaces, for the calendar month in UTC.' },
          {
            ways: {
              kubectl: `# CostDeckConfig "default"
spec:
  budgets:
    - name: payments
      scope: team            # cluster, namespace, team or environment
      value: payments
      monthlyLimit: "1200"
      thresholds: [80, 100]
      forecast: true`,
            },
          },
        ],
      },
      {
        id: 'alerts',
        title: 'Alerts',
        blocks: [
          {
            list: [
              '**Budget alerts** fire once per threshold per month. Reaching 100% first never sends the 80% alert afterwards.',
              '**Anomaly alerts** compare each namespace\'s cost for the day before with its average over the seven days before that, and fire when it is the chosen percentage higher and at least the minimum amount more.',
              'Alerts go to the Webex space when one is connected, and are always listed under **Recent alerts**.',
            ],
          },
          { code: `# Prometheus: budgets heading over
costdeck_budget_forecast_ratio > 1`, label: 'Alert from your own monitoring' },
        ],
      },
    ],
  },

  {
    id: 'reports',
    title: 'Reports',
    icon: 'reports',
    summary: 'The cost digest everyone reads, sent on a schedule, and AI-written reports for each audience.',
    topics: [
      {
        id: 'digest',
        title: 'The cost digest',
        blocks: [
          { p: 'The digest compares the last seven days, or the last calendar month, with the period before: what the nodes cost, what pods requested and used, what the schedules saved, the most expensive namespaces and teams, and the actions worth taking. It comes from the cost history, not from a model, so engineers and finance read the same numbers.' },
          { p: 'Open **Reports**, pick **Last 7 days** or **Last month**, then copy it as Markdown, download it, or send it to the Webex space now.' },
          { ways: { api: `curl -H "Authorization: Bearer $TOKEN" "$COSTDECK/api/reports/digest?period=week"` } },
        ],
      },
      {
        id: 'schedule',
        title: 'Send it every week',
        blocks: [
          {
            steps: [
              'Connect Webex with a space ID (Settings → Notifications → Webex).',
              'In **Reports** → **Scheduled digest**, switch it on and choose weekly (Mondays) or monthly (the 1st), the time and the time zone.',
              'Save. The digest is posted at that time and kept in the history.',
            ],
          },
          {
            ways: {
              kubectl: `# CostDeckConfig "default"
spec:
  reports:
    digest:
      enabled: true
      frequency: weekly      # or monthly
      time: "09:00"
      timezone: Europe/Berlin`,
            },
          },
          { tip: 'Turning the schedule on, or changing it, never sends a digest for an occasion that has already passed; the first one goes out at the next Monday or 1st.' },
        ],
      },
      {
        id: 'ai',
        title: 'AI reports for each audience',
        blocks: [
          { p: 'With an AI model connected, **Generate report** writes a report from the same overview, digest and node data, for one of three audiences:' },
          {
            table: {
              head: ['Audience', 'Gets'],
              rows: [
                ['Executive', 'One page: run rate, trend, efficiency, savings and three decisions with their savings.'],
                ['Engineering', 'Waste by namespace, right-sizing, scheduling, nodes and hygiene, ending in a checklist ordered by saving.'],
                ['Team showback', 'Cost and efficiency per team and namespace, with what each team can do.'],
              ],
            },
          },
          { p: 'The model is told to quote Cost Deck\'s figures and never compute its own. Every report is kept in the history (the newest 24), where it can be downloaded as Markdown, printed to PDF or deleted.' },
        ],
      },
    ],
  },

  {
    id: 'access',
    title: 'Access and sign-in',
    icon: 'access',
    summary: 'Roles, single sign-on with Microsoft Entra ID, and API tokens.',
    topics: [
      {
        id: 'roles',
        title: 'Roles',
        blocks: [
          {
            table: {
              head: ['Role', 'Can'],
              rows: [
                ['viewer', 'See everything; use the assistant and MCP read-only.'],
                ['operator', 'Also start and stop schedules, hand them back to the schedule, generate reports, read logs.'],
                ['admin', 'Also create, edit and delete schedules, change settings and manage API tokens.'],
              ],
            },
          },
        ],
      },
      {
        id: 'entra',
        title: 'Sign in with Microsoft Entra ID',
        blocks: [
          {
            steps: [
              'In the Entra admin center, create an app registration with a **Web** redirect URI `https://<your-host>/api/auth/entra/callback`, and a client secret.',
              'Add a groups claim to the token (Token configuration → Add groups claim).',
              'In Cost Deck open **Settings** → **Single sign-on**, enter the tenant ID, client ID and secret, and map Entra groups to roles.',
              'Click **Test connection**, then sign in with Microsoft in a private window.',
            ],
          },
          { tip: 'Once SSO works you can turn off local login. Keep the admin password anyway: it is the way back in if SSO breaks.' },
          { p: 'The installation guide covers multi-tenant setups, app roles, sovereign clouds and the single-page-app redirect URI.' },
        ],
      },
      {
        id: 'tokens',
        title: 'API tokens',
        blocks: [
          { p: 'Scripts, CI pipelines and MCP clients use API tokens instead of a password.' },
          {
            steps: [
              'Open **Settings** → **API tokens**.',
              'Give the token a name, a role and an expiry, and create it.',
              'Copy it now: it is shown once and only a hash is stored.',
            ],
          },
          { code: `${apiSetup}

curl -H "Authorization: Bearer $TOKEN" "$COSTDECK/api/scaling/groups"`, label: 'Use it' },
        ],
      },
      {
        id: 'sessions',
        title: 'Sessions',
        blocks: [
          { p: 'Signing in to the dashboard sets an HttpOnly `costdeck-session` cookie, valid for 12 hours, marked Secure over HTTPS and SameSite=Lax. More than five password attempts a minute from one address are refused.' },
          { p: 'Without a session or token only these answer: sign-in and sign-out, the sign-in options, the OpenAPI spec and Swagger UI, and the Webex webhook (which checks its own signature).' },
        ],
      },
    ],
  },

  {
    id: 'ai',
    title: 'AI assistant',
    icon: 'ai',
    summary: 'Ask questions about cost and schedules in plain language, and generate cost reports.',
    topics: [
      {
        id: 'connect',
        title: 'Connect a model',
        blocks: [
          {
            steps: [
              'Open **Settings** → **AI assistant** and switch AI on.',
              'Choose Anthropic (Claude), OpenAI, Google Gemini, or any OpenAI-compatible endpoint such as Ollama, vLLM or a company gateway.',
              'Enter the API key (and the base URL for OpenAI-compatible endpoints), click **Load available models** and pick one.',
              'Save.',
            ],
          },
          { tip: 'The model sees what the assistant\'s tools return: namespace and workload names, usage, costs and schedule states. It cannot read Secrets or logs.' },
        ],
      },
      {
        id: 'ask',
        title: 'Ask the assistant',
        blocks: [
          { p: 'Click **CostDeck AI** in the bottom-right corner of any page. The assistant reads live data through the same tools MCP clients use, so its answers reflect the cluster as it is now. Try:' },
          {
            list: [
              '"Which namespaces waste the most money?"',
              '"Why is staging still up?"',
              '"Start dev until 6 pm."',
            ],
          },
          { p: 'When the assistant wants to change something, it shows a confirmation card. Nothing happens until someone with the operator role clicks **Confirm**.' },
        ],
      },
      {
        id: 'reports',
        title: 'Cost reports',
        blocks: [
          { p: 'Open **Reports**, choose who the report is for (executive, engineering or team showback) and click **Generate report**. See the Reports guide for the digest, the schedule and the history.' },
        ],
      },
    ],
  },

  {
    id: 'mcp',
    title: 'MCP clients',
    icon: 'mcp',
    summary: 'Use Cost Deck from Claude Code, Claude Desktop, Cursor or any MCP client.',
    topics: [
      {
        id: 'connect',
        title: 'Connect a client',
        blocks: [
          {
            steps: [
              'Open **Settings** → **MCP server** and switch it on.',
              'Create an API token (Settings → API tokens). A viewer token gets the read-only tools; operator and admin tokens also get the scaling actions.',
              'Add the server to your client:',
            ],
          },
          {
            code: `# Claude Code
claude mcp add --transport http costdeck https://costdeck.example.com/mcp \\
  --header "Authorization: Bearer cdk_..."

# Claude Desktop, Cursor and others (mcpServers)
{
  "mcpServers": {
    "costdeck": {
      "url": "https://costdeck.example.com/mcp",
      "headers": { "Authorization": "Bearer cdk_..." }
    }
  }
}`,
          },
          { p: 'MCP uses Streamable HTTP on the dashboard\'s own URL and port, so there is nothing extra to expose.' },
        ],
      },
      {
        id: 'tools',
        title: 'Tools',
        blocks: [
          {
            table: {
              head: ['Tool', 'Does'],
              rows: [
                ['get_cluster_overview', 'Nodes, cost and the biggest namespaces'],
                ['list_scaling_groups, get_scaling_group', 'Schedules and their state'],
                ['list_namespace_configs', 'Per-namespace configs and workload rules'],
                ['list_namespaces, get_namespace_status', 'Usage and cost per namespace'],
                ['get_cost_overview', 'Run rate, efficiency, month to date, savings and the ranked opportunities'],
                ['get_rightsizing_recommendations', 'What can be reduced in a namespace'],
                ['scale_group, scale_namespace', 'Start now, scale down now, or resume the schedule (operator)'],
                ['revert_optimization', 'Undo an automatic right-sizing from version 1.3 or earlier (operator)'],
              ],
            },
          },
          { warn: 'Action tools change the cluster immediately. Give clients a viewer token unless they need to scale.' },
        ],
      },
    ],
  },

  {
    id: 'webex',
    title: 'Webex',
    icon: 'webex',
    summary: 'Start and stop environments from a Webex space and get told when they are ready.',
    topics: [
      {
        id: 'setup',
        title: 'Set up the bot',
        blocks: [
          {
            steps: [
              'Create a bot at developer.webex.com and copy its token.',
              'Add the bot to the space your team uses for Cost Deck and copy the space ID.',
              'Open **Settings** → **Notifications** → **Webex**, enter both and save. Switch on **Announce scaling transitions** to hear when a schedule has finished scaling.',
            ],
          },
          { p: 'Scaling commands are accepted only in that space, so its members are who may scale. Without a space the bot only answers `list` and `status`, because anyone on Webex can message a bot.' },
          {
            list: [
              '**Polling** (default): the operator reads new messages every 10 seconds. Nothing has to reach the cluster from outside. In group spaces, @mention the bot.',
              '**Webhook**: set a webhook secret and point a Webex webhook at `https://<your-host>/api/webex/webhook`. Messages arrive instantly and are verified by signature.',
            ],
          },
        ],
      },
      {
        id: 'commands',
        title: 'Commands',
        blocks: [
          {
            code: `list                                     all schedules and their state
status group <name>                      details of one schedule
status config <namespace>
scale group <name> up|down [for 4h | until next | forever]
scale config <namespace> up|down [...]
resume group <name>                      follow the schedule again
resume config <namespace>
help`,
          },
          { p: 'Without a duration, a scale command holds until the next scheduled change. When several clusters share one space, set `clusterName` in each cluster\'s CostDeckConfig and start commands with it: `staging scale group dev down`.' },
        ],
      },
    ],
  },

  {
    id: 'health',
    title: 'Monitoring Cost Deck',
    icon: 'health',
    summary: 'Prometheus metrics, health and logs of the operator itself.',
    topics: [
      {
        id: 'page',
        title: 'Cost Deck Health',
        blocks: [
          { p: 'The **Cost Deck Health** page shows the operator\'s status and errors, its CPU and memory over time, how many namespaces it manages, and its logs. Operators can read the logs; admins can download them.' },
        ],
      },
      {
        id: 'replicas',
        title: 'One replica or more',
        blocks: [
          { p: 'One replica is enough for scaling: schedules are reconciled from their state every minute, so a restart only delays the next change by a minute or two. While it restarts, though, the dashboard, the API, MCP and the Webex webhook are unavailable.' },
          { p: 'Run **two replicas** when people or tools rely on the dashboard and API. Every replica serves them; one leader runs the controllers, the Webex poller, the cost history and the digest, and another takes over within about 15 seconds. With `replicaCount` above 1 the chart spreads the replicas over nodes and adds a PodDisruptionBudget.' },
          { code: `helm upgrade costdeck-operator oci://ghcr.io/migalsp/costdeck/charts/costdeck-operator \\
  -n costdeck --reset-then-reuse-values --set replicaCount=2`, label: 'Two replicas' },
          { p: 'The health page lists the replicas and marks the leader; its logs are the leader\'s, where the controllers write.' },
        ],
      },
      {
        id: 'metrics',
        title: 'Prometheus metrics',
        blocks: [
          { p: 'The `costdeck-operator-metrics` Service (port 8080) exports:' },
          {
            table: {
              head: ['Metric', 'Meaning'],
              rows: [
                ['costdeck_scaling_desired_up', '1 while a schedule wants its workloads up'],
                ['costdeck_scaling_ready', '1 when every target reached the desired state'],
                ['costdeck_scaling_override_active', '1 while a manual override is in force'],
                ['costdeck_estimated_hourly_savings', 'Cost of what is currently kept down'],
                ['costdeck_cluster_estimated_monthly_cost', 'Cluster run rate: part="nodes", "storage", "network", "requested" or "used"'],
                ['costdeck_budget_spent_ratio', 'A budget\'s spending this month as a share of its limit'],
                ['costdeck_budget_forecast_ratio', 'A budget\'s forecast for the month as a share of its limit'],
                ['costdeck_namespace_estimated_monthly_cost', 'Cost of a namespace\'s running pods'],
                ['costdeck_namespace_cpu_usage_cores', 'Observed CPU'],
                ['costdeck_namespace_memory_usage_bytes', 'Observed working-set memory'],
              ],
            },
          },
          { code: `# Helm values, with the Prometheus Operator
metrics:
  serviceMonitor:
    enabled: true
    labels: { release: kube-prometheus-stack }`, label: 'Scrape it' },
          { tip: 'A useful alert: `costdeck_scaling_desired_up != costdeck_scaling_ready` for more than 15 minutes means a schedule is stuck.' },
        ],
      },
    ],
  },

  {
    id: 'help',
    title: 'Troubleshooting',
    icon: 'help',
    summary: 'The questions people ask most, and where to look.',
    topics: [
      {
        id: 'not-down',
        title: 'My environment did not scale down',
        blocks: [
          {
            list: [
              'Does the card say **Kept up manually**? Someone started it; click **Follow schedule**.',
              'Does it say **Up · needed by …**? Another schedule depends on it and is up.',
              'Is the time zone right? "19:00 UTC" is 21:00 in Berlin in summer.',
              'Did a GitOps tool scale it back up? See Schedules → Argo CD and Flux.',
              'Is the workload in **Keep running** on the namespace page?',
            ],
          },
        ],
      },
      {
        id: 'stuck',
        title: 'It has been "Starting…" for a long time',
        blocks: [
          { p: 'Open the schedule → **Activity**. **Needs attention** lists pods that cannot start and why: an image that cannot be pulled, a crash loop, a pending pod with no room on any node. Fix the cause, or use **Advanced** → **Don\'t wait forever** so one namespace cannot hold up the rest.' },
        ],
      },
      {
        id: 'waiting',
        title: 'It says "Waiting for … to start"',
        blocks: [
          { p: 'The schedule depends on another one that is not fully up yet. Open the dependency and look at its Activity. If the dependency does not exist any more, remove it under **Edit** → **Advanced**.' },
        ],
      },
      {
        id: 'conflict',
        title: 'A namespace shows a conflict',
        blocks: [
          { p: 'Two schedules include the same namespace. The older schedule keeps it; remove it from one of them.' },
        ],
      },
      {
        id: 'no-history',
        title: 'The daily chart is empty or "This month" shows a dash',
        blocks: [
          { p: 'The cost history starts when Cost Deck is installed and fills in every five minutes. The first bars appear within minutes, the week-over-week change after eight days.' },
        ],
      },
      {
        id: 'estimate',
        title: 'Costs say "estimate"',
        blocks: [
          { p: 'No better price source is available. Switch on **Cloud list prices** under Settings → Prices (AWS needs the `pricing:GetProducts` permission; Azure needs nothing), or enter custom rates.' },
        ],
      },
      {
        id: 'logs',
        title: 'Where are the logs?',
        blocks: [
          { p: 'On the **Cost Deck Health** page, or:' },
          { code: 'kubectl logs -n costdeck deploy/costdeck-operator -f' },
        ],
      },
    ],
  },
]
