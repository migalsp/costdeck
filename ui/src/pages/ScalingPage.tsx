import { useState, useEffect } from 'react'
import { 
  Plus, 
  Clock, 
  Power,
  Play,
  Square,
  Settings2,
  LayoutGrid,
  Layers,
  CalendarClock,
  Cloud,
  Database,
  ChevronUp,
  ChevronDown
} from 'lucide-react'
import ScalingConfigModal from '../components/ScalingConfigModal'
import ScalingPipelineModal from '../components/ScalingPipelineModal'

interface ScalingSchedule {
  days: number[];
  startTime: string;
  endTime: string;
  timezone?: string;
}

interface ScalingGroup {
  metadata: {
    name: string;
  };
  spec: {
    category: string;
    namespaces: string[];
    active?: boolean;
    schedules?: ScalingSchedule[];
    sequence?: string[];
    exclusions?: string[];
  };
  status?: {
    phase: string;
    lastAction: string;
    managedCount: number;
    namespacesReady?: number;
    namespacesTotal?: number;
  };
}

interface ScalingConfig {
  metadata: {
    name: string;
  };
  spec: {
    targetNamespace: string;
    active?: boolean;
    schedules?: ScalingSchedule[];
    sequence?: string[];
    exclusions?: string[];
  };
  status?: {
    phase: string;
    lastAction: string;
  };
}

const ScalingPage: React.FC<{ onSelectNamespace: (ns: string) => void }> = ({ onSelectNamespace }) => {
  const [groups, setGroups] = useState<ScalingGroup[]>([]);
  const [policies, setPolicies] = useState<ScalingConfig[]>([]);
  const [namespaces, setNamespaces] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);
  
  // Modal States
  const [editingGroup, setEditingGroup] = useState<ScalingGroup | null>(null);
  const [viewingPipelineGroupName, setViewingPipelineGroupName] = useState<string | null>(null);
  const [isAddingGroup, setIsAddingGroup] = useState(false);
  const [editingPolicy, setEditingPolicy] = useState<{ mode: 'schedule' | 'sequence' | 'group', name: string, spec: any } | null>(null);
  const [newGroupName, setNewGroupName] = useState('');
  const [newGroupCategory, setNewGroupCategory] = useState('Solution');
  const [selectedNS, setSelectedNS] = useState<string[]>([]);
  const [deletingGroupName, setDeletingGroupName] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [isScalingMap, setIsScalingMap] = useState<Record<string, boolean>>({});

  // Section Collapse State
  const [namespacesCollapsed, setNamespacesCollapsed] = useState(false);
  const [databasesCollapsed, setDatabasesCollapsed] = useState(false);

  // External Targets
  const [availableAuroraClusters, setAvailableAuroraClusters] = useState<any[]>([]);

  useEffect(() => {
    fetch('/api/discovery/aws/aurora')
      .then(res => res.json())
      .then(data => setAvailableAuroraClusters(data || []))
      .catch(err => console.error("Failed to fetch Aurora clusters", err));
  }, []);

  useEffect(() => {
    fetchData();
    // Auto-refresh every 10 seconds for real-time status (silent)
    const interval = setInterval(() => fetchData(true), 10000);
    return () => clearInterval(interval);
  }, []);

  const fetchData = async (silent = false) => {
    if (!silent) setLoading(true);
    try {
      const [groupsRes, policiesRes, nsRes] = await Promise.all([
        fetch('/api/scaling/groups'),
        fetch('/api/scaling/configs'),
        fetch('/api/namespaces')
      ]);
      const groupsData = await groupsRes.json();
      const policiesData = await policiesRes.json();
      const nsData = await nsRes.json();
      
      setGroups((groupsData || []).sort((a: any, b: any) => a.metadata.name.localeCompare(b.metadata.name)));
      setPolicies((policiesData || []).sort((a: any, b: any) => a.spec.targetNamespace.localeCompare(b.spec.targetNamespace)));
      // deduplicate and sort namespaces
      const uniqueNamespaces = Array.from(new Set(nsData.map((n: any) => n.spec.targetNamespace || n.metadata.name))) as string[];
      uniqueNamespaces.sort((a, b) => a.localeCompare(b));
      setNamespaces(uniqueNamespaces);
    } catch (err) {
      console.error("Failed to fetch scaling data", err);
    } finally {
      setLoading(false);
    }
  };

  const handleUpsertGroup = async () => {
    if (!newGroupName) return;
    
    // Auto-sanitize for the user: lowercase, spaces/invalid replaced by hyphens, multiple hyphens collapsed
    const sanitizedGroupUrlName = newGroupName
      .toLowerCase()
      .trim()
      .replace(/[^a-z0-9-]/g, '-')
      .replace(/-+/g, '-')
      .replace(/^-|-$/g, '');
    
    // Validate Kubernetes naming convention (RFC 1123) after sanitization
    const k8sNameRegex = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;
    if (!k8sNameRegex.test(sanitizedGroupUrlName)) {
      setError('Invalid group name format. Must start and end with an alphanumeric character.');
      return;
    }

    if (selectedNS.length === 0) {
      setError('Please select at least one namespace for the group.');
      return;
    }

    setError(null);
    try {
      const method = editingGroup ? 'PUT' : 'POST';
      const endpoint = editingGroup ? `/api/scaling/groups/${editingGroup.metadata.name}` : '/api/scaling/groups';
      
      const res = await fetch(endpoint, {
        method: method,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          metadata: { name: sanitizedGroupUrlName },
          spec: { 
            ...(editingGroup?.spec || {}),
            category: newGroupCategory, 
            namespaces: selectedNS,
            active: editingGroup ? editingGroup.spec.active : true 
          }
        })
      });
      if (!res.ok) {
        const errText = await res.text();
        setError(`Failed to save group: ${errText}`);
        return;
      }
      setIsAddingGroup(false);
      setEditingGroup(null);
      setNewGroupName('');
      setSelectedNS([]);
      fetchData();
    } catch (err: any) {
      console.error("Failed to save group", err);
      setError(`Failed to save group: ${err.message}`);
    }
  };

  const handleManualScale = async (type: 'group' | 'config', name: string, active: boolean) => {
    const key = `${type}-${name}`;
    if (isScalingMap[key]) return;
    
    setIsScalingMap(prev => ({ ...prev, [key]: true }));

    // Optimistic UI update
    if (type === 'group') {
      setGroups(prev => prev.map((g: any) => g.metadata.name === name ? {
        ...g,
        status: { ...(g.status || {}), phase: 'Scaling...', lastAction: g.status?.lastAction || '' }
      } : g));
    } else {
      setPolicies(prev => prev.map(p => p.metadata.name === name ? {
        ...p,
        status: { ...(p.status || {}), phase: 'Scaling...', lastAction: p.status?.lastAction || '' }
      } : p));
    }

    const endpoint = type === 'group' ? `/api/scaling/groups/${name}/manual` : `/api/scaling/configs/${name}/manual`;
    try {
      await fetch(endpoint, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ active })
      });
      fetchData(true);
      
      let attempts = 0;
      const fastPoll = setInterval(() => {
        fetchData(true);
        attempts++;
        if (attempts > 10) {
          clearInterval(fastPoll);
          setIsScalingMap(prev => ({ ...prev, [key]: false }));
        }
      }, 2000);
      
      setTimeout(() => setIsScalingMap(prev => ({ ...prev, [key]: false })), 22000);

    } catch (err) {
      console.error("Failed to trigger scaling", err);
      setIsScalingMap(prev => ({ ...prev, [key]: false }));
      fetchData(true); // Revert optimistic UI on failure
    }
  };

  const handleUpdateConfig = async (updatedSpec: any) => {
    if (!editingPolicy) return;
    // Determine endpoint: check if name matches a group
    const isGroup = groups.some(g => g.metadata.name === editingPolicy.name);
    const endpoint = isGroup ? `/api/scaling/groups/${editingPolicy.name}` : `/api/scaling/configs/${editingPolicy.name}`;
    
    // When saving schedule, clear manual override so schedule takes control
    if (editingPolicy.mode === 'schedule') {
      delete updatedSpec.active;
    }
    
    try {
      const res = await fetch(endpoint, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ 
          metadata: { name: editingPolicy.name },
          spec: updatedSpec 
        })
      });

      if (!res.ok) {
        const text = await res.text();
        throw new Error(text || res.statusText);
      }

      setEditingPolicy(null);
      setError(null);
      fetchData(true);
    } catch (err: any) {
      console.error("Failed to update config", err);
      setError(`Failed to update configuration: ${err.message}`);
    }
  };

  const handleDeleteGroup = (e: React.MouseEvent, name: string) => {
    e.stopPropagation();
    e.preventDefault();
    setDeletingGroupName(name);
  };

  const confirmDeleteGroup = async () => {
    if (!deletingGroupName) return;
    try {
      await fetch(`/api/scaling/groups/${deletingGroupName}`, { method: 'DELETE' });
      fetchData();
    } catch (err) {
      console.error("Failed to delete group", err);
    } finally {
      setDeletingGroupName(null);
    }
  };

  const handleCreateIndividualConfig = async (ns: string) => {
    const groupName = getGroupForNamespace(ns);
    if (groupName && !window.confirm(`Namespace "${ns}" is currently managed by the group "${groupName}". Individual configuration will be overridden by the group. Do you still want to create it?`)) {
      return;
    }
    
    try {
      await fetch('/api/scaling/configs', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          metadata: { name: `config-${ns}` },
          spec: { targetNamespace: ns, active: true }
        })
      });
      fetchData();
    } catch (err) {
      console.error("Failed to create config", err);
    }
  };

  const toggleNS = (ns: string) => {
    setSelectedNS(prev => 
      prev.includes(ns) ? prev.filter(n => n !== ns) : [...prev, ns]
    );
  };

  const GroupOfNamespaces = ({ group }: { group: ScalingGroup }) => {
    const phase = getPhaseColor(group.status?.phase);
    return (
    <div className="bg-white rounded-2xl border border-slate-200 shadow-sm hover:shadow-md transition-all p-6">
      {/* Header Row */}
      <div className="flex items-center justify-between mb-4">
        <div className="flex items-center gap-3">
          <div className={`w-10 h-10 rounded-xl flex items-center justify-center shrink-0 ${
            group.spec.category === 'Solution' ? 'bg-indigo-50 text-indigo-500' : 'bg-amber-50 text-amber-500'
          }`}>
            {group.spec.category === 'Solution' ? <LayoutGrid size={20} /> : <Layers size={20} />}
          </div>
          <div>
            <h3 className="font-bold text-slate-800 text-base">{group.metadata.name}</h3>
            <span className="text-[10px] uppercase font-bold text-slate-400 tracking-wider">
              {group.spec.category} Group
            </span>
          </div>
        </div>
        <div className="flex items-center gap-1">
          <button onClick={(e) => { e.stopPropagation(); handleManualScale('group', group.metadata.name, true); }}
            disabled={isScalingMap[`group-${group.metadata.name}`]}
            className={`p-1.5 rounded-lg transition-colors ${group.status?.phase === 'ScaledUp' ? 'bg-emerald-50 text-emerald-500' : 'bg-slate-50 text-slate-400 hover:bg-slate-100'} ${isScalingMap[`group-${group.metadata.name}`] ? 'opacity-50 cursor-not-allowed' : ''}`}
            title="Scale Up">
              {isScalingMap[`group-${group.metadata.name}`] && group.status?.phase === 'Scaling...' ? (
                <div className="w-3.5 h-3.5 border-2 border-slate-300 border-t-emerald-500 rounded-full animate-spin"></div>
              ) : (
                <Play size={14} fill={group.status?.phase === 'ScaledUp' ? "currentColor" : "none"} />
              )}
          </button>
          <button onClick={(e) => { e.stopPropagation(); handleManualScale('group', group.metadata.name, false); }}
            disabled={isScalingMap[`group-${group.metadata.name}`]}
            className={`p-1.5 rounded-lg transition-colors ${group.status?.phase === 'ScaledDown' ? 'bg-rose-50 text-rose-500' : 'bg-slate-50 text-slate-400 hover:bg-slate-100'} ${isScalingMap[`group-${group.metadata.name}`] ? 'opacity-50 cursor-not-allowed' : ''}`}
            title="Scale Down">
              {isScalingMap[`group-${group.metadata.name}`] && group.status?.phase === 'Scaling...' ? (
                <div className="w-3.5 h-3.5 border-2 border-slate-300 border-t-rose-500 rounded-full animate-spin"></div>
              ) : (
                <Square size={14} fill={group.status?.phase === 'ScaledDown' ? "currentColor" : "none"} />
              )}
          </button>
          <button onClick={(e) => { e.stopPropagation(); setEditingPolicy({ mode: 'schedule', name: group.metadata.name, spec: { ...group.spec } }); }}
          className="p-1.5 text-slate-400 hover:bg-indigo-50 hover:text-indigo-500 rounded-lg transition-colors" title="Availability Schedule">
          <CalendarClock size={14} /></button>
        <button onClick={(e) => { e.stopPropagation(); setEditingPolicy({ mode: 'sequence', name: group.metadata.name, spec: { ...group.spec } }); }}
          className="p-1.5 text-slate-400 hover:bg-indigo-50 hover:text-indigo-500 rounded-lg transition-colors" title="Namespace Scaling Sequence">
          <Settings2 size={14} /></button>
        <button onClick={(e) => { e.stopPropagation(); setEditingGroup(group); setNewGroupName(group.metadata.name); setNewGroupCategory(group.spec.category); setSelectedNS(group.spec.namespaces); setIsAddingGroup(true); }}
          className="p-1.5 text-slate-400 hover:bg-indigo-50 hover:text-indigo-500 rounded-lg transition-colors" title="Manage Group Namespaces">
          <Layers size={14} /></button>
          <button onClick={(e) => handleDeleteGroup(e, group.metadata.name)}
            className="p-1.5 text-slate-300 hover:text-rose-500 hover:bg-rose-50 rounded-lg transition-colors" title="Delete Group">
            <Plus size={14} className="rotate-45" /></button>
        </div>
      </div>

      {/* Namespaces */}
      <div className="mb-3">
        <div className="flex items-center justify-between text-[11px] font-bold text-slate-400 uppercase tracking-wider mb-2">
          <span>Namespaces</span>
          <span className="bg-slate-100 px-2 py-0.5 rounded-full text-slate-600">{group.spec.namespaces.length}</span>
        </div>
        <div className="flex flex-wrap gap-1.5">
          {group.spec.namespaces.map(ns => (
            <span key={ns} className="px-2.5 py-1 bg-slate-50 text-slate-600 rounded-lg text-[10px] font-medium border border-slate-200">{ns}</span>
          ))}
        </div>
      </div>

      {/* Progress Bar */}
      <div 
        className="mb-3 px-3 py-2 -mx-3 rounded-xl hover:bg-slate-50 transition-colors cursor-pointer group/pb relative"
        title="View Execution Pipeline & Logs"
        onClick={() => setViewingPipelineGroupName(group.metadata.name)}
      >
        <div className="flex items-center justify-between text-[10px] font-bold text-slate-400 uppercase tracking-wider mb-2">
          <span className="group-hover/pb:text-indigo-500 transition-colors">Scaling Progress</span>
          <span className="text-slate-500">{group.status?.namespacesReady || 0} / {group.status?.namespacesTotal || group.spec.namespaces.length || 0} NS</span>
        </div>
        <div className="h-1.5 w-full bg-slate-100 rounded-full overflow-hidden relative">
          <div 
            className="absolute top-0 left-0 h-full bg-indigo-500 rounded-full transition-all duration-500 ease-out"
            style={{ 
              width: `${Math.min(100, Math.max(0, ((group.status?.namespacesReady || 0) / (group.status?.namespacesTotal || group.spec.namespaces.length || 1)) * 100))}%` 
            }}
          />
        </div>
      </div>

      {/* Footer */}
      <div className="pt-3 border-t border-slate-100 flex items-center justify-between">
        <div className="flex items-center gap-2 text-[11px] text-slate-400 font-medium">
          <Clock size={12} />
          <span>{group.status?.lastAction ? new Date(group.status.lastAction).toLocaleTimeString() : 'N/A'}</span>
        </div>
        <div className="flex items-center gap-1.5">
          <div className={`w-2 h-2 rounded-full ${phase.dot}`} />
          <span className={`text-[11px] font-bold uppercase tracking-tight ${phase.text}`}>{group.status?.phase || 'Idle'}</span>
        </div>
      </div>
    </div>
    );
  };

  const getGroupForNamespace = (ns: string): string | null => {
    const group = groups.find(g => g.spec.namespaces.includes(ns));
    return group ? group.metadata.name : null;
  };

  const getPhaseColor = (phase?: string) => {
    switch (phase) {
      case 'ScaledUp': return { dot: 'bg-emerald-500', text: 'text-emerald-600' };
      case 'ScalingUp': return { dot: 'bg-emerald-400 animate-pulse', text: 'text-emerald-500' };
      case 'ScaledDown': return { dot: 'bg-slate-300', text: 'text-slate-500' };
      case 'ScalingDown': return { dot: 'bg-amber-400 animate-pulse', text: 'text-amber-500' };
      case 'PartlyScaled': return { dot: 'bg-amber-500', text: 'text-amber-600' };
      case 'OverriddenByGroup': return { dot: 'bg-indigo-300', text: 'text-indigo-500' };
      case 'Scaling...': return { dot: 'bg-blue-400 animate-pulse', text: 'text-blue-500' };
      default: return { dot: 'bg-slate-300', text: 'text-slate-400' };
    }
  };

  const ConfigCard = ({ config }: { config: ScalingConfig }) => {
    const managedBy = getGroupForNamespace(config.spec.targetNamespace);
    const phase = getPhaseColor(config.status?.phase);
    
    // Convert CamelCase to spaced strings e.g., ScaledUp -> Scaled Up
    const parsePhase = (phaseStr?: string) => {
      if (!phaseStr) return 'Idle';
      if (phaseStr === 'OverriddenByGroup') return 'Overridden';
      return phaseStr.replace(/([A-Z])/g, ' $1').trim();
    };

    return (
    <div 
      className="bg-white rounded-2xl border border-slate-200 shadow-sm hover:shadow-md transition-all cursor-pointer min-h-[88px] flex items-center px-4 py-3 gap-3"
      onClick={() => onSelectNamespace(config.spec.targetNamespace)}
    >
      <div className="w-10 h-10 rounded-xl bg-slate-50 text-slate-500 flex items-center justify-center shrink-0">
        <Layers size={20} />
      </div>
      <div className="flex-1 min-w-0 flex flex-col justify-center gap-1.5">
        <h4 className="font-bold text-slate-800 text-[15px] leading-tight break-words" title={config.spec.targetNamespace}>
          {config.spec.targetNamespace}
        </h4>
        <div className="flex items-center gap-1.5">
          <div className={`w-2 h-2 rounded-full shrink-0 ${phase.dot}`} />
          <span className={`text-[12px] font-bold tracking-wide ${phase.text}`}>
            {parsePhase(config.status?.phase)}
          </span>
        </div>
        <span className={`text-[12px] font-bold leading-none block break-words ${managedBy ? 'text-slate-800' : 'text-slate-400'}`}>
          {managedBy ? `Managed by: ${managedBy}` : 'Self-managed'}
        </span>
      </div>
      
      <div className="flex flex-col gap-1 items-end shrink-0" onClick={(e) => e.stopPropagation()}>
        <div className="flex gap-1">
          <button onClick={() => handleManualScale('config', config.metadata.name, true)}
            disabled={isScalingMap[`config-${config.metadata.name}`]}
            className={`p-1.5 rounded-lg transition-colors ${config.status?.phase === 'ScaledUp' ? 'bg-emerald-50 text-emerald-500' : 'bg-slate-50 text-slate-400 hover:bg-slate-100'} ${isScalingMap[`config-${config.metadata.name}`] ? 'opacity-50 cursor-not-allowed' : ''}`}
            title="Scale Up">
              {isScalingMap[`config-${config.metadata.name}`] && config.status?.phase === 'Scaling...' ? (
                <div className="w-3.5 h-3.5 border-2 border-slate-300 border-t-emerald-500 rounded-full animate-spin"></div>
              ) : (
                <Play size={14} fill={config.status?.phase === 'ScaledUp' ? "currentColor" : "none"} />
              )}
          </button>
          <button onClick={() => handleManualScale('config', config.metadata.name, false)}
            disabled={isScalingMap[`config-${config.metadata.name}`]}
            className={`p-1.5 rounded-lg transition-colors ${config.status?.phase === 'ScaledDown' ? 'bg-rose-50 text-rose-500' : 'bg-slate-50 text-slate-400 hover:bg-slate-100'} ${isScalingMap[`config-${config.metadata.name}`] ? 'opacity-50 cursor-not-allowed' : ''}`}
            title="Scale Down">
              {isScalingMap[`config-${config.metadata.name}`] && config.status?.phase === 'Scaling...' ? (
                <div className="w-3.5 h-3.5 border-2 border-slate-300 border-t-rose-500 rounded-full animate-spin"></div>
              ) : (
                <Square size={14} fill={config.status?.phase === 'ScaledDown' ? "currentColor" : "none"} />
              )}
          </button>
        </div>
        <div className="flex gap-1">
          <button onClick={() => setEditingPolicy({ mode: 'schedule', name: config.metadata.name, spec: config.spec })}
            className="p-1.5 text-slate-400 hover:bg-indigo-50 hover:text-indigo-500 rounded-lg transition-colors" title="Schedule">
            <CalendarClock size={14} /></button>
          <button onClick={() => setEditingPolicy({ mode: 'sequence', name: config.metadata.name, spec: config.spec })}
            className="p-1.5 text-slate-400 hover:bg-slate-100 rounded-lg transition-colors" title="Sequence & Exclusions">
            <Settings2 size={14} /></button>
        </div>
      </div>
    </div>
    );
  };

  return (
    <div className="p-8 max-w-7xl mx-auto min-h-full">
      <div className="flex justify-between items-center mb-8">
        <div>
          <h1 className="text-3xl font-black text-slate-800 tracking-tight">Workload Scaling</h1>
          <p className="text-slate-500 mt-1">Orchestrate infrastructure availability by schedule or on-demand.</p>
        </div>
        <div className="flex gap-3">
          <button 
            onClick={() => setIsAddingGroup(true)}
            className="flex items-center gap-2 bg-indigo-600 hover:bg-indigo-700 text-white px-5 py-2.5 rounded-xl font-bold shadow-lg shadow-indigo-600/20 transition-all border border-indigo-400/20"
          >
            <Plus size={20} />
            New Group
          </button>
        </div>
      </div>

      {error && (
        <div className="mb-6 p-4 bg-rose-50 border border-rose-100 rounded-2xl flex items-center gap-3 text-rose-600 animate-in slide-in-from-top duration-300">
          <Square className="rotate-45 shrink-0" size={20} />
          <div className="flex-1 text-sm font-bold">{error}</div>
          <button onClick={() => setError(null)} className="p-1 hover:bg-rose-100 rounded-full transition-colors text-rose-400">
            <Plus size={16} className="rotate-45" />
          </button>
        </div>
      )}

      {loading && groups.length === 0 ? (
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6">
          {[1, 2, 3].map(i => <div key={i} className="h-48 bg-slate-100 rounded-2xl animate-pulse" />)}
        </div>
      ) : (
        <div className="space-y-12">
          {/* Dynamic Category Sections */}
          {Array.from(new Set(groups.map(g => g.spec.category))).sort().map(category => (
            <section key={category}>
              <div className="flex items-center gap-3 mb-6">
                <div className={`w-8 h-8 rounded-lg flex items-center justify-center ${
                  category === 'Solution' ? 'bg-indigo-500/10 text-indigo-500' : 
                  category === 'Platform' ? 'bg-amber-500/10 text-amber-500' : 
                  'bg-emerald-500/10 text-emerald-500'
                }`}>
                  {category === 'Solution' ? <LayoutGrid size={18} /> : 
                   category === 'Platform' ? <Layers size={18} /> : 
                   <LayoutGrid size={18} />}
                </div>
                <h2 className="text-xl font-bold text-slate-700">Scaling Groups</h2>
                <div className="h-px flex-1 bg-slate-200/60 ml-2" />
              </div>
              <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6">
                {groups.filter(g => g.spec.category === category).map(group => (
                  <GroupOfNamespaces key={group.metadata.name} group={group} />
                ))}
              </div>
            </section>
          ))}

          {groups.length === 0 && (
            <div className="py-24 border-2 border-dashed border-slate-200 rounded-3xl flex flex-col items-center justify-center text-slate-400 gap-3 grayscale opacity-60">
               <LayoutGrid size={48} />
               <p className="font-bold">No scaling groups configured yet.</p>
               <button onClick={() => setIsAddingGroup(true)} className="text-indigo-600 font-bold hover:underline">Create your first group</button>
            </div>
          )}

          {/* Individual Namespaces Section */}
          <section>
            <div className="flex items-center gap-3 mb-6">
              <div className="w-8 h-8 bg-slate-500/10 text-slate-500 rounded-lg flex items-center justify-center">
                <Power size={18} />
              </div>
              <h2 className="text-xl font-bold text-slate-700">Namespaces</h2>
              <div className="h-px flex-1 bg-slate-200/60 ml-2" />
              <button onClick={() => setNamespacesCollapsed(!namespacesCollapsed)} className="p-1.5 text-slate-400 hover:bg-slate-100 rounded-lg transition-colors" title={namespacesCollapsed ? "Expand" : "Collapse"}>
                {namespacesCollapsed ? <ChevronDown size={18} /> : <ChevronUp size={18} />}
              </button>
            </div>
            
            {!namespacesCollapsed && (
              <div className="animate-in slide-in-from-top-2 fade-in duration-200">
                {namespaces.length === 0 ? (
                  <div className="h-32 bg-slate-50 rounded-2xl flex items-center justify-center text-slate-400">Loading namespaces...</div>
                ) : (
                  <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
                    {policies.map(config => (
                      <ConfigCard key={config.metadata.name} config={config} />
                    ))}
                    {namespaces.map(ns => {
                      const managedByGroup = groups.find(g => g.spec.namespaces?.includes(ns));
                      const hasIndividualConfig = policies.some(p => p.spec.targetNamespace === ns);
                      if (hasIndividualConfig) return null; // Handled in ConfigCard

                      return (
                      <div key={ns} className={`bg-white border rounded-2xl p-4 flex items-center justify-between group transition-all cursor-pointer ${managedByGroup ? 'border-indigo-100 hover:border-indigo-300' : 'border-dashed border-slate-200 hover:border-indigo-300'} hover:shadow-sm`}
                        onClick={() => handleCreateIndividualConfig(ns)}
                        title={managedByGroup ? `Click to override sequence/exclusions for ${ns}` : "Click to enable scaling control for this namespace"}>
                        <div className="flex items-center gap-3 min-w-0">
                          <div className={`w-10 h-10 rounded-xl flex items-center justify-center transition-colors shrink-0 ${managedByGroup ? 'bg-indigo-50 text-indigo-500' : 'bg-slate-50 text-slate-300 group-hover:bg-indigo-50 group-hover:text-indigo-500'}`}>
                            <Layers size={20} />
                          </div>
                          <div className="flex flex-col min-w-0">
                            <span className={`font-bold transition-colors whitespace-nowrap overflow-hidden text-ellipsis ${managedByGroup ? 'text-indigo-900' : 'text-slate-400 group-hover:text-slate-700'}`}>{ns}</span>
                            <span className={`text-[11px] font-bold uppercase tracking-wider transition-colors ${managedByGroup ? 'text-indigo-400' : 'text-slate-300 group-hover:text-indigo-400'}`}>
                              {managedByGroup ? `Managed by ${managedByGroup.metadata.name}` : 'Unmanaged'}
                            </span>
                          </div>
                        </div>
                        <div className={`w-8 h-8 rounded-full flex items-center justify-center transition-colors shrink-0 ${managedByGroup ? 'bg-indigo-50 group-hover:bg-indigo-100' : 'bg-slate-50 group-hover:bg-indigo-50'}`}>
                          <Plus size={16} className={`transition-colors ${managedByGroup ? 'text-indigo-400 group-hover:text-indigo-600' : 'text-slate-300 group-hover:text-indigo-500'}`} />
                        </div>
                      </div>
                      );
                    })}
                  </div>
                )}
              </div>
            )}
          </section>

          {/* Public Cloud Managed Services Section */}
          <section>
            <div className="flex items-center gap-3 mb-6">
              <div className="w-8 h-8 bg-amber-500/10 text-amber-500 rounded-lg flex items-center justify-center">
                <Cloud size={18} />
              </div>
              <h2 className="text-xl font-bold text-slate-700">Public Cloud Managed Services</h2>
              <div className="h-px flex-1 bg-slate-200/60 ml-2" />
              <button onClick={() => setDatabasesCollapsed(!databasesCollapsed)} className="p-1.5 text-slate-400 hover:bg-slate-100 rounded-lg transition-colors" title={databasesCollapsed ? "Expand" : "Collapse"}>
                {databasesCollapsed ? <ChevronDown size={18} /> : <ChevronUp size={18} />}
              </button>
            </div>

            {!databasesCollapsed && (
              <div className="mb-8 animate-in slide-in-from-top-2 fade-in duration-200">
                <h3 className="text-sm font-bold text-slate-500 uppercase tracking-wider mb-4">Databases</h3>
                <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
                  {availableAuroraClusters.length === 0 ? (
                    <div className="col-span-full border border-dashed border-slate-200 rounded-2xl p-6 flex flex-col items-center justify-center text-slate-400">
                      <p className="font-medium text-sm">No databases discovered.</p>
                      <p className="text-xs mt-1">Ensure AWS provider is enabled and configured correctly.</p>
                    </div>
                  ) : (
                    availableAuroraClusters.map((cluster, i) => (
                      <div key={i} className="bg-white border border-slate-200 rounded-2xl p-4 flex flex-col group hover:border-amber-300 hover:shadow-sm transition-all cursor-default relative overflow-hidden">
                        {cluster.executeAfter && (
                           <div className="absolute top-0 right-0 bg-indigo-100 text-indigo-700 text-[9px] font-black px-2 py-0.5 rounded-bl-lg uppercase">
                             Managed
                           </div>
                        )}
                        <div className="flex items-center gap-3 min-w-0 mb-3 mt-1">
                          <div className="w-10 h-10 rounded-xl bg-slate-50 flex items-center justify-center shrink-0">
                            <img src="https://upload.wikimedia.org/wikipedia/commons/9/93/Amazon_Web_Services_Logo.svg" alt="AWS" className="w-5 opacity-60 grayscale group-hover:grayscale-0 transition-all" />
                          </div>
                          <div className="flex flex-col min-w-0">
                            <span className="font-bold text-slate-700 text-sm whitespace-nowrap overflow-hidden text-ellipsis">{cluster.identifier}</span>
                            <div className="flex items-center gap-2">
                              <span className="text-[11px] font-bold text-amber-500 uppercase tracking-wider">{cluster.type}</span>
                              {cluster.status && (
                                <span className={`flex items-center gap-1 text-[10px] font-bold uppercase ${cluster.status === 'available' ? 'text-emerald-500' : cluster.status === 'stopped' ? 'text-rose-500' : 'text-amber-500'}`}>
                                  <span className={`w-1.5 h-1.5 rounded-full ${cluster.status === 'available' ? 'bg-emerald-500 animate-pulse' : cluster.status === 'stopped' ? 'bg-rose-500' : 'bg-amber-500 animate-pulse'}`}></span>
                                  {cluster.status}
                                </span>
                              )}
                            </div>
                          </div>
                        </div>
                        <div className="text-xs text-slate-500 font-medium flex items-center gap-1">
                          <Database size={12} className="opacity-50" /> Region: <span className="text-slate-700">{cluster.region}</span>
                        </div>
                      </div>
                    ))
                  )}
                </div>
              </div>
            )}
          </section>
        </div>
      )}

      {isAddingGroup && (
        <div className="fixed inset-0 bg-slate-900/60 backdrop-blur-sm z-50 flex items-center justify-center p-4">
          <div className="bg-white rounded-3xl shadow-2xl w-full max-w-xl border border-white/20 animate-in fade-in zoom-in duration-300">
             <div className="p-8 border-b border-slate-100 flex justify-between items-center bg-slate-50/50 rounded-t-3xl">
               <h2 className="text-2xl font-black text-slate-800 tracking-tight">{editingGroup ? 'Edit Group' : 'Create New Group'}</h2>
               <button onClick={() => { setIsAddingGroup(false); setEditingGroup(null); }} className="p-2 hover:bg-white rounded-full transition-colors text-slate-400">
                 <Plus size={20} className="rotate-45" />
               </button>
             </div>
             <div className="p-8 space-y-6 overflow-y-auto max-h-[70vh]">
                <div>
                  <label className="block text-sm font-bold text-slate-500 uppercase tracking-wider mb-2">Group Name</label>
                  <input type="text" value={newGroupName} onChange={(e) => setNewGroupName(e.target.value)}
                    disabled={!!editingGroup}
                    className={`w-full bg-slate-50 border border-slate-200 rounded-xl px-4 py-3 outline-none focus:ring-2 focus:ring-indigo-500/20 focus:border-indigo-500 transition-all font-medium text-slate-800 ${editingGroup ? 'opacity-50' : ''}`} placeholder="enterprise-dev-env" />
                </div>
                <div>
                  <label className="block text-sm font-bold text-slate-500 uppercase tracking-wider mb-2">Category</label>
                  <div className="relative">
                    <input
                      type="text"
                      value={newGroupCategory}
                      onChange={(e) => setNewGroupCategory(e.target.value)}
                      className="w-full bg-slate-50 border border-slate-200 rounded-xl px-4 py-3 outline-none focus:ring-2 focus:ring-indigo-500/20 focus:border-indigo-500 transition-all font-medium text-slate-800"
                      placeholder="e.g. Solution, Platform, Production..."
                    />
                    <div className="mt-2 flex flex-wrap gap-2">
                      {Array.from(new Set(groups.map(g => g.spec.category))).map(cat => (
                        <button
                          key={cat}
                          onClick={() => setNewGroupCategory(cat)}
                          className={`px-3 py-1 rounded-lg text-[10px] font-bold border transition-all ${newGroupCategory === cat ? 'bg-indigo-600 border-indigo-600 text-white' : 'bg-white border-slate-200 text-slate-400 hover:border-slate-300'}`}
                        >
                          {cat}
                        </button>
                      ))}
                    </div>
                  </div>
                </div>
                <div>
                  <label className="block text-sm font-bold text-slate-500 uppercase tracking-wider mb-4">Select Namespaces</label>
                  <div className="max-h-48 overflow-y-auto pr-2 grid grid-cols-3 gap-2">
                    {namespaces.filter(ns => {
                      // Hide namespaces already in OTHER groups
                      return !groups.some(g => editingGroup ? g.metadata.name !== editingGroup.metadata.name && g.spec.namespaces.includes(ns) : g.spec.namespaces.includes(ns));
                    }).map(ns => (
                      <div key={ns} onClick={() => toggleNS(ns)}
                        className={`flex items-center justify-between p-3 rounded-xl cursor-pointer transition-all border ${selectedNS.includes(ns) ? 'bg-indigo-50/50 border-indigo-200' : 'bg-slate-50 border-transparent hover:border-slate-200'}`}>
                        <span className={`font-semibold text-sm ${selectedNS.includes(ns) ? 'text-indigo-700' : 'text-slate-700'}`}>{ns}</span>
                        <div className={`w-5 h-5 rounded-md border flex items-center justify-center transition-all ${selectedNS.includes(ns) ? 'bg-indigo-500 border-indigo-500' : 'bg-white border-slate-300'}`}>
                          {selectedNS.includes(ns) && <Plus size={14} className="text-white rotate-45" />}
                        </div>
                      </div>
                    ))}
                  </div>
                </div>
             </div>
             <div className="p-8 bg-slate-50/50 border-t border-slate-100 flex gap-4 rounded-b-3xl">
               <button onClick={() => { setIsAddingGroup(false); setEditingGroup(null); }} className="flex-1 px-6 py-3 rounded-xl font-bold text-slate-500 hover:bg-white transition-all">Cancel</button>
               <button onClick={handleUpsertGroup} disabled={!newGroupName || selectedNS.length === 0}
                 className="flex-1 px-6 py-3 rounded-xl bg-indigo-600 text-white font-bold shadow-lg shadow-indigo-600/20 hover:bg-indigo-700 transition-all disabled:opacity-50">Save Group</button>
             </div>
          </div>
        </div>
      )}

      {/* Delete Confirmation Modal */}
      {deletingGroupName && (
        <div className="fixed inset-0 bg-slate-900/60 backdrop-blur-sm z-[200] flex items-center justify-center p-4">
          <div className="bg-white rounded-2xl shadow-2xl w-full max-w-sm p-8 text-center">
            <div className="w-16 h-16 bg-rose-50 rounded-full flex items-center justify-center mx-auto mb-4">
              <Plus size={32} className="rotate-45 text-rose-500" />
            </div>
            <h3 className="text-xl font-black text-slate-800 mb-2">Delete Group</h3>
            <p className="text-slate-500 text-sm mb-6">Are you sure you want to delete <b className="text-slate-800">"{deletingGroupName}"</b>? This action cannot be undone.</p>
            <div className="flex gap-3">
              <button onClick={() => setDeletingGroupName(null)} className="flex-1 px-4 py-3 rounded-xl font-bold text-slate-500 hover:bg-slate-50 transition-all border border-slate-200">Cancel</button>
              <button onClick={confirmDeleteGroup} className="flex-1 px-4 py-3 rounded-xl bg-rose-500 text-white font-bold shadow-lg shadow-rose-500/20 hover:bg-rose-600 transition-all">Delete</button>
            </div>
          </div>
        </div>
      )}

      {editingPolicy && (
        <ScalingConfigModal 
          name={editingPolicy.name}
          mode={editingPolicy.mode}
          spec={editingPolicy.spec} 
          onClose={() => setEditingPolicy(null)} 
          onSave={handleUpdateConfig} 
        />
      )}

      {viewingPipelineGroupName && groups.find(g => g.metadata.name === viewingPipelineGroupName) && (
        <ScalingPipelineModal 
          group={groups.find(g => g.metadata.name === viewingPipelineGroupName)!} 
          onClose={() => setViewingPipelineGroupName(null)} 
        />
      )}
    </div>
  );
};

export default ScalingPage;
