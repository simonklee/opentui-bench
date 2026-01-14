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
  const [searchParams] = useSearchParams();

  return (
    <div class="absolute inset-0 bg-bg-dark z-50 flex flex-col font-ui">
        <div class="flex-none px-6 py-3 border-b border-border bg-bg-panel flex justify-between items-center">
            <nav class="flex items-center gap-2 text-[14px]">
                <button onClick={props.onClose} class="text-text-muted hover:text-accent font-medium cursor-pointer">Benchmarks</button>
                <span class="text-text-muted">/</span>
                <span class="font-mono font-semibold text-text-main">{props.benchmark.name}</span>
            </nav>
            <div>
                <Button onClick={props.onClose}>✕ Close</Button>
            </div>
        </div>
        
        <div class="flex-1 overflow-auto p-6">
            <div class="flex flex-wrap gap-6 p-4 bg-bg-panel border border-border rounded-md mb-6 items-center">
                <div class="flex flex-col gap-1">
                    <div class="text-[11px] uppercase text-text-muted font-semibold">Average</div>
                    <div class="text-[14px] font-mono font-semibold text-text-main">{formatNs(props.benchmark.avg_ns)}</div>
                </div>
                <div class="flex flex-col gap-1">
                    <div class="text-[11px] uppercase text-text-muted font-semibold">P50 / P99</div>
                    <div class="text-[14px] font-mono font-semibold text-text-main">
                        {formatNs(props.benchmark.p50_ns)} / {formatNs(props.benchmark.p99_ns)}
                    </div>
                </div>
                <div class="flex flex-col gap-1">
                    <div class="text-[11px] uppercase text-text-muted font-semibold">Range</div>
                    <div class="text-[14px] font-mono font-semibold text-text-main">
                        {formatNs(props.benchmark.min_ns)} - {formatNs(props.benchmark.max_ns)}
                    </div>
                </div>
                    <div class="flex flex-col gap-1">
                    <div class="text-[11px] uppercase text-text-muted font-semibold">Trend</div>
                    <div class="text-[14px] font-mono font-semibold text-text-main">
                        <TrendIndicator 
                            trendData={props.trendData}
                            benchmarkName={props.benchmark.name}
                            fromCompare={searchParams.from === 'compare'}
                            compareBaseRunId={searchParams.compare_base as string | undefined}
                        />
                    </div>
                </div>
                    <div class="flex flex-col gap-1">
                    <div class="text-[11px] uppercase text-text-muted font-semibold">History</div>
                    <div class="text-[14px] font-mono font-semibold text-text-main">
                        {props.trendData?.length || 0} runs
                    </div>
                </div>
            </div>

            <div class="bg-bg-dark p-5 rounded-md border border-border mb-6 flex flex-col h-[800px]">
                <div class="flex justify-between items-center mb-4">
                    <h3 class="text-[12px] font-bold text-text-muted uppercase">Flamegraph</h3>
                    <div class="flex gap-2 items-center">
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
                        >Download Profile</Button>
                        <div class="relative">
                            <Button class="px-2.5" onClick={() => setShowProfileHelp(!showProfileHelp())}>?</Button>
                            <Show when={showProfileHelp()}>
                                <div class="absolute right-0 top-full mt-2 w-[300px] bg-bg-panel border border-border rounded-md shadow-lg z-50 p-4 text-[12px] text-text-main">
                                    <div class="font-semibold mb-2">CPU Profile</div>
                                    <p class="text-text-muted mb-2">
                                        A pprof CPU profile captured during the benchmark run.
                                    </p>
                                    <ul class="text-text-muted list-disc list-inside space-y-1">
                                        <li><strong>Interactive</strong> - Opens pprof web UI in a new tab</li>
                                        <li><strong>Download</strong> - Downloads the .pprof file for use with <code class="bg-bg-dark px-1 rounded">go tool pprof</code></li>
                                    </ul>
                                </div>
                            </Show>
                        </div>
                    </div>
                </div>
                <div class="flex-1 bg-bg-panel rounded overflow-hidden relative border border-border">
                    <FlamegraphViewer 
                        runId={props.runId} 
                        resultId={props.benchmark.id} 
                        view={props.flamegraphView} 
                    />
                </div>
            </div>

            <div class="mb-8 flex flex-col h-[450px]">
                    <div class="flex justify-between items-center mb-6">
                    <h3 class="text-[14px] font-bold text-text-main font-mono">PERFORMANCE TREND</h3>
                    <div class="flex gap-2 items-center">
                        <Button active={props.chartRange === 10} onClick={() => props.setChartRange(10)}>10</Button>
                        <Button active={props.chartRange === 30} onClick={() => props.setChartRange(30)}>30</Button>
                        <Button active={props.chartRange === 100} onClick={() => props.setChartRange(100)}>MAX</Button>
                    </div>
                    </div>
                    <div class="flex-1 relative">
                    <Show when={props.trendData} fallback={<div>Loading trend...</div>}>
                        <TrendChart 
                            data={props.trendData!} 
                            range={props.chartRange} 
                            onPointClick={props.onTrendClick}
                        />
                    </Show>
                    </div>
                    <div class="mt-4 text-[11px] text-text-muted font-mono flex gap-4">
                        <span>• Error bars: 95% CI</span>
                        <span>• Shaded: ±1 SD</span>
                    </div>
            </div>

            <div>
                <div class="text-[12px] font-bold text-text-muted uppercase mb-3">Memory Allocations</div>
                <div class="flex flex-wrap gap-3">
                    <For each={props.benchmark.mem_stats}>
                        {m => (
                            <div class="bg-bg-panel border border-border px-2.5 py-1 rounded-xl text-[11px] flex gap-1.5">
                                <span class="font-semibold text-text-muted">{m.name}</span>
                                <span class="font-mono">{formatBytes(m.bytes)}</span>
                            </div>
                        )}
                    </For>
                    <Show when={!props.benchmark.mem_stats?.length}>
                        <span class="text-text-muted text-[12px]">No memory stats available</span>
                    </Show>
                </div>
            </div>
        </div>
    </div>
  );
};

export default BenchmarkDetailModal;
