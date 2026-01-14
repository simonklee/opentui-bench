import { Show, createSignal, For } from "solid-js";
import type { Component } from "solid-js";
import { useSearchParams } from "@solidjs/router";
import { formatNs, formatBytes } from "../utils/format";
import { Button } from "./Button";
import TrendChart from "./TrendChart";
import FlamegraphViewer from "./FlamegraphViewer";
import type { BenchmarkResult, TrendPoint } from "../services/api";
import TrendIndicator from "./TrendIndicator";

interface BenchmarkDetailModalProps {
  benchmark: BenchmarkResult;
  runId: number;
  commitHash: string;
  trendData: TrendPoint[] | undefined;
  flamegraphView: 'flamegraph' | 'callgraph';
  setFlamegraphView: (v: 'flamegraph' | 'callgraph') => void;
  hasCpuProfile: boolean;
  chartRange: number;
  setChartRange: (v: number) => void;
  onClose: () => void;
  onDownloadCpu: () => void;
  onOpenPProf: () => void;
  onTrendClick?: (runId: number, resultId: number) => void;
}

const BenchmarkDetailModal: Component<BenchmarkDetailModalProps> = (props) => {
  const [showProfileHelp, setShowProfileHelp] = createSignal(false);
  const [showTrendHelp, setShowTrendHelp] = createSignal(false);
  const [searchParams] = useSearchParams();

  // Helper for stat blocks
  const StatBlock = (p: { label: string, value: any, sub?: any }) => (
      <div class="flex flex-col gap-0.5 md:gap-1">
          <div class="text-[9px] md:text-[10px] uppercase tracking-widest text-text-muted font-bold">{p.label}</div>
          <div class="text-[14px] md:text-[16px] font-mono font-medium text-black truncate">{p.value}</div>
          <Show when={p.sub}>
              <div class="text-[10px] md:text-[11px] text-text-muted font-mono truncate">{p.sub}</div>
          </Show>
      </div>
  );

  return (
    <div class="absolute inset-0 bg-white z-50 flex flex-col font-ui">
        {/* Header */}
        <div class="flex-none px-4 md:px-8 py-3 md:py-4 border-b border-black flex justify-between items-center bg-white">
            <nav class="flex items-center gap-2 md:gap-3 text-[13px] overflow-hidden">
                <button onClick={props.onClose} class="text-text-muted hover:text-black uppercase tracking-wider font-bold cursor-pointer whitespace-nowrap text-[11px] md:text-[13px]">Benchmarks</button>
                <span class="text-text-muted">/</span>
                <span class="font-mono text-[11px] md:text-[13px] text-text-muted">#{props.commitHash.slice(0, 7)}</span>
                <span class="text-text-muted">/</span>
                <span class="font-mono font-bold text-black text-[13px] md:text-[15px] truncate">{props.benchmark.name}</span>
            </nav>
            <div class="flex-none ml-4">
                <Button onClick={props.onClose} class="!border-transparent hover:!bg-transparent hover:!text-black hover:underline whitespace-nowrap">
                    Close <span class="hidden sm:inline ml-1">[Esc]</span>
                </Button>
            </div>
        </div>
        
        <div class="flex-1 overflow-auto p-4 md:p-8 bg-white">
            
            {/* Stats Grid */}
            <div class="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-6 gap-y-4 gap-x-2 md:gap-8 mb-4 md:mb-12 pb-4 md:pb-8 border-b border-border">
                <StatBlock label="Average" value={formatNs(props.benchmark.avg_ns)} />
                <StatBlock label="P50 / P99" value={formatNs(props.benchmark.p50_ns)} sub={formatNs(props.benchmark.p99_ns)} />
                <StatBlock label="Min — Max" value={formatNs(props.benchmark.min_ns)} sub={formatNs(props.benchmark.max_ns)} />
                <StatBlock label="Std Dev" value={formatNs(props.benchmark.std_dev_ns)} />
                
                <div class="flex flex-col gap-0.5 md:gap-1">
                    <div class="text-[9px] md:text-[10px] uppercase tracking-widest text-text-muted font-bold">Trend</div>
                    <div class="h-[21px] md:h-[24px] flex items-center">
                         <TrendIndicator 
                            trendData={props.trendData}
                            benchmarkName={props.benchmark.name}
                            currentRunId={props.runId}
                            fromCompare={searchParams.from === 'compare'}
                            compareBaseRunId={searchParams.compare_base as string | undefined}
                        />
                    </div>
                </div>

                <div class="flex flex-col gap-0.5 md:gap-1">
                    <div class="text-[9px] md:text-[10px] uppercase tracking-widest text-text-muted font-bold">Memory</div>
                    <div class="flex flex-col gap-0.5 md:gap-1">
                        <For each={props.benchmark.mem_stats}>
                            {m => (
                                <div class="font-mono text-[10px] md:text-[12px] truncate">
                                    <span class="text-text-muted mr-2">{m.name}:</span>
                                    <span>{formatBytes(m.bytes)}</span>
                                </div>
                            )}
                        </For>
                         <Show when={!props.benchmark.mem_stats?.length}>
                            <span class="text-text-muted text-[10px] md:text-[12px] italic">None</span>
                        </Show>
                    </div>
                </div>
            </div>

            <div class="flex flex-col gap-12 pb-12">
                {/* Trend Column */}
                <div class="flex flex-col h-auto md:h-[400px]">
                    <div class="flex flex-col sm:flex-row sm:justify-between sm:items-center gap-4 sm:gap-0 mb-6 border-b border-border pb-2">
                        <div class="flex items-center gap-2 relative">
                            <h3 class="text-[12px] font-bold text-black uppercase tracking-widest">Performance History</h3>
                            <button 
                                onClick={() => setShowTrendHelp(!showTrendHelp())} 
                                class="w-4 h-4 rounded-full border border-text-muted text-[10px] text-text-muted flex items-center justify-center hover:border-black hover:text-black hover:bg-black/5 transition-colors"
                            >
                                ?
                            </button>
                            <Show when={showTrendHelp()}>
                                <div class="fixed inset-0 z-[60]" onClick={() => setShowTrendHelp(false)}></div>
                                <div class="absolute left-0 top-full mt-2 w-[320px] bg-white border border-black shadow-2xl z-[70] p-4 text-[12px] text-black">
                                    <div class="font-bold uppercase tracking-widest mb-3 border-b border-border pb-2">Visualization Guide</div>
                                    
                                    <div class="space-y-4 font-ui">
                                        <div>
                                            <div class="font-bold mb-1">Error Bars (95% CI)</div>
                                            <p class="text-text-muted leading-relaxed">
                                                Shows the 95% Confidence Interval of the mean. We are 95% confident the true mean lies within this range. Narrower bars indicate higher precision (more stable results or more samples).
                                            </p>
                                        </div>
                                        
                                        <div>
                                            <div class="font-bold mb-1">Shaded Band (Standard Deviation)</div>
                                            <p class="text-text-muted leading-relaxed">
                                                The light gray background band represents ±1 Standard Deviation from the mean, showing the variability of individual benchmark runs.
                                            </p>
                                        </div>

                                        <div>
                                            <div class="font-bold mb-1">Interaction</div>
                                            <p class="text-text-muted leading-relaxed">
                                                Click any data point to inspect that specific run's details.
                                            </p>
                                        </div>
                                    </div>
                                </div>
                            </Show>
                        </div>
                        <div class="flex gap-2 items-center self-start sm:self-auto">
                            <Button active={props.chartRange === 10} onClick={() => props.setChartRange(10)}>10</Button>
                            <Button active={props.chartRange === 30} onClick={() => props.setChartRange(30)}>30</Button>
                            <Button active={props.chartRange === 100} onClick={() => props.setChartRange(100)}>MAX</Button>
                        </div>
                    </div>
                    <div class="h-[300px] md:h-auto md:flex-1 relative border border-border p-4">
                        <Show when={props.trendData} fallback={<div class="flex items-center justify-center h-full text-text-muted font-mono text-xs">Loading trend data...</div>}>
                            <TrendChart 
                                data={props.trendData!} 
                                range={props.chartRange} 
                                currentRunId={props.runId}
                                onPointClick={props.onTrendClick}
                            />
                        </Show>
                    </div>
                    <div class="mt-3 flex justify-between text-[10px] text-text-muted font-mono uppercase tracking-wider">
                        <span>Error Bars: 95% CI</span>
                        <span>Shaded: ±1 SD</span>
                    </div>
                </div>

                {/* Flamegraph Column */}
                <div class="flex flex-col h-auto md:h-[600px]">
                    <div class="flex flex-col sm:flex-row sm:justify-between sm:items-center gap-4 sm:gap-0 mb-6 border-b border-border pb-2">
                        <div class="flex items-center gap-2 relative">
                            <h3 class="text-[12px] font-bold text-black uppercase tracking-widest">Execution Profile</h3>
                            <button 
                                onClick={() => setShowProfileHelp(!showProfileHelp())} 
                                class="w-4 h-4 rounded-full border border-text-muted text-[10px] text-text-muted flex items-center justify-center hover:border-black hover:text-black hover:bg-black/5 transition-colors"
                            >
                                ?
                            </button>
                            <Show when={showProfileHelp()}>
                                <div class="fixed inset-0 z-[60]" onClick={() => setShowProfileHelp(false)}></div>
                                <div class="absolute left-0 top-full mt-2 w-[320px] bg-white border border-black shadow-2xl z-[70] p-4 text-[12px] text-black">
                                    <div class="font-bold uppercase tracking-widest mb-3 border-b border-border pb-2">CPU Profile</div>
                                    <p class="text-text-muted mb-4 font-ui leading-relaxed">
                                        A pprof CPU profile captured during the benchmark run. This visualizes where the program spent its time.
                                    </p>
                                    <div class="space-y-3 font-ui">
                                        <div class="flex flex-col gap-1">
                                            <div class="font-bold flex items-center gap-2">
                                                <span class="w-2 h-2 bg-black rounded-full"></span>
                                                Interactive
                                            </div>
                                            <p class="text-text-muted pl-4">Opens the full pprof web UI in a new tab for deep analysis.</p>
                                        </div>
                                        <div class="flex flex-col gap-1">
                                            <div class="font-bold flex items-center gap-2">
                                                <span class="w-2 h-2 bg-black rounded-full"></span>
                                                Download
                                            </div>
                                            <p class="text-text-muted pl-4">Downloads the <code class="bg-bg-hover px-1 py-0.5 rounded-none font-mono text-[10px]">.pprof</code> file for local use with <code class="bg-bg-hover px-1 py-0.5 rounded-none font-mono text-[10px]">go tool pprof</code>.</p>
                                        </div>
                                    </div>
                                </div>
                            </Show>
                        </div>
                        <div class="flex gap-2 items-center self-start sm:self-auto flex-wrap">
                            <Button 
                                active={props.flamegraphView === 'flamegraph'}
                                onClick={() => props.setFlamegraphView('flamegraph')}
                            >Flamegraph</Button>
                            <Button
                                disabled={!props.hasCpuProfile}
                                onClick={props.onOpenPProf}
                            >Interactive</Button>
                            <Button
                                disabled={!props.hasCpuProfile}
                                onClick={props.onDownloadCpu}
                            >Download</Button>
                        </div>
                    </div>
                    <div class="h-[500px] md:h-auto md:flex-1 bg-white border border-border relative">
                        <FlamegraphViewer 
                            runId={props.runId} 
                            resultId={props.benchmark.id} 
                            view={props.flamegraphView} 
                        />
                    </div>
                </div>
            </div>
        </div>
    </div>
  );
};

export default BenchmarkDetailModal;
