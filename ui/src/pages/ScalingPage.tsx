import { useState, useEffect } from 'react'
import { Plus, Cloud, Database, Layers, ChevronUp, ChevronDown, CalendarClock, MoonStar, Link2, Hand } from 'lucide-react'
import { errorMessage } from '../lib/api'
import { formatMoney } from '../lib/format'
import type { ExternalTarget, ScalingConfig, ScalingGroup, ScalingSpec } from '../lib/types'
import { usePolling } from '../lib/usePolling'
import type { NamespaceFinOps } from './Dashboard'
import WorkloadRulesDialog from '../components/WorkloadRulesDialog'
import ScheduleCard from '../components/ScheduleCard'
import ScheduleDetails, { type DetailsTab } from '../components/ScheduleDetails'
import ScheduleWizard, { type WizardTab } from '../components/ScheduleWizard'
import { AWSLogo } from '../components/ProviderLogos'
import { useAuth } from '../lib/auth'
import OverrideDialog, { type OverrideUntil } from '../components/OverrideDialog'
import { Button, SectionTitle } from '../components/ui'

type Target = { type: 'group' | 'config'; name: string }

// Kubernetes' own system namespaces are left out of the "not scheduled yet" list.
const isSystemNamespace = (ns: string) => ns.startsWith('kube-')

type WizardState =
  | { kind: 'group'; existing?: ScalingGroup; initialNamespaces?: string[]; initialTab?: WizardTab }
  | { kind: 'config'; existing: ScalingConfig }

const ScalingPage: React.FC<{ onSelectNamespace: (ns: string) => void }> = ({ onSelectNamespace }) => {
  const { can } = useAuth();
  const [groups, setGroups] = useState<ScalingGroup[]>([]);
  const [policies, setPolicies] = useState<ScalingConfig[]>([]);
  const [namespaces, setNamespaces] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [wizard, setWizard] = useState<WizardState | null>(null);
  // Workload rules (exclusions and workload order) of a single-namespace config.
  const [ordering, setOrdering] = useState<{ name: string; spec: ScalingSpec } | null>(null);
  const [details, setDetails] = useState<{ kind: 'group' | 'config'; name: string; tab: DetailsTab } | null>(null);
  const [isScalingMap, setIsScalingMap] = useState<Record<string, boolean>>({});
  const [overridePrompt, setOverridePrompt] = useState<Target & { active: boolean; hasSchedule: boolean } | null>(null);
  const [cloudExpanded, setCloudExpanded] = useState(false);

  // Discovery State
  const [discoveredResources, setDiscoveredResources] = useState<Record<string, ExternalTarget[]>>({});
  const [activeDiscoveryTab, setActiveDiscoveryTab] = useState<string>('');
  // Fall back to the first tab when nothing (or a tab that has since emptied) is selected.
  const discoveryTab = discoveredResources[activeDiscoveryTab] ? activeDiscoveryTab : Object.keys(discoveredResources)[0] ?? '';

  useEffect(() => {
    const fetchDiscovery = async () => {
      try {
        const [auroraRes, ec2Res] = await Promise.all([
          fetch('/api/discovery/aws/aurora'),
          fetch('/api/discovery/aws/ec2')
        ]);
        const aurora = await auroraRes.json() || [];
        const ec2 = await ec2Res.json() || [];
        const resources: Record<string, ExternalTarget[]> = {};
        if (aurora.length > 0) resources['Databases'] = aurora;
        if (ec2.length > 0) resources['Compute'] = ec2;
        setDiscoveredResources(resources);
      } catch (err) {
        console.error("Failed to fetch discovered resources", err);
      }
    };
    fetchDiscovery();
  }, []);

  const fetchData = async () => {
    try {
      const [groupsRes, policiesRes, nsRes] = await Promise.all([
        fetch('/api/scaling/groups'),
        fetch('/api/scaling/configs'),
        fetch('/api/namespaces')
      ]);
      const groupsData = await groupsRes.json();
      const policiesData = await policiesRes.json();
      const nsData = await nsRes.json();
      setGroups(((groupsData || []) as ScalingGroup[]).sort((a, b) => a.metadata.name.localeCompare(b.metadata.name)));
      setPolicies(((policiesData || []) as ScalingConfig[]).sort((a, b) => a.spec.targetNamespace.localeCompare(b.spec.targetNamespace)));
      const uniqueNamespaces = Array.from(new Set((nsData as NamespaceFinOps[]).map(n => n.spec.targetNamespace || n.metadata.name)));
      uniqueNamespaces.sort((a, b) => a.localeCompare(b));
      setNamespaces(uniqueNamespaces);
    } catch (err) {
      console.error("Failed to fetch scaling data", err);
    } finally {
      setLoading(false);
    }
  };

  // Status refreshes every 10 seconds; the first load clears the skeleton.
  usePolling(() => fetchData(), 10000);

  // `active` of null clears spec.active so the schedule takes control again. `until`
  // bounds the override so a single click never pins the target forever.
  const handleManualScale = async (type: 'group' | 'config', name: string, active: boolean | null, until?: OverrideUntil) => {
    const key = `${type}-${name}`;
    if (isScalingMap[key]) return;
    setIsScalingMap(prev => ({ ...prev, [key]: true }));
    const endpoint = type === 'group' ? `/api/scaling/groups/${name}/manual` : `/api/scaling/configs/${name}/manual`;
    try {
      const res = await fetch(endpoint, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ active, ...(active !== null && until && until !== 'forever' ? { until } : {}) })
      });
      if (!res.ok) throw new Error(await res.text());
      fetchData();
      // Poll quickly for a while so the transition shows up without waiting for the
      // regular refresh.
      let attempts = 0;
      const fastPoll = setInterval(() => {
        fetchData();
        if (++attempts > 10) {
          clearInterval(fastPoll);
          setIsScalingMap(prev => ({ ...prev, [key]: false }));
        }
      }, 2000);
    } catch (err) {
      setError(`Could not change ${name}: ${errorMessage(err)}`);
      setIsScalingMap(prev => ({ ...prev, [key]: false }));
    }
  };

  const handleSaveOrder = async (spec: ScalingSpec) => {
    if (!ordering) return;
    const isGroup = groups.some(g => g.metadata.name === ordering.name);
    const endpoint = isGroup ? `/api/scaling/groups/${ordering.name}` : `/api/scaling/configs/${ordering.name}`;
    try {
      const res = await fetch(endpoint, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ metadata: { name: ordering.name }, spec })
      });
      if (!res.ok) throw new Error((await res.text()) || res.statusText);
      setOrdering(null);
      setError(null);
      fetchData();
    } catch (err) {
      setError(`Could not save the start order: ${errorMessage(err)}`);
    }
  };

  const deleteGroup = async (name: string) => {
    try {
      const res = await fetch(`/api/scaling/groups/${name}`, { method: 'DELETE' });
      if (!res.ok) throw new Error(await res.text());
      setDetails(null);
      fetchData();
    } catch (err) {
      setError(`Could not delete ${name}: ${errorMessage(err)}`);
    }
  };

  const grouped = new Set(groups.flatMap(g => g.spec.namespaces));
  // A config whose namespace belongs to a group only fine-tunes workloads for that group;
  // a config on its own is a single-namespace schedule.
  const standaloneConfigs = policies.filter(p => !grouped.has(p.spec.targetNamespace));
  const scheduled = new Set([...grouped, ...standaloneConfigs.map(p => p.spec.targetNamespace)]);
  const unscheduled = namespaces.filter(ns => !scheduled.has(ns) && !isSystemNamespace(ns));
  const categories = Array.from(new Set(groups.map(g => g.spec.category || 'General'))).sort();

  const totalHourly = [...groups.map(g => g.status), ...standaloneConfigs.map(p => p.status)]
    .reduce((sum, st) => sum + (parseFloat(st?.estimatedHourlySavings || '') || 0), 0);
  const currency = [...groups, ...policies].find(o => o.status?.currency)?.status?.currency || 'USD';

  const prompt = (type: 'group' | 'config', name: string, active: boolean, spec: ScalingSpec) =>
    setOverridePrompt({ type, name, active, hasSchedule: (spec.schedules?.length || 0) > 0 || spec.activation === 'OnDemand' });

  const groupCard = (group: ScalingGroup) => (
    <ScheduleCard
      key={group.metadata.name}
      name={group.metadata.name}
      namespaces={group.spec.namespaces}
      spec={group.spec}
      generation={group.metadata.generation}
      status={group.status}
      busy={isScalingMap[`group-${group.metadata.name}`]}
      canOperate={can('operator')}
      canAdmin={can('admin')}
      onOpen={tab => setDetails({ kind: 'group', name: group.metadata.name, tab })}
      onStart={() => prompt('group', group.metadata.name, true, group.spec)}
      onStop={() => prompt('group', group.metadata.name, false, group.spec)}
      onResume={() => handleManualScale('group', group.metadata.name, null)}
      onEdit={() => setWizard({ kind: 'group', existing: group })}
    />
  );

  const ResourceCard = ({ item }: { item: ExternalTarget }) => (
    <div className="bg-white border border-slate-200 rounded-xl p-4 flex flex-col group hover:border-brand-300 hover:shadow-md transition-all cursor-default relative overflow-hidden">
      <div className="flex items-center gap-3 min-w-0 mb-3 mt-1">
        <AWSLogo className="grayscale group-hover:grayscale-0 transition-all" />
        <div className="flex flex-col min-w-0">
          <span className="font-bold text-slate-700 text-sm whitespace-nowrap overflow-hidden text-ellipsis">{item.name || item.identifier}</span>
          <div className="flex items-center gap-2">
            <span className="text-[11px] font-bold text-brand-500 uppercase tracking-wider">{item.type}</span>
            {item.status && (
              <span className={`flex items-center gap-1 text-[10px] font-bold uppercase ${item.status === 'available' || item.status === 'running' ? 'text-emerald-500' : item.status === 'stopped' ? 'text-rose-500' : 'text-amber-500'}`}>
                <span className={`w-1.5 h-1.5 rounded-full ${item.status === 'available' || item.status === 'running' ? 'bg-emerald-500' : item.status === 'stopped' ? 'bg-rose-500' : 'bg-amber-500'}`}></span>
                {item.status}
              </span>
            )}
          </div>
        </div>
      </div>
      <div className="text-xs text-slate-500 font-medium flex items-center gap-1">
        <Database size={12} className="opacity-50" /> Region: <span className="text-slate-700">{item.region}</span>
      </div>
    </div>
  );

  return (
    <div className="p-8 max-w-7xl mx-auto min-h-full">
      <div className="flex flex-wrap justify-between items-start gap-4 mb-8">
        <div>
          <h1 className="text-2xl font-bold tracking-tight text-slate-900 flex items-center gap-2">Scaling</h1>
          <p className="mt-1 text-sm text-slate-500">Scale non-production namespaces to zero when nobody needs them, and bring them back on time.</p>
          {totalHourly > 0 && (
            <p className="mt-2 inline-flex items-center gap-2 px-3 py-1.5 rounded-xl bg-emerald-50 border border-emerald-100 text-emerald-700 text-sm font-bold">
              Saving ~{formatMoney(totalHourly, currency)}/h right now (≈ {formatMoney(totalHourly * 730, currency)}/month at this rate)
            </p>
          )}
        </div>
        {can('admin') && (
          <Button variant="primary" icon={<Plus size={16} />} onClick={() => setWizard({ kind: 'group' })}>New schedule</Button>
        )}
      </div>

      {error && (
        <div className="mb-6 p-4 bg-rose-50 border border-rose-100 rounded-xl flex items-center gap-3 text-rose-600">
          <div className="flex-1 text-sm font-bold">{error}</div>
          <button onClick={() => setError(null)} className="p-1 hover:bg-rose-100 rounded-full text-rose-400"><Plus size={16} className="rotate-45" /></button>
        </div>
      )}

      {loading ? (
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6">
          {[1, 2, 3].map(i => <div key={i} className="h-64 bg-slate-100 rounded-xl animate-pulse" />)}
        </div>
      ) : (
        <div className="space-y-10">
          {groups.length === 0 && standaloneConfigs.length === 0 ? (
            <div className="py-14 px-6 border-2 border-dashed border-slate-200 rounded-2xl text-center">
              <CalendarClock size={40} className="mx-auto text-brand-400" />
              <h2 className="mt-3 text-xl font-bold text-slate-700">No schedules yet</h2>
              <p className="mt-1 text-slate-500 max-w-xl mx-auto">Pick namespaces and the hours they should run. Outside those hours CostDeck scales their workloads to zero and restores them afterwards.</p>
              <div className="mt-6 grid gap-3 sm:grid-cols-3 max-w-3xl mx-auto text-left">
                <div className="p-4 rounded-xl bg-slate-50"><MoonStar size={18} className="text-brand-500" /><div className="mt-2 text-sm font-bold text-slate-700">Off at night and at weekends</div><div className="text-xs text-slate-500">Weekdays 08:00–20:00 keeps a dev environment down 64% of the week.</div></div>
                <div className="p-4 rounded-xl bg-slate-50"><Link2 size={18} className="text-brand-500" /><div className="mt-2 text-sm font-bold text-slate-700">Shared platforms on demand</div><div className="text-xs text-slate-500">A platform can start only when an environment that needs it is running.</div></div>
                <div className="p-4 rounded-xl bg-slate-50"><Hand size={18} className="text-amber-500" /><div className="mt-2 text-sm font-bold text-slate-700">Override any time</div><div className="text-xs text-slate-500">Start or stop now with one click; the schedule takes over again later.</div></div>
              </div>
              {can('admin') && (
                <button onClick={() => setWizard({ kind: 'group' })} className="mt-6 px-5 py-2.5 rounded-xl bg-brand-600 text-white font-bold hover:bg-brand-700">
                  Create your first schedule
                </button>
              )}
            </div>
          ) : (
            <>
              {categories.map(category => (
                <section key={category}>
                  {categories.length > 1 && (
                    <SectionTitle>{category}</SectionTitle>
                  )}
                  <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6">
                    {groups.filter(g => (g.spec.category || 'General') === category).map(groupCard)}
                  </div>
                </section>
              ))}

              {standaloneConfigs.length > 0 && (
                <section>
                  <SectionTitle>Single-namespace schedules</SectionTitle>
                  <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6">
                    {standaloneConfigs.map(config => (
                      <ScheduleCard
                        key={config.metadata.name}
                        name={config.spec.targetNamespace}
                        namespaces={[config.spec.targetNamespace]}
                        spec={config.spec}
                        generation={config.metadata.generation}
                        status={config.status}
                        busy={isScalingMap[`config-${config.metadata.name}`]}
                        canOperate={can('operator')}
                        canAdmin={can('admin')}
                        onOpen={() => setDetails({ kind: 'config', name: config.metadata.name, tab: 'overview' })}
                        onStart={() => prompt('config', config.metadata.name, true, config.spec)}
                        onStop={() => prompt('config', config.metadata.name, false, config.spec)}
                        onResume={() => handleManualScale('config', config.metadata.name, null)}
                        onEdit={() => setWizard({ kind: 'config', existing: config })}
                      />
                    ))}
                  </div>
                </section>
              )}
            </>
          )}

          {unscheduled.length > 0 && (
            <section>
              <SectionTitle aside={<span className="text-xs text-slate-400">always on until you give them a schedule</span>}>Not scheduled yet</SectionTitle>
              <div className="flex flex-wrap gap-2">
                {unscheduled.map(ns => (
                  <div key={ns} className="flex items-center rounded-xl border border-slate-200 bg-white overflow-hidden">
                    <button onClick={() => onSelectNamespace(ns)} className="px-3 py-1.5 text-sm font-semibold text-slate-600 hover:text-brand-600">{ns}</button>
                    {can('admin') && (
                      <button onClick={() => setWizard({ kind: 'group', initialNamespaces: [ns] })} title={`Schedule ${ns}`}
                        className="px-2 py-1.5 border-l border-slate-200 text-slate-400 hover:bg-brand-50 hover:text-brand-600">
                        <Plus size={14} />
                      </button>
                    )}
                  </div>
                ))}
              </div>
            </section>
          )}

          <section>
            <button onClick={() => setCloudExpanded(!cloudExpanded)} className="w-full text-left">
              <SectionTitle aside={
                <span className="inline-flex items-center gap-2 text-xs text-slate-400">
                  {Object.values(discoveredResources).flat().length} discovered · add them to a schedule under Start order
                  {cloudExpanded ? <ChevronUp size={16} /> : <ChevronDown size={16} />}
                </span>
              }>
                <span className="inline-flex items-center gap-2"><Cloud size={16} className="text-amber-500" /> Cloud resources</span>
              </SectionTitle>
            </button>
            {cloudExpanded && (
              Object.keys(discoveredResources).length > 0 ? (
                <div className="space-y-4">
                  <div className="flex p-1 bg-slate-100/80 rounded-xl w-fit">
                    {Object.keys(discoveredResources).map(tab => (
                      <button key={tab} onClick={() => setActiveDiscoveryTab(tab)}
                        className={`flex items-center gap-2 px-5 py-1.5 rounded-xl text-sm font-bold transition-all ${discoveryTab === tab ? 'bg-white text-brand-900 shadow-sm' : 'text-slate-500 hover:text-slate-700'}`}>
                        {tab === 'Databases' ? <Database size={16} /> : <Layers size={16} />}
                        {tab} <span className="text-[10px] text-slate-400">{discoveredResources[tab].length}</span>
                      </button>
                    ))}
                  </div>
                  {discoveryTab && (
                    <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
                      {discoveredResources[discoveryTab].map((item, i) => <ResourceCard key={i} item={item} />)}
                    </div>
                  )}
                </div>
              ) : (
                <p className="text-sm text-slate-400">Nothing discovered. Connect a cloud account under Settings to stop databases and instances along with your schedules.</p>
              )
            )}
          </section>
        </div>
      )}

      {details && (() => {
        const group = details.kind === 'group' ? groups.find(g => g.metadata.name === details.name) : undefined;
        const config = details.kind === 'config' ? policies.find(c => c.metadata.name === details.name) : undefined;
        if (!group && !config) return null;
        const kind = details.kind;
        const name = details.name;
        const spec = (group || config)!.spec;
        return (
          <ScheduleDetails
            target={group ? { kind: 'group', group } : { kind: 'config', config: config! }}
            groups={groups}
            tab={details.tab}
            onTab={tab => setDetails({ ...details, tab })}
            busy={isScalingMap[`${kind}-${name}`]}
            canOperate={can('operator')}
            canAdmin={can('admin')}
            onClose={() => setDetails(null)}
            onStart={() => prompt(kind, name, true, spec)}
            onStop={() => prompt(kind, name, false, spec)}
            onResume={() => handleManualScale(kind, name, null)}
            onEdit={tab => {
              // One dialog at a time: editing replaces the details view.
              setDetails(null);
              if (group) setWizard({ kind: 'group', existing: group, initialTab: tab });
              else setWizard({ kind: 'config', existing: config! });
            }}
            onDelete={group ? () => deleteGroup(name) : undefined}
            onRules={config ? () => setOrdering({ name, spec: config.spec }) : undefined}
            onSelectNamespace={ns => { setDetails(null); onSelectNamespace(ns); }}
          />
        );
      })()}

      {wizard && (
        <ScheduleWizard
          {...wizard}
          discovered={Object.values(discoveredResources).flat()}
          namespaces={namespaces}
          groups={groups}
          onClose={() => setWizard(null)}
          onSaved={() => { setWizard(null); fetchData(); }}
        />
      )}

      {overridePrompt && (
        <OverrideDialog
          name={overridePrompt.name}
          kind={overridePrompt.type}
          active={overridePrompt.active}
          hasSchedule={overridePrompt.hasSchedule}
          onCancel={() => setOverridePrompt(null)}
          onConfirm={(until) => {
            const { type, name, active } = overridePrompt;
            setOverridePrompt(null);
            handleManualScale(type, name, active, until);
          }}
        />
      )}

      {ordering && (
        <WorkloadRulesDialog
          namespace={policies.find(c => c.metadata.name === ordering.name)?.spec.targetNamespace || ordering.name}
          spec={ordering.spec}
          onClose={() => setOrdering(null)}
          onSave={handleSaveOrder}
        />
      )}

    </div>
  );
};

export default ScalingPage;
