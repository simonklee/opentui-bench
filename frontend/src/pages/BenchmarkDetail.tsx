import { createSignal, Show, createEffect } from "solid-js";
import type { Component } from "solid-js";
import { formatNs } from "../utils/format";
import { setLastViewedRunId } from "../store";
import { copyTrigger } from "../shortcuts";
import BenchmarkFilterBar from "../components/BenchmarkFilterBar";
import BenchmarkResultsTable from "../components/BenchmarkResultsTable";
import BenchmarkDetailModal from "../components/BenchmarkDetailModal";
import { useBenchmarkDetail } from "../hooks/useBenchmarkDetail";
import { ApiError } from "../services/api";
import { Button } from "../components/Button";

const BenchmarkDetail: Component = () => {
  const {
    run,
    runLoading,
    runError,
    refetchRun,
    filter,
    setFilter,
    category,
    setCategory,
    categories,
    filteredResults,
    sortBy,
    sortDesc,
    handleSort,
    selectedBenchmarkId,
    selectBenchmark,
    selectedBenchmark,
    trendData,
    branchTrendData,
    isOnBranch,
    runBranch,
    hasCpuProfile,
    closeDetail,
    navigateToBenchmark,
    regressionContext,
  } = useBenchmarkDetail();

  // UI State that belongs to the view layer
  const [flamegraphView, setFlamegraphView] = createSignal<"flamegraph" | "callgraph">(
    "flamegraph",
  );
  const [chartRange, setChartRange] = createSignal(70);
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
    window.open(`/api/runs/${rid}/results/${bid}/pprof/ui/`, "_blank");
  };

  const copyBenchmarkResults = () => {
    const results = filteredResults();
    if (!results.length) return;

    const md =
      "| Name | Avg | P50 | P99 | Min | Max | StdDev |\n|---|---|---|---|---|---|---|\n" +
      results
        .map(
          (r) =>
            `| ${r.name} | ${formatNs(r.avg_ns)} | ${formatNs(r.p50_ns)} | ${formatNs(r.p99_ns)} | ${formatNs(r.min_ns)} | ${formatNs(r.max_ns)} | ${formatNs(r.std_dev_ns)} |`,
        )
        .join("\n");

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
    navigateToBenchmark(runId, resultId);
  };

  return (
    <div class="flex flex-col h-full relative font-ui">
      <Show
        when={!runLoading()}
        fallback={
          <div class="flex flex-1 items-center justify-center text-[13px] text-text-muted">
            Loading benchmark run...
          </div>
        }
      >
        <Show
          when={!runError()}
          fallback={
            <div class="flex flex-1 flex-col items-center justify-center gap-3 px-6 text-center">
              <div class="text-[14px] font-bold uppercase tracking-widest">
                {runError() instanceof ApiError && runError().status === 404
                  ? "Benchmark run not found"
                  : "Unable to load benchmark run"}
              </div>
              <p class="max-w-md text-[12px] text-text-muted">
                {runError() instanceof ApiError && runError().status === 404
                  ? "This run does not exist or is no longer available."
                  : "Check your connection and try again."}
              </p>
              <Show when={!(runError() instanceof ApiError && runError().status === 404)}>
                <Button onClick={() => void refetchRun()}>Retry</Button>
              </Show>
            </div>
          }
        >
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
              branch={runBranch()}
              runIdentity={run()!}
              trendData={trendData()}
              branchTrendData={isOnBranch() ? branchTrendData() : undefined}
              flamegraphView={flamegraphView()}
              setFlamegraphView={setFlamegraphView}
              hasCpuProfile={hasCpuProfile()}
              chartRange={chartRange()}
              setChartRange={setChartRange}
              regressionContext={regressionContext()}
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
        </Show>
      </Show>
    </div>
  );
};

export default BenchmarkDetail;
