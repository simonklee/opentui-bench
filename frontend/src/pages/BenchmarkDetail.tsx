import { createSignal, Show, createEffect } from "solid-js";
import type { Component } from "solid-js";
import { formatNs } from "../utils/format";
import { setLastViewedRunId } from "../store";
import { copyTrigger } from "../shortcuts";
import BenchmarkFilterBar from "../components/BenchmarkFilterBar";
import BenchmarkResultsTable from "../components/BenchmarkResultsTable";
import BenchmarkDetailModal from "../components/BenchmarkDetailModal";
import { useBenchmarkDetail } from "../hooks/useBenchmarkDetail";

const BenchmarkDetail: Component = () => {
    const {
        run,
        filter, setFilter,
        category, setCategory,
        categories,
        filteredResults,
        sortBy, sortDesc, handleSort,
        selectedBenchmarkId, selectBenchmark,
        selectedBenchmark,
        trendData,
        hasCpuProfile,
        closeDetail,
        navigate
    } = useBenchmarkDetail();
    
    // UI State that belongs to the view layer
    const [flamegraphView, setFlamegraphView] = createSignal<'flamegraph' | 'callgraph'>('flamegraph');
    const [chartRange, setChartRange] = createSignal(30);
    const [copyToast, setCopyToast] = createSignal(false);

    // Side effects relevant to the global store/view
    createEffect(() => {
        if (run()) {
            setLastViewedRunId(run()!.id);
        }
    });

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


    const handleTrendClick = (runId: number, resultId: number) => {
        // Navigate to the clicked run and select the same benchmark
        navigate(`/benchmarks/${runId}?bench_id=${resultId}`);
    };

    return (
        <div class="flex flex-col h-full relative font-ui">
            <BenchmarkFilterBar 
                run={run()}
                filter={filter()}
                setFilter={setFilter}
                category={category()}
                setCategory={setCategory}
                categories={categories()}
                resultCount={filteredResults().length}
                onCopy={copyBenchmarkResults}
                hasResults={filteredResults().length > 0}
            />

            <BenchmarkResultsTable 
                results={filteredResults()}
                selectedId={selectedBenchmarkId()}
                onSelect={selectBenchmark}
                sortBy={sortBy()}
                sortDesc={sortDesc()}
                onSort={handleSort}
            />

            <Show when={selectedBenchmark()}>
                <BenchmarkDetailModal 
                    benchmark={selectedBenchmark()!}
                    runId={run()!.id}
                    commitHash={run()!.commit_hash}
                    trendData={trendData()}
                    flamegraphView={flamegraphView()}
                    setFlamegraphView={setFlamegraphView}
                    hasCpuProfile={hasCpuProfile()}
                    chartRange={chartRange()}
                    setChartRange={setChartRange}
                    onClose={closeDetail}
                    onDownloadCpu={downloadCpuProfile}
                    onOpenPProf={openPProfUI}
                    onTrendClick={handleTrendClick}
                />
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
