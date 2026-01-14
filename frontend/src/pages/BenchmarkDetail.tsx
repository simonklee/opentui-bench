import { createResource, createSignal, For, Show, createMemo, onMount, createEffect } from "solid-js";
import type { Component } from "solid-js";
import { useParams, useSearchParams, useNavigate } from "@solidjs/router";
import { api } from "../services/api";
import type { BenchmarkResult } from "../services/api";
import { formatNs, formatBytes } from "../utils/format";
import TrendChart from "../components/TrendChart";
import FlamegraphViewer from "../components/FlamegraphViewer";
import { Button } from "../components/Button";
import { setLastViewedRunId } from "../store";
import { copyTrigger } from "../shortcuts";

const BenchmarkDetail: Component = () => {
    const params = useParams();
    const [searchParams, setSearchParams] = useSearchParams();
    const navigate = useNavigate();
    
    const [run] = createResource(() => {
        if (!params.id) return undefined;
        const id = parseInt(params.id);
        return isNaN(id) ? undefined : id;
    }, api.getRunDetails);

    createEffect(() => {
        if (run()) {
            setLastViewedRunId(run()!.id);
        }
    });
    
    const [filter, setFilter] = createSignal("");
    const [category, setCategory] = createSignal("");
    const [selectedBenchmarkId, setSelectedBenchmarkId] = createSignal<number | null>(null);
    const [sortBy, setSortBy] = createSignal<keyof BenchmarkResult | 'mem_stats'>("avg_ns");
    const [sortDesc, setSortDesc] = createSignal(false);
    
    // UI State
    const [flamegraphView, setFlamegraphView] = createSignal<'flamegraph' | 'callgraph'>('flamegraph');
    const [chartRange, setChartRange] = createSignal(30);
    const [hasCpuProfile, setHasCpuProfile] = createSignal(false);
    const [copyToast, setCopyToast] = createSignal(false);
    const [showProfileHelp, setShowProfileHelp] = createSignal(false);
    
    createEffect(() => {
        const bid = searchParams.bench_id;
        if (typeof bid === 'string') {
            setSelectedBenchmarkId(parseInt(bid));
        } else {
            setSelectedBenchmarkId(null);
        }
    });

    // Select benchmark by name if ?name param is present (used when navigating from Compare page)
    createEffect(() => {
        const nameParam = searchParams.name;
        const r = run();
        if (typeof nameParam === 'string' && r) {
            const found = r.results.find(b => b.name === nameParam);
            if (found) {
                setSelectedBenchmarkId(found.id);
                // Set bench_id and clear name param in one call to avoid conflicts
                setSearchParams({
                    bench_id: found.id,
                    name: undefined
                });
            }
        }
    });

    const categories = createMemo(() => {
        const r = run();
        if (!r) return [];
        return [...new Set(r.results.map(i => i.category))];
    });

    const filteredResults = createMemo(() => {
        const r = run();
        if (!r) return [];
        let data = r.results;
        
        if (category()) data = data.filter(i => i.category === category());
        if (filter()) {
            const term = filter().toLowerCase();
            data = data.filter(i => i.name.toLowerCase().includes(term) || i.category.toLowerCase().includes(term));
        }
        
        return [...data].sort((a, b) => {
            let va: number | string = 0;
            let vb: number | string = 0;
            const field = sortBy();
            
            if (field === 'mem_stats') {
                va = a.mem_stats?.[0]?.bytes || 0;
                vb = b.mem_stats?.[0]?.bytes || 0;
            } else {
                va = a[field] as number | string;
                vb = b[field] as number | string;
            }

            if (va < vb) return sortDesc() ? 1 : -1;
            if (va > vb) return sortDesc() ? -1 : 1;
            return 0;
        });
    });

    const handleSort = (field: keyof BenchmarkResult | 'mem_stats') => {
        if (sortBy() === field) {
            setSortDesc(!sortDesc());
        } else {
            setSortBy(field);
            setSortDesc(false);
        }
    };

    const selectBenchmark = (id: number) => {
        setSelectedBenchmarkId(id);
        setSearchParams({ bench_id: id });
    };

    const closeDetail = () => {
        // If we came from compare page, go back there with the same selections
        if (searchParams.from === 'compare') {
            const base = searchParams.compare_base;
            const curr = searchParams.compare_curr;
            const params = new URLSearchParams();
            if (base) params.set('base', base as string);
            if (curr) params.set('curr', curr as string);
            navigate(`/compare?${params.toString()}`);
            return;
        }
        setSelectedBenchmarkId(null);
        setSearchParams({ bench_id: null });
    };
    
    const selectedBenchmark = createMemo(() => {
        return run()?.results.find(r => r.id === selectedBenchmarkId());
    });

    const [trendData] = createResource(
        () => {
            const name = selectedBenchmark()?.name;
            return name ? { name, limit: 100 } : null;
        },
        ({ name, limit }) => api.getTrend(name, limit)
    );
    
    // Check artifacts for current benchmark
    createEffect(async () => {
        const rid = run()?.id;
        const bid = selectedBenchmarkId();
        if (!rid || !bid) {
            setHasCpuProfile(false);
            return;
        }
        
        try {
            const res = await fetch(`/api/runs/${rid}/results/${bid}/artifacts`);
            if (res.ok) {
                const artifacts = await res.json();
                setHasCpuProfile(Array.isArray(artifacts) && artifacts.some((a: any) => a.kind === 'cpu.pprof'));
            } else {
                setHasCpuProfile(false);
            }
        } catch {
            setHasCpuProfile(false);
        }
    });

    const downloadFlameSvg = () => {
        const rid = run()?.id;
        const bid = selectedBenchmarkId();
        if (!rid || !bid) return;
        window.location.href = `/api/runs/${rid}/results/${bid}/${flamegraphView()}`;
    };

    const downloadCpuProfile = () => {
        const rid = run()?.id;
        const bid = selectedBenchmarkId();
        if (!rid || !bid) return;
        window.location.href = `/api/runs/${rid}/results/${bid}/artifacts/cpu.pprof/download`;
    };

    const openPProfUI = () => {
        const rid = run()?.id;
        const bid = selectedBenchmarkId();
        if (!rid || !bid) return;
        window.open(`/api/runs/${rid}/results/${bid}/pprof/ui/`, '_blank');
    };

    const copyBenchmarkResults = () => {
        const results = filteredResults();
        if (!results.length) return;

        const md = '| Name | Avg | P50 | P99 | Min | Max | StdDev |\n|---|---|---|---|---|---|---|\n' +
            results.map(r => 
                `| ${r.name} | ${formatNs(r.avg_ns)} | ${formatNs(r.p50_ns)} | ${formatNs(r.p99_ns)} | ${formatNs(r.min_ns)} | ${formatNs(r.max_ns)} | ${formatNs(r.std_dev_ns)} |`
            ).join('\n');

        navigator.clipboard.writeText(md).then(() => {
            setCopyToast(true);
            setTimeout(() => setCopyToast(false), 2000);
        });
    };

    // Listen for 'y' keyboard shortcut
    createEffect(() => {
        const trigger = copyTrigger();
        if (trigger > 0 && !selectedBenchmarkId()) {
            copyBenchmarkResults();
        }
    });

    return (
        <div class="flex flex-col h-full relative font-ui">
            <div class="flex-none p-4 px-6 border-b border-border bg-bg-dark flex justify-between items-center h-[57px]">
                <div>
                    <h2 class="text-[16px] font-semibold text-text-main flex items-center gap-3">
                        Benchmarks
                        <Show when={run()}>
                            <div class="text-[11px] text-text-muted flex gap-2 items-center font-normal">
                                <span class="font-mono text-accent">#{run()!.commit_hash.substring(0,7)}</span>
                                <span class="font-semibold text-text-main">{run()!.branch}</span>
                                <span>• {run()!.commit_message}</span>
                            </div>
                        </Show>
                    </h2>
                </div>
                <div class="flex gap-2">
                    <input 
                        type="text" 
                        placeholder="Filter benchmarks..." 
                        class="w-[240px] px-3 py-1.5 border border-border rounded-md text-[12px] bg-bg-panel text-text-main focus:bg-bg-dark focus:border-accent outline-none shadow-none"
                        value={filter()}
                        onInput={(e) => setFilter(e.currentTarget.value)}
                        onKeyDown={(e) => { if (e.key === 'Escape') e.currentTarget.blur(); }}
                    />
                    <select 
                        class="px-3 py-1.5 pr-8 border border-border rounded-md text-[12px] bg-bg-dark text-text-main outline-none cursor-pointer appearance-none bg-[url('data:image/svg+xml;charset=UTF-8,%3csvg%20xmlns=\'http://www.w3.org/2000/svg\'%20viewBox=\'0%200%2024%2024\'%20fill=\'none\'%20stroke=\'currentColor\'%20stroke-width=\'2\'%20stroke-linecap=\'round\'%20stroke-linejoin=\'round\'%3e%3cpolyline%20points=\'6%209%2012%2015%2018%209\'%3e%3c/polyline%3e%3c/svg%3e')] bg-[length:12px] bg-[right_8px_center] bg-no-repeat"
                        value={category()}
                        onChange={(e) => setCategory(e.currentTarget.value)}
                    >
                        <option value="">All Categories</option>
                        <For each={categories()}>
                            {c => <option value={c}>{c}</option>}
                        </For>
                    </select>
                    <span class="text-[11px] font-semibold text-text-muted ml-2 flex items-center">{filteredResults().length} RESULTS</span>
                    <Button 
                        onClick={copyBenchmarkResults}
                        disabled={!filteredResults().length}
                    >
                        Copy
                    </Button>
                </div>
            </div>

            <div class="flex-1 overflow-auto bg-bg-dark">
                <table class="w-full text-left border-collapse text-[12px] font-mono">
                    <thead class="bg-bg-panel sticky top-0 z-10 border-b border-border font-ui text-[11px] uppercase tracking-wider text-text-muted select-none">
                        <tr>
                            <th class="px-4 py-2.5 font-semibold cursor-pointer hover:bg-bg-hover hover:text-text-main" onClick={() => handleSort('category')}>Cat</th>
                            <th class="px-4 py-2.5 font-semibold cursor-pointer hover:bg-bg-hover hover:text-text-main" onClick={() => handleSort('name')}>Name</th>
                            <th class="px-4 py-2.5 font-semibold text-right cursor-pointer hover:bg-bg-hover hover:text-text-main" onClick={() => handleSort('avg_ns')}>Avg</th>
                            <th class="px-4 py-2.5 font-semibold text-right cursor-pointer hover:bg-bg-hover hover:text-text-main" onClick={() => handleSort('p50_ns')}>P50</th>
                            <th class="px-4 py-2.5 font-semibold text-right cursor-pointer hover:bg-bg-hover hover:text-text-main" onClick={() => handleSort('p99_ns')}>P99</th>
                            <th class="px-4 py-2.5 font-semibold text-right cursor-pointer hover:bg-bg-hover hover:text-text-main" onClick={() => handleSort('min_ns')}>Min</th>
                            <th class="px-4 py-2.5 font-semibold text-right cursor-pointer hover:bg-bg-hover hover:text-text-main" onClick={() => handleSort('max_ns')}>Max</th>
                            <th class="px-4 py-2.5 font-semibold text-right cursor-pointer hover:bg-bg-hover hover:text-text-main" onClick={() => handleSort('std_dev_ns')}>StdDev</th>
                            <th class="px-4 py-2.5 font-semibold text-right cursor-pointer hover:bg-bg-hover hover:text-text-main" onClick={() => handleSort('mem_stats')}>Mem</th>
                            <th class="px-4 py-2.5 font-semibold text-right cursor-pointer hover:bg-bg-hover hover:text-text-main" onClick={() => handleSort('sample_count')}>N</th>
                            <th class="px-4 py-2.5 font-semibold text-right cursor-pointer hover:bg-bg-hover hover:text-text-main" onClick={() => handleSort('iterations')}>Iter</th>
                        </tr>
                    </thead>
                    <tbody class="divide-y divide-bg-hover">
                        <For each={filteredResults()}>
                            {item => (
                                <tr 
                                    class={`hover:bg-bg-hover cursor-pointer ${selectedBenchmarkId() === item.id ? 'bg-bg-panel' : ''}`}
                                    onClick={() => selectBenchmark(item.id)}
                                >
                                    <td class="px-4 py-2.5 text-text-muted text-[11px] font-ui">{item.category}</td>
                                    <td class="px-4 py-2.5 font-medium text-text-main max-w-[300px] truncate font-ui" title={item.name}>{item.name}</td>
                                    <td class="px-4 py-2.5 text-right font-semibold text-accent">{formatNs(item.avg_ns)}</td>
                                    <td class="px-4 py-2.5 text-right text-text-muted">{formatNs(item.p50_ns)}</td>
                                    <td class="px-4 py-2.5 text-right text-text-muted">{formatNs(item.p99_ns)}</td>
                                    <td class="px-4 py-2.5 text-right text-text-muted">{formatNs(item.min_ns)}</td>
                                    <td class="px-4 py-2.5 text-right text-text-muted">{formatNs(item.max_ns)}</td>
                                    <td class="px-4 py-2.5 text-right text-text-muted">{formatNs(item.std_dev_ns)}</td>
                                    <td class="px-4 py-2.5 text-right text-text-muted">
                                        {formatBytes(item.mem_stats?.[0]?.bytes)}
                                    </td>
                                    <td class="px-4 py-2.5 text-right text-text-main">{item.sample_count}</td>
                                    <td class="px-4 py-2.5 text-right text-text-main">{item.iterations || '-'}</td>
                                </tr>
                            )}
                        </For>
                    </tbody>
                </table>
            </div>

            <Show when={selectedBenchmark()}>
                <div class="absolute inset-0 bg-bg-dark z-50 flex flex-col font-ui">
                    <div class="flex-none px-6 py-3 border-b border-border bg-bg-panel flex justify-between items-center">
                        <nav class="flex items-center gap-2 text-[14px]">
                            <button onClick={closeDetail} class="text-text-muted hover:text-accent font-medium cursor-pointer">Benchmarks</button>
                            <span class="text-text-muted">/</span>
                            <span class="font-mono font-semibold text-text-main">{selectedBenchmark()?.name}</span>
                        </nav>
                        <div>
                            <Button onClick={closeDetail}>✕ Close</Button>
                        </div>
                    </div>
                    
                    <div class="flex-1 overflow-auto p-6">
                        <div class="flex flex-wrap gap-6 p-4 bg-bg-panel border border-border rounded-md mb-6 items-center">
                            <div class="flex flex-col gap-1">
                                <div class="text-[11px] uppercase text-text-muted font-semibold">Average</div>
                                <div class="text-[14px] font-mono font-semibold text-text-main">{formatNs(selectedBenchmark()!.avg_ns)}</div>
                            </div>
                            <div class="flex flex-col gap-1">
                                <div class="text-[11px] uppercase text-text-muted font-semibold">P50 / P99</div>
                                <div class="text-[14px] font-mono font-semibold text-text-main">
                                    {formatNs(selectedBenchmark()!.p50_ns)} / {formatNs(selectedBenchmark()!.p99_ns)}
                                </div>
                            </div>
                            <div class="flex flex-col gap-1">
                                <div class="text-[11px] uppercase text-text-muted font-semibold">Range</div>
                                <div class="text-[14px] font-mono font-semibold text-text-main">
                                    {formatNs(selectedBenchmark()!.min_ns)} - {formatNs(selectedBenchmark()!.max_ns)}
                                </div>
                            </div>
                             <div class="flex flex-col gap-1">
                                <div class="text-[11px] uppercase text-text-muted font-semibold">Trend</div>
                                <div class="text-[14px] font-mono font-semibold text-text-main">
                                    <Show when={trendData() && trendData()!.length > 1} fallback={<span>No history</span>}>
                                        {(() => {
                                            const curr = trendData()![0]!;
                                            const prev = trendData()![1]!;
                                            const diff = curr.avg_ns - prev.avg_ns;
                                            const pct = (diff/prev.avg_ns)*100;
                                            const color = diff > 0 ? 'text-danger' : (diff < 0 ? 'text-success' : 'text-text-muted');
                                            // Use compare_base if coming from compare view, otherwise use trend data
                                            const prevRunId = searchParams.from === 'compare' && searchParams.compare_base
                                                ? searchParams.compare_base
                                                : prev.run_id;
                                            const prevUrl = searchParams.from === 'compare' && searchParams.compare_base
                                                ? `/benchmarks/${prevRunId}?name=${encodeURIComponent(selectedBenchmark()!.name)}`
                                                : `/benchmarks/${prevRunId}?bench_id=${prev.result_id}`;
                                            return (
                                                <>
                                                    <span class={`${color} font-semibold`}>{diff>0?'+':''}{pct.toFixed(1)}%</span>
                                                    {' vs '}
                                                    <a
                                                        href={prevUrl}
                                                        class="text-accent hover:underline cursor-pointer"
                                                        onClick={(e) => { e.preventDefault(); navigate(prevUrl); }}
                                                    >prev</a>
                                                </>
                                            );
                                        })()}
                                    </Show>
                                </div>
                            </div>
                             <div class="flex flex-col gap-1">
                                <div class="text-[11px] uppercase text-text-muted font-semibold">History</div>
                                <div class="text-[14px] font-mono font-semibold text-text-main">
                                    {trendData()?.length || 0} runs
                                </div>
                            </div>
                        </div>

                        <div class="bg-bg-dark p-5 rounded-md border border-border mb-6 flex flex-col h-[800px]">
                            <div class="flex justify-between items-center mb-4">
                                <h3 class="text-[12px] font-bold text-text-muted uppercase">Flamegraph</h3>
                                <div class="flex gap-2 items-center">
                                    <Button 
                                        active={flamegraphView() === 'flamegraph'}
                                        onClick={() => setFlamegraphView('flamegraph')}
                                    >Flamegraph</Button>
                                    <Button
                                        disabled={!hasCpuProfile()}
                                        onClick={openPProfUI}
                                    >Interactive</Button>
                                    <Button
                                        disabled={!hasCpuProfile()}
                                        onClick={downloadCpuProfile}
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
                                    runId={run()!.id} 
                                    resultId={selectedBenchmark()!.id} 
                                    view={flamegraphView()} 
                                />
                            </div>
                        </div>

                        <div class="bg-bg-dark p-5 rounded-md border border-border mb-6 flex flex-col h-[400px]">
                             <div class="flex justify-between items-center mb-4">
                                <h3 class="text-[12px] font-bold text-text-muted uppercase">Performance Trend</h3>
                                <div class="flex gap-2 items-center">
                                    <Button active={chartRange() === 10} onClick={() => setChartRange(10)}>10</Button>
                                    <Button active={chartRange() === 30} onClick={() => setChartRange(30)}>30</Button>
                                    <Button active={chartRange() === 100} onClick={() => setChartRange(100)}>MAX</Button>
                                </div>
                             </div>
                             <div class="flex-1 relative">
                                <Show when={trendData()} fallback={<div>Loading trend...</div>}>
                                    <TrendChart data={trendData()!} range={chartRange()} />
                                </Show>
                             </div>
                        </div>

                        <div>
                            <div class="text-[12px] font-bold text-text-muted uppercase mb-3">Memory Allocations</div>
                            <div class="flex flex-wrap gap-3">
                                <For each={selectedBenchmark()!.mem_stats}>
                                    {m => (
                                        <div class="bg-bg-panel border border-border px-2.5 py-1 rounded-xl text-[11px] flex gap-1.5">
                                            <span class="font-semibold text-text-muted">{m.name}</span>
                                            <span class="font-mono">{formatBytes(m.bytes)}</span>
                                        </div>
                                    )}
                                </For>
                                <Show when={!selectedBenchmark()!.mem_stats?.length}>
                                    <span class="text-text-muted text-[12px]">No memory stats available</span>
                                </Show>
                            </div>
                        </div>
                    </div>
                </div>
            </Show>

            {/* Toast notification */}
            <Show when={copyToast()}>
                <div class="fixed bottom-6 right-6 bg-success text-white px-6 py-3 rounded-md font-medium shadow-lg z-[100]">
                    Copied to clipboard
                </div>
            </Show>
        </div>
    );
};

export default BenchmarkDetail;
