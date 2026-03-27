import React, { useState, useEffect, useRef } from 'react';
import { Layers, Terminal, X, RefreshCw, Check } from 'lucide-react';

interface ScalingGroup {
  metadata: { name: string };
  spec: {
    namespaces: string[];
    sequence?: string[];
    active?: boolean;
  };
  status?: {
    phase: string;
    originalReplicas?: Record<string, number>;
    namespacesReady?: number;
    namespacesTotal?: number;
    readyNamespaces?: string[];
  };
}

interface Event {
  metadata: { name: string; creationTimestamp: string };
  type: string;
  reason: string;
  message: string;
  count: number;
  lastTimestamp?: string;
  eventTime?: string;
}

interface ScalingPipelineModalProps {
  group: ScalingGroup;
  onClose: () => void;
}

const ScalingPipelineModal: React.FC<ScalingPipelineModalProps> = ({ group, onClose }) => {
  const [events, setEvents] = useState<Event[]>([]);
  const [liveGroup, setLiveGroup] = useState<ScalingGroup>(group);
  const terminalRef = useRef<HTMLDivElement>(null);

  const fetchData = async () => {
    try {
      const [eventsRes, groupRes] = await Promise.all([
        fetch(`/api/scaling/groups/${group.metadata.name}/events?t=${Date.now()}`, { cache: 'no-store' }),
        fetch(`/api/scaling/groups?t=${Date.now()}`, { cache: 'no-store' })
      ]);
      
      if (eventsRes.ok) {
        let data = await eventsRes.json();
        data = (data || []).sort((a: Event, b: Event) => {
          const timeA = new Date(a.lastTimestamp || a.eventTime || a.metadata.creationTimestamp).getTime();
          const timeB = new Date(b.lastTimestamp || b.eventTime || b.metadata.creationTimestamp).getTime();
          return timeA - timeB;
        });
        setEvents(data);
      }
      
      if (groupRes.ok) {
        const groupsData: ScalingGroup[] = await groupRes.json();
        const currentGroup = groupsData.find(g => g.metadata.name === group.metadata.name);
        if (currentGroup) {
          setLiveGroup(currentGroup);
        }
      }
    } catch (err) {
      console.error("Failed to fetch data for modal", err);
    }
  };

  useEffect(() => {
    fetchData();
    const interval = setInterval(fetchData, 2000);
    return () => clearInterval(interval);
  }, [group.metadata.name]);

  useEffect(() => {
    if (terminalRef.current) {
      terminalRef.current.scrollTop = terminalRef.current.scrollHeight;
    }
  }, [events]);

  // Build Pipeline Stages
  let stages: string[][] = [];
  const managedNamespaces = liveGroup.spec.namespaces || group.spec.namespaces || [];
  const sequence = liveGroup.spec.sequence || group.spec.sequence;
  
  if (sequence && sequence.length > 0) {
    sequence.forEach(s => stages.push(s.split(/\s+/)));
    
    // Find missing namespaces for the final stage
    const missing = managedNamespaces.filter(ns => {
      let found = false;
      stages.forEach(stage => {
        if (stage.includes(ns)) found = true;
      });
      return !found;
    });
    if (missing.length > 0) {
      stages.push(missing);
    }
  } else {
    stages.push(managedNamespaces);
  }

  // If Scaling Down, the backend reverses the sequence execution. We must mirror this visually.
  const isScalingDown = liveGroup.spec.active === false;
  if (isScalingDown) {
    stages = [...stages].reverse();
  }

  // Pre-calculate ranges to determine which stage is currently running
  let runningCount = 0;
  const stageRanges = stages.map(stage => {
    const start = runningCount;
    runningCount += stage.length;
    return { start, end: runningCount };
  });

  const readyCount = liveGroup.status?.namespacesReady || 0;
  const targetPhase = liveGroup.status?.phase;
  const isScaling = targetPhase === 'ScalingUp' || targetPhase === 'ScalingDown' || targetPhase === 'Scaling...';
  const isDone = targetPhase === 'ScaledUp' || targetPhase === 'ScaledDown';

  return (
    <div className="fixed inset-0 bg-slate-900/60 backdrop-blur-sm z-[100] flex items-center justify-center p-4">
      <div className="bg-white rounded-3xl shadow-2xl w-full max-w-5xl border border-white/20 animate-in fade-in zoom-in duration-300 flex flex-col max-h-[90vh]">
        
        {/* Header */}
        <div className="p-6 border-b border-slate-100 flex justify-between items-center bg-slate-50/50 rounded-t-3xl shrink-0">
          <div className="flex items-center gap-4">
            <div className="w-12 h-12 bg-indigo-50 text-indigo-500 rounded-xl flex items-center justify-center">
              <Layers size={24} />
            </div>
            <div>
              <h2 className="text-2xl font-black text-slate-800 tracking-tight flex items-center gap-3">
                {group.metadata.name} Pipeline
                {isScaling && (
                  <span className="flex items-center gap-1.5 text-xs font-bold bg-amber-100 text-amber-600 px-3 py-1 rounded-full animate-pulse">
                    <RefreshCw size={12} className="animate-spin" /> In Progress
                  </span>
                )}
                {isDone && (
                  <span className="flex items-center gap-1.5 text-xs font-bold bg-emerald-100 text-emerald-600 px-3 py-1 rounded-full">
                    <Check size={12} strokeWidth={3} /> {targetPhase}
                  </span>
                )}
              </h2>
              <p className="text-slate-500 text-sm font-medium mt-1">Scaling sequence visualization and live executor logs</p>
            </div>
          </div>
          <button onClick={onClose} className="p-2 hover:bg-white rounded-full transition-colors text-slate-400 border border-transparent hover:border-slate-200">
            <X size={20} />
          </button>
        </div>

        {/* Pipeline Body */}
        <div className="flex-1 overflow-y-auto bg-slate-50/50 p-8">
          
          <div className="mb-4">
            <h3 className="font-bold text-slate-400 uppercase tracking-wider text-xs mb-6">Execution Sequence</h3>
          </div>

          {/* Render CI/CD Pipeline */}
          <div className="flex items-stretch gap-6 overflow-x-auto pb-6">
            {stages.map((stage, idx) => {
              const range = stageRanges[idx];
              let status = 'pending';
              
              if (isDone) {
                status = 'done';
              } else if (readyCount >= range.end) {
                status = 'done';
              } else if (readyCount >= range.start && readyCount < range.end) {
                status = 'in-progress';
              }

              let boxClass = "bg-white rounded-2xl p-5 w-64 flex flex-col shadow-[0_2px_12px_-4px_rgba(0,0,0,0.06)] ring-1 transition-all ";
              let headerClass = "flex items-center gap-2 mb-4 pb-3 border-b border-slate-50 ";
              let badgeClass = "w-6 h-6 rounded-full flex items-center justify-center font-bold text-xs shrink-0 ";
              
              if (status === 'done') {
                boxClass += "ring-emerald-200 shadow-emerald-100/50";
                badgeClass += "bg-emerald-100 text-emerald-600";
              } else if (status === 'in-progress') {
                boxClass += "ring-amber-300 shadow-amber-200/50 relative overflow-hidden";
                badgeClass += "bg-amber-100 text-amber-600 ring-4 ring-amber-50 animate-pulse";
              } else {
                boxClass += "ring-slate-100 opacity-60";
                badgeClass += "bg-slate-100 text-slate-400";
              }

              return (
                <div key={idx} className="flex items-center shrink-0 group">
                  {/* Stage Box */}
                  <div className={boxClass}>
                    {status === 'in-progress' && (
                      <div className="absolute top-0 left-0 w-full h-1 bg-gradient-to-r from-amber-300 to-amber-500 animate-pulse" />
                    )}
                    <div className={headerClass}>
                      <div className={badgeClass}>
                        {status === 'done' ? <Check size={14} strokeWidth={3} /> : (idx + 1)}
                      </div>
                      <span className={`font-bold ${status === 'pending' ? 'text-slate-400' : 'text-slate-700'}`}>Stage {idx + 1}</span>
                    </div>
                    
                    <div className="flex flex-col gap-2">
                      {stage.map(ns => {
                        const isNSReady = liveGroup.status?.readyNamespaces?.includes(ns);
                        const isStageRunning = status === 'in-progress';
                        
                        let nsStatus = 'pending';
                        if (isNSReady) nsStatus = 'done';
                        else if (isStageRunning) nsStatus = 'in-progress';

                        const isExt = ns.startsWith('ext:');
                        const extName = isExt ? ns.slice(4) : ns;

                        return (
                          <div key={ns} className={`flex items-center gap-2 px-3 py-2 rounded-xl ${nsStatus === 'pending' ? 'bg-slate-50/50' : (nsStatus === 'done' ? 'bg-emerald-50/50' : 'bg-amber-50/50')}`}>
                            <div className={`w-1.5 h-1.5 rounded-full shrink-0 ${nsStatus === 'pending' ? 'bg-slate-200' : (nsStatus === 'done' ? 'bg-emerald-400' : 'bg-amber-400 animate-pulse')}`} />
                            <span className={`text-sm font-medium truncate flex-1 ${nsStatus === 'pending' ? 'text-slate-400' : (nsStatus === 'done' ? 'text-emerald-700' : 'text-amber-700')}`} title={extName}>{extName}</span>
                            {isExt && (
                              <img src="https://upload.wikimedia.org/wikipedia/commons/9/93/Amazon_Web_Services_Logo.svg" alt="AWS" className={`w-4 shrink-0 transition-opacity ${nsStatus === 'pending' ? 'opacity-30 grayscale' : 'opacity-70'}`} />
                            )}
                          </div>
                        );
                      })}
                    </div>
                  </div>

                  {/* Arrow to Next Stage */}
                  {idx < stages.length - 1 && (
                    <div className={`ml-6 flex shrink-0 items-center justify-center ${status === 'done' ? 'text-emerald-300' : 'text-slate-300'}`}>
                      <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                        <line x1="5" y1="12" x2="19" y2="12"></line>
                        <polyline points="12 5 19 12 12 19"></polyline>
                      </svg>
                    </div>
                  )}
                </div>
              );
            })}
          </div>

        </div>

        {/* Event Terminal */}
        <div className="h-64 bg-slate-900 rounded-b-3xl shrink-0 flex flex-col font-mono overflow-hidden">
          <div className="bg-slate-950 px-4 py-2 flex items-center justify-between border-b border-white/10 shadow-md z-10">
            <div className="flex items-center gap-2 text-slate-400 text-xs">
              <Terminal size={14} />
              <span>Kubernetes Events</span>
            </div>
            <div className="flex gap-1.5">
              <div className="w-3 h-3 rounded-full bg-slate-800" />
              <div className="w-3 h-3 rounded-full bg-slate-800" />
              <div className="w-3 h-3 rounded-full bg-slate-800" />
            </div>
          </div>
          
          <div ref={terminalRef} className="flex-1 p-4 overflow-y-auto text-xs text-slate-300 space-y-1.5">
            {events.length === 0 ? (
              <div className="text-slate-600 italic">Waiting for scaling events...</div>
            ) : (
              events.map((e, idx) => {
                const eventTime = e.lastTimestamp || e.eventTime || e.metadata.creationTimestamp;
                const timeString = eventTime ? new Date(eventTime).toLocaleTimeString([], { hour12: false }) : '--:--:--';
                
                return (
                  <div key={idx} className="flex items-start gap-4 hover:bg-slate-800/50 px-2 py-1 -mx-2 rounded transition-colors">
                    <span className="text-slate-500 shrink-0 w-20">
                      {timeString}
                    </span>
                    <span className={`font-bold shrink-0 w-[120px] truncate ${e.type === 'Warning' ? 'text-rose-400' : 'text-emerald-400'}`}>
                      [{e.reason}]
                    </span>
                    <span className={`${e.type === 'Warning' ? 'text-rose-200' : 'text-slate-100'} flex-1 leading-relaxed`}>{e.message}</span>
                    {e.count > 1 && (
                      <span className="text-slate-500 shrink-0 ml-4 font-bold bg-slate-800 px-1.5 py-0.5 rounded">x{e.count}</span>
                    )}
                  </div>
                );
              })
            )}
          </div>
        </div>

      </div>
    </div>
  );
};

export default ScalingPipelineModal;
