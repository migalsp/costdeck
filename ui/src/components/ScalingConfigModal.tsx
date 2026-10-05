import React, { useState, useEffect } from 'react';
import { Plus, Shield, ChevronUp, ChevronDown } from 'lucide-react';
import type { ExternalTarget, ScalingSpec } from '../lib/types';

interface ScalingConfigModalProps {
  name: string;
  // The schedule itself is edited in ScheduleWizard; this dialog only orders and excludes.
  mode: 'sequence';
  spec: ScalingSpec;
  onClose: () => void;
  onSave: (updatedSpec: ScalingSpec) => void;
}

const ScalingConfigModal: React.FC<ScalingConfigModalProps> = ({ name, mode, spec, onClose, onSave }) => {
  const [editingSpec, setEditingSpec] = React.useState({ ...spec, externalTargets: spec.externalTargets || [] });

  // External Targets State
  const [availableAuroraClusters, setAvailableAuroraClusters] = useState<ExternalTarget[]>([]);
  // Only the sequence editor offers external targets, so only it starts out loading them.
  const [loadingAurora, setLoadingAurora] = useState(mode === 'sequence');
  const [showExternalDropdownForStage, setShowExternalDropdownForStage] = useState<number | null>(null);

  useEffect(() => {
    if (mode === 'sequence') {
      // Fetch external targets (just AWS Aurora for now)
      fetch('/api/discovery/aws/aurora')
        .then(res => res.json())
        .then(data => setAvailableAuroraClusters(data || []))
        .catch(err => console.error("Failed to fetch Aurora clusters", err))
        .finally(() => setLoadingAurora(false));
    }
  }, [mode]);

  const isGroup = !!spec.namespaces;
  const title = isGroup ? 'Start order & cloud resources' : 'Workload rules';
  const subtitle = isGroup
    ? 'Namespaces start stage by stage and stop in reverse order'
    : 'Choose the order workloads start in, and which ones never scale down';

  return (
    <div className="fixed inset-0 bg-slate-900/60 backdrop-blur-sm z-[100] flex items-center justify-center p-4">
      <div className="bg-white rounded-2xl shadow-2xl w-full max-w-2xl overflow-hidden border border-white/20 animate-in fade-in zoom-in duration-300">
        <div className="p-8 border-b border-slate-100 flex justify-between items-center bg-slate-50/50">
          <div>
            <h2 className="text-2xl font-bold text-slate-800 tracking-tight">{title}</h2>
            <p className="text-sm text-slate-500">{subtitle} for <span className="text-brand-600 font-bold">{name}</span></p>
          </div>
          <button onClick={onClose} className="p-2 hover:bg-white rounded-full transition-colors text-slate-400">
            <Plus size={20} className="rotate-45" />
          </button>
        </div>
        <div className="p-8 space-y-8 overflow-y-auto max-h-[70vh]">

          {/* Sequence - shown in 'sequence' mode or 'group' mode */}
          {mode === 'sequence' && (
            <div className="space-y-8">

              <div>
                <label className="block text-sm font-bold text-slate-500 uppercase tracking-wider mb-4 flex items-center justify-between">
                  {editingSpec.namespaces ? 'Namespace Scaling Sequence (Stages)' : 'Resource Scaling Sequence'}
                  <button onClick={() => {
                    const seq = [...(editingSpec.sequence || []), ""];
                    setEditingSpec({ ...editingSpec, sequence: seq });
                  }} className="text-[10px] bg-brand-50 px-3 py-1.5 rounded-lg text-brand-600 font-bold hover:bg-brand-100 transition-colors uppercase tracking-widest">+ Add Stage/Pattern</button>
                </label>
                <div className="space-y-2">
                  {(editingSpec.sequence || []).map((s: string, idx: number) => {
                    const stageItems = s.split(' ').filter(n => n);
                    const stageNamespaces = stageItems.filter(n => !n.startsWith('ext:'));
                    const stageTargets = stageItems.filter(n => n.startsWith('ext:')).map(n => n.slice(4));
                    return (
                      <div key={idx} className="relative p-5 bg-white rounded-xl border-2 border-slate-100 shadow-sm group">
                        <div className="flex items-center gap-4 mb-3">
                          <div className="w-8 h-8 rounded-full bg-brand-50 border border-brand-100 flex items-center justify-center text-sm font-bold text-brand-600 shadow-sm shrink-0">
                            {idx + 1}
                          </div>
                          <div className="flex-1 font-bold text-slate-700 text-sm">Stage {idx + 1}</div>

                          {/* Reorder Controls */}
                          <div className="flex items-center gap-1 opacity-0 group-hover:opacity-100 transition-opacity mr-2">
                            <button
                              disabled={idx === 0}
                              onClick={() => {
                                const seq = [...(editingSpec.sequence || [])];
                                [seq[idx - 1], seq[idx]] = [seq[idx], seq[idx - 1]];
                                setEditingSpec({ ...editingSpec, sequence: seq });
                              }}
                              className="p-1.5 text-slate-400 hover:text-brand-600 hover:bg-brand-50 rounded-lg disabled:opacity-30 disabled:hover:bg-transparent disabled:hover:text-slate-400"
                            >
                              <ChevronUp size={16} />
                            </button>
                            <button
                              disabled={idx === (editingSpec.sequence?.length || 0) - 1}
                              onClick={() => {
                                const seq = [...(editingSpec.sequence || [])];
                                [seq[idx + 1], seq[idx]] = [seq[idx], seq[idx + 1]];
                                setEditingSpec({ ...editingSpec, sequence: seq });
                              }}
                              className="p-1.5 text-slate-400 hover:text-brand-600 hover:bg-brand-50 rounded-lg disabled:opacity-30 disabled:hover:bg-transparent disabled:hover:text-slate-400"
                            >
                              <ChevronDown size={16} />
                            </button>
                          </div>

                          <button onClick={() => {
                            const seq = editingSpec.sequence?.filter((_, i) => i !== idx);
                            // Also remove any external targets dependent on this stage
                            const remainingTargets = (editingSpec.externalTargets || []).filter(t => !stageTargets.includes(t.identifier));
                            setEditingSpec({ ...editingSpec, sequence: seq, externalTargets: remainingTargets });
                          }} className="text-slate-300 hover:text-rose-500 opacity-0 group-hover:opacity-100 transition-opacity p-1.5 rounded-lg hover:bg-rose-50" title="Remove Stage">
                            <Plus size={18} className="rotate-45" />
                          </button>
                        </div>

                        {editingSpec.namespaces ? (
                          <div className="flex flex-wrap gap-2">
                            {editingSpec.namespaces.filter((ns: string) => {
                              const isSelected = stageNamespaces.includes(ns);
                              const isSelectedInOtherStage = editingSpec.sequence?.some((s: string, otherIdx: number) =>
                                otherIdx !== idx && s.split(' ').includes(ns)
                              );
                              return isSelected || !isSelectedInOtherStage;
                            }).map((ns: string) => {
                              const isSelected = stageNamespaces.includes(ns);
                              return (
                                <button key={ns} onClick={() => {
                                  const newStageNs = isSelected ? stageNamespaces.filter(n => n !== ns) : [...stageNamespaces, ns];
                                  const seq = [...(editingSpec.sequence || [])];
                                  seq[idx] = [...newStageNs, ...stageTargets.map(t => `ext:${t}`)].join(' ');
                                  setEditingSpec({ ...editingSpec, sequence: seq });
                                }} className={`px-2 py-1.5 text-xs rounded-xl border-2 font-bold transition-all ${isSelected ? 'bg-brand-600 text-white border-brand-600 shadow-sm' : 'bg-slate-50 text-slate-500 border-slate-200 hover:border-slate-300 hover:bg-white'}`}>
                                  {ns}
                                </button>
                              )
                            })}
                          </div>
                        ) : (
                          <input type="text" className="bg-slate-50 border border-slate-200 rounded-xl outline-none text-sm font-bold text-slate-700 w-full px-4 py-3 focus:border-brand-400 focus:bg-white transition-colors" value={s}
                            placeholder="e.g. auth-service or api-*"
                            onChange={(e) => {
                              const seq = [...(editingSpec.sequence || [])];
                              seq[idx] = e.target.value;
                              setEditingSpec({ ...editingSpec, sequence: seq });
                            }}
                            onKeyDown={(e) => {
                              if (e.key === 'Enter') {
                                e.preventDefault();
                                const seq = [...(editingSpec.sequence || []), ''];
                                setEditingSpec({ ...editingSpec, sequence: seq });
                              }
                            }} />
                        )}

                        {/* External Targets block for this Stage */}
                        <div className="ml-8 pl-4 border-l-2 border-brand-100/50 mt-4 mb-2">
                          {/* List assigned external targets */}
                          {(editingSpec.externalTargets || []).filter(t => stageTargets.includes(t.identifier)).map((target, tIdx) => (
                            <div key={`ext-${idx}-${tIdx}`} className="flex items-center gap-2 mb-2 p-2.5 bg-amber-50 rounded-xl border border-amber-100/50">
                              <div className="w-6 h-6 rounded-lg bg-white shadow-sm flex items-center justify-center">
                                <img src="https://upload.wikimedia.org/wikipedia/commons/9/93/Amazon_Web_Services_Logo.svg" alt="AWS" className="w-3.5 opacity-60 grayscale" />
                              </div>
                              <div className="flex-1 flex flex-col justify-center min-w-0">
                                <span className="text-[11px] font-bold text-amber-700 uppercase tracking-wider">{target.type}</span>
                                <span className="text-xs font-bold text-amber-900 truncate">{target.identifier}</span>
                              </div>
                              <button onClick={() => {
                                const newTargets = editingSpec.externalTargets?.filter(t => t.identifier !== target.identifier);
                                const seq = [...(editingSpec.sequence || [])];
                                seq[idx] = stageItems.filter(n => n !== `ext:${target.identifier}`).join(' ');
                                setEditingSpec({ ...editingSpec, sequence: seq, externalTargets: newTargets });
                              }} className="text-amber-300 hover:text-rose-500 transition-colors p-1">
                                <Plus size={14} className="rotate-45" />
                              </button>
                            </div>
                          ))}

                          <div className="relative mt-2">
                            <button
                              onClick={() => setShowExternalDropdownForStage(showExternalDropdownForStage === idx ? null : idx)}
                              className="text-[11px] font-bold text-amber-600 hover:text-amber-700 uppercase tracking-widest flex items-center gap-1 px-2 py-1 rounded-lg transition-colors disabled:opacity-30 disabled:hover:text-amber-600"
                            >
                              <Plus size={12} /> Add 3rd Party Target (e.g., AWS RDS)
                            </button>

                            {showExternalDropdownForStage === idx && (
                              <div className="absolute top-full left-0 mt-2 w-72 bg-white rounded-xl shadow-[0_10px_40px_-10px_rgba(0,0,0,0.15)] border border-slate-100 z-50 p-2 origin-top animate-in zoom-in-95 duration-200">
                                <div className="text-[10px] font-bold uppercase text-slate-400 mb-2 px-2 flex justify-between items-center">
                                  Discovered AWS Resources
                                  {loadingAurora && <div className="w-3 h-3 border-2 border-slate-200 border-t-amber-500 rounded-full animate-spin"></div>}
                                </div>

                                {!loadingAurora && availableAuroraClusters.length === 0 ? (
                                  <div className="px-2 py-3 text-xs font-medium text-slate-500 italic text-center">No AWS Aurora clusters found or IRSA not configured.</div>
                                ) : (
                                  <div className="max-h-48 overflow-y-auto">
                                    {availableAuroraClusters.map(cluster => {
                                      const isAdded = (editingSpec.externalTargets || []).some(t => t.identifier === cluster.identifier);
                                      return (
                                        <button
                                          key={cluster.identifier}
                                          disabled={isAdded}
                                          onClick={() => {
                                            const newTarget = {
                                              ...cluster,
                                              executeAfter: "" // No longer used, target is embedded in sequence array
                                            };
                                            const targets = [...(editingSpec.externalTargets || []), newTarget];
                                            const seq = [...(editingSpec.sequence || [])];
                                            seq[idx] = [...stageItems, `ext:${cluster.identifier}`].join(' ');
                                            setEditingSpec({ ...editingSpec, sequence: seq, externalTargets: targets });
                                            setShowExternalDropdownForStage(null);
                                          }}
                                          className="w-full text-left px-3 py-2 flex flex-col rounded-lg transition-colors hover:bg-slate-50 disabled:opacity-40"
                                        >
                                          <span className="text-xs font-bold text-slate-800 break-all">{cluster.identifier}</span>
                                          <span className="text-[10px] font-medium text-slate-400">{cluster.region}</span>
                                        </button>
                                      );
                                    })}
                                  </div>
                                )}
                              </div>
                            )}
                          </div>
                        </div>
                        <div className="mt-4 pt-3 border-t border-slate-100 flex justify-center opacity-0 group-hover:opacity-100 transition-opacity">
                          <button
                            onClick={() => {
                              const seq = [...(editingSpec.sequence || [])];
                              seq.splice(idx + 1, 0, "");
                              setEditingSpec({ ...editingSpec, sequence: seq });
                            }}
                            className="text-[10px] font-bold text-slate-400 hover:text-brand-600 hover:bg-brand-50 uppercase tracking-widest flex items-center gap-1 px-3 py-1.5 rounded-lg transition-colors"
                          >
                            <Plus size={12} /> Add Stage Below
                          </button>
                        </div>
                      </div>
                    );
                  })}
                  <div className="mt-2 text-[10px] text-slate-400 font-medium space-y-1 px-1">
                    <p>Stages start top to bottom and stop bottom to top.</p>
                    {editingSpec.namespaces ? (
                      <p>• Define groups of namespaces to scale together by separating names with spaces.</p>
                    ) : (
                      <p>• Applied to this specific namespace configuration.</p>
                    )}
                  </div>
                </div>
              </div>
            </div>
          )}

          {/* Exclusions - shown ONLY for individual namespaces */}
          {mode === 'sequence' && !editingSpec.namespaces && (
            <div>
              <label className="block text-sm font-bold text-slate-500 uppercase tracking-wider mb-4">Exclusions (Stay Always On)</label>
              <div className="flex flex-wrap gap-2 p-6 bg-slate-50 rounded-xl border border-slate-100">
                {(editingSpec.exclusions || []).map((ex: string, idx: number) => (
                  <span key={`${ex}-${idx}`} className="px-3 py-2 bg-rose-50 border-2 border-rose-100 text-rose-600 rounded-xl text-xs font-bold flex items-center gap-2">
                    {ex}
                    <Plus size={14} className="rotate-45 cursor-pointer opacity-60 hover:opacity-100"
                      onClick={() => {
                        const exs = [...(editingSpec.exclusions || [])];
                        exs.splice(idx, 1);
                        setEditingSpec({ ...editingSpec, exclusions: exs });
                      }} />
                  </span>
                ))}
                <input type="text" placeholder="Service name or mask (e.g. db-*)" className="bg-white border-2 border-dashed border-slate-200 rounded-xl px-4 py-2 text-xs outline-none focus:border-brand-400 transition-all font-bold text-slate-400 flex-1 min-w-[150px]"
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') {
                      const val = e.currentTarget.value.trim();
                      if (val && !(editingSpec.exclusions || []).includes(val)) {
                        const exs = [...(editingSpec.exclusions || []), val];
                        setEditingSpec({ ...editingSpec, exclusions: exs });
                        e.currentTarget.value = '';
                      }
                    }
                  }} />
              </div>
              <p className="mt-2 text-[10px] text-slate-400 font-medium px-1 italic">
                Use full service names or prefix masks (e.g. <code className="bg-slate-100 px-1 rounded">redis-*</code>). These resources will never be scaled down.
              </p>
            </div>
          )}

          {/* Inheritance Note for Groups */}
          {mode === 'sequence' && editingSpec.namespaces && (
            <div className="bg-brand-50/50 border border-brand-100 p-6 rounded-xl">
              <div className="flex items-center gap-3 text-brand-600 mb-2">
                <Shield size={18} />
                <span className="font-bold text-sm uppercase tracking-wider">Workloads that never stop</span>
              </div>
              <p className="text-xs text-slate-600 leading-relaxed font-medium">
                To keep a workload running while the rest of its namespace scales down, open the namespace and use Workload rules. Every schedule that covers the namespace respects them.
              </p>
            </div>
          )}

        </div>
        <div className="p-8 bg-white border-t border-slate-100 flex gap-4">
          <button onClick={onClose} className="flex-1 px-6 py-4 rounded-xl font-bold text-slate-400 hover:bg-slate-50 transition-all uppercase tracking-widest text-xs">Cancel</button>
          <button onClick={() => onSave(editingSpec)} className="flex-1 px-6 py-4 rounded-xl bg-brand-600 text-white font-bold shadow-xl hover:bg-brand-700 transition-all uppercase tracking-widest text-xs">Apply Configuration</button>
        </div>
      </div>
    </div>
  );
};

export default ScalingConfigModal;
