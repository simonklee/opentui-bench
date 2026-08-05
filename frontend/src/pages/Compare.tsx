import { createResource, createSignal, For, Show, createMemo, createEffect } from "solid-js";
import type { Component } from "solid-js";
import { useSearchParams, useNavigate } from "@solidjs/router";
import { api, ApiError, runtimeName, runtimeVersion } from "../services/api";
import type { Run } from "../services/api";
import { formatDate, formatNs } from "../utils/format";
import { Button } from "../components/Button";
import BenchmarkFilterBar from "../components/BenchmarkFilterBar";
import { copyTrigger } from "../shortcuts";
import {
  globalCategory,
  globalFilter,
  lastViewedRunId,
  setGlobalCategory,
  setGlobalFilter,
  benchmarkKind,
  jsRuntimeFilter,
} from "../store";
import { useFilteredBenchmarks } from "../hooks/useFilteredBenchmarks";
import { useFilterParams } from "../hooks/useFilterParams";

/** Group runs by branch for optgroup display. "main" includes empty/null branches. */
function groupRunsByBranch(runs: Run[]): { branch: string; runs: Run[] }[] {
  const groups = new Map<string, Run[]>();
  for (const r of runs) {
    const branch = r.branch && r.branch !== "" ? r.branch : "main";
    if (!groups.has(branch)) groups.set(branch, []);
    groups.get(branch)!.push(r);
  }
  // Put "main" first, then other branches alphabetically
  const result: { branch: string; runs: Run[] }[] = [];
  const mainGroup = groups.get("main");
  if (mainGroup) result.push({ branch: "main", runs: mainGroup });
  for (const [branch, branchRuns] of [...groups.entries()].sort((a, b) =>
    a[0].localeCompare(b[0]),
  )) {
    if (branch !== "main") result.push({ branch, runs: branchRuns });
  }
  return result;
}

function formatRunOption(r: Run): string {
  const runtime = r.benchmark_kind === "js" ? `${runtimeName(r)} ${runtimeVersion(r)} · ` : "";
  return `${runtime}#${r.commit_hash.substring(0, 7)} · ${r.commit_message?.substring(0, 50)}${r.commit_message?.length > 50 ? "..." : ""}`;
}

const RunOption: Component<{ run: Run; disabled: boolean }> = (props) => (
  <option value={String(props.run.id)} disabled={props.disabled}>
    {formatRunOption(props.run)}
  </option>
);

function hasSameIdentity(a: Run, b: Run): boolean {
  return (
    a.benchmark_kind === b.benchmark_kind &&
    a.machine_id === b.machine_id &&
    a.benchmark_suite === b.benchmark_suite &&
    a.protocol_version === b.protocol_version &&
    a.js_runtime === b.js_runtime &&
    runtimeVersion(a) === runtimeVersion(b) &&
    a.zig_version === b.zig_version &&
    a.manifest_hash === b.manifest_hash &&
    (a.benchmark_kind !== "zig" || a.zig_optimize === b.zig_optimize)
  );
}

function hasRuntimeCompareIdentity(a: Run, b: Run): boolean {
  return (
    a.benchmark_kind === "js" &&
    b.benchmark_kind === "js" &&
    (a.commit_hash_full || a.commit_hash) === (b.commit_hash_full || b.commit_hash) &&
    a.js_runtime !== b.js_runtime &&
    a.machine_id === b.machine_id &&
    a.benchmark_suite === b.benchmark_suite &&
    a.protocol_version === b.protocol_version &&
    a.zig_version === b.zig_version &&
    a.manifest_hash === b.manifest_hash
  );
}

function findDefaultPair(runs: Run[], preferredRunId: number | null) {
  const preferred = runs.find((run) => run.id === preferredRunId);
  const candidates = preferred ? [preferred, ...runs.filter((run) => run !== preferred)] : runs;

  for (const current of candidates) {
    const currentIndex = runs.indexOf(current);
    const baseline = runs.slice(currentIndex + 1).find((run) => hasSameIdentity(run, current));
    if (baseline) return { baseline: baseline.id, current: current.id };
  }

  return null;
}

function findRuntimeDefaultPair(runs: Run[], preferredRunId: number | null) {
  const preferred = runs.find((run) => run.id === preferredRunId);
  const candidates = preferred ? [preferred, ...runs.filter((run) => run !== preferred)] : runs;
  for (const compared of candidates) {
    const baseline = runs.find((run) => hasRuntimeCompareIdentity(run, compared));
    if (baseline) return { baseline: baseline.id, current: compared.id };
  }
  return null;
}

function mergeRuns(...lists: Run[][]): Run[] {
  const seen = new Set<number>();
  return lists.flat().filter((run) => {
    if (seen.has(run.id)) return false;
    seen.add(run.id);
    return true;
  });
}

function parseRunId(value: string): number | null {
  if (!/^[1-9]\d*$/.test(value)) return null;
  const id = Number(value);
  return Number.isSafeInteger(id) ? id : null;
}

const Compare: Component = () => {
  const [searchParams, setSearchParams] = useSearchParams();
  const navigate = useNavigate();
  useFilterParams(searchParams, setSearchParams);
  const compareMode = createMemo(() =>
    benchmarkKind() === "js" && searchParams.compare_mode === "runtime" ? "runtime" : "history",
  );
  const [loadedRecentRuns] = createResource(
    () => [benchmarkKind(), compareMode(), jsRuntimeFilter()] as const,
    ([kind, mode, runtime]) =>
      api.getRuns(100, kind, undefined, mode === "runtime" ? "all" : runtime),
  );
  const recentRuns = createMemo(() => {
    const loaded = loadedRecentRuns();
    return loaded?.every((run) => run.benchmark_kind === benchmarkKind()) ? loaded : undefined;
  });
  const [copyToast, setCopyToast] = createSignal(false);
  const compatible = (a: Run, b: Run) =>
    compareMode() === "runtime" ? hasRuntimeCompareIdentity(a, b) : hasSameIdentity(a, b);

  // Keep as strings to match select option values
  const baseId = createMemo(() => {
    const val = searchParams.base;
    return typeof val === "string" ? val : "";
  });
  const currId = createMemo(() => {
    const val = searchParams.curr;
    return typeof val === "string" ? val : "";
  });

  const bookmarkRequest = createMemo(() => {
    const recent = recentRuns();
    if (!recent) return null;
    const kind = benchmarkKind();
    const ids = [baseId(), currId()]
      .map(parseRunId)
      .filter((id): id is number => id !== null && !recent.some((run) => run.id === id))
      .filter((id, index, all) => all.indexOf(id) === index)
      .sort((a, b) => a - b);
    return ids.length ? { ids, kind, key: `${kind}:${ids.join(",")}` } : null;
  });
  const [loadedBookmarkedRuns, { refetch: retryBookmarkedRuns }] = createResource(
    bookmarkRequest,
    async (request) => {
      const settled = await Promise.allSettled(
        request.ids.map((id) => api.getRunDetails(id, request.kind)),
      );
      const runs: Run[] = [];
      const missingIds: number[] = [];
      let loadFailed = false;

      settled.forEach((result, index) => {
        const id = request.ids[index]!;
        if (result.status === "rejected") {
          if (result.reason instanceof ApiError && result.reason.status === 404) {
            missingIds.push(id);
          } else {
            loadFailed = true;
          }
          return;
        }

        const run = result.value;
        if (run.id === id && run.benchmark_kind === request.kind) {
          runs.push(run);
        } else {
          loadFailed = true;
        }
      });

      return { key: request.key, runs, missingIds, loadFailed };
    },
  );
  const bookmarkedRuns = createMemo(() => {
    const loaded = loadedBookmarkedRuns();
    const selectedIds = new Set([parseRunId(baseId()), parseRunId(currId())]);
    return (loaded?.runs ?? []).filter(
      (run) => run.benchmark_kind === benchmarkKind() && selectedIds.has(run.id),
    );
  });
  const bookmarkLoadFailed = createMemo(() => {
    const request = bookmarkRequest();
    const loaded = loadedBookmarkedRuns();
    return !!request && loaded?.key === request.key && loaded.loadFailed;
  });

  const [loadedPeerRuns] = createResource(
    () => {
      const anchors = bookmarkedRuns()
        .filter((run) => run.benchmark_kind === "js")
        .filter((run, index, all) =>
          all.slice(0, index).every((previous) => !hasSameIdentity(previous, run)),
        );
      return anchors.length ? { anchors, kind: benchmarkKind() } : null;
    },
    async ({ anchors, kind }) =>
      (await Promise.all(anchors.map((identity) => api.getRuns(100, kind, identity)))).flat(),
  );
  const peerRuns = createMemo(() => {
    const loaded = loadedPeerRuns();
    const anchors = bookmarkedRuns().filter((run) => run.benchmark_kind === "js");
    return (loaded ?? []).filter((run) => anchors.some((anchor) => hasSameIdentity(run, anchor)));
  });

  const runs = createMemo(() => {
    const recent = recentRuns();
    return recent ? mergeRuns(recent, bookmarkedRuns(), peerRuns()) : undefined;
  });
  const selectedRun = (id: string) => runs()?.find((run) => run.id === parseRunId(id)) ?? null;
  const selectedBaseRun = () => selectedRun(baseId());
  const selectedCurrRun = () => selectedRun(currId());

  createEffect(() => {
    const request = bookmarkRequest();
    const loaded = loadedBookmarkedRuns();
    if (!request || loaded?.key !== request.key) return;

    const updates: Record<string, string | null> = {};

    if (loaded.missingIds.includes(parseRunId(baseId()) ?? -1)) updates.base = null;
    if (loaded.missingIds.includes(parseRunId(currId()) ?? -1)) updates.curr = null;

    if (Object.keys(updates).length) setSearchParams(updates, { replace: true });
  });

  // Group runs by branch for <optgroup> display
  const grouped = createMemo(() => {
    const r = runs();
    if (!r) return [];
    return groupRunsByBranch(r);
  });
  const hasMultipleBranches = createMemo(() => grouped().length > 1);

  const [didAutoSelect, setDidAutoSelect] = createSignal(false);
  let previousKind = benchmarkKind();

  createEffect(() => {
    const kind = benchmarkKind();
    if (kind === previousKind) return;
    previousKind = kind;
    setDidAutoSelect(false);
  });

  const setCompareMode = (mode: "history" | "runtime") => {
    setDidAutoSelect(false);
    setSearchParams({
      compare_mode: mode === "runtime" ? "runtime" : null,
      base: null,
      curr: null,
    });
  };

  // Auto-select runs only on first load with no URL params.
  // Use the router's searchParams (not window.location.search) so that
  // navigations like navigate("/compare?base=103&curr=105") are detected
  // immediately when this component mounts.
  createEffect(() => {
    const r = runs();
    // Skip if URL already has base/curr params, or we already auto-selected
    if (searchParams.base || searchParams.curr || didAutoSelect()) return;

    if (r && r.length > 0) {
      const pair =
        compareMode() === "runtime"
          ? findRuntimeDefaultPair(r, lastViewedRunId())
          : findDefaultPair(r, lastViewedRunId());
      if (!pair) return;
      setDidAutoSelect(true);

      setSearchParams(
        {
          ...searchParams,
          base: pair.baseline,
          curr: pair.current,
        },
        { replace: true },
      );
    }
  });

  const compareRequest = createMemo(() => {
    const baseline = selectedBaseRun();
    const current = selectedCurrRun();
    const kind = benchmarkKind();
    if (
      !baseline ||
      !current ||
      baseline.benchmark_kind !== kind ||
      current.benchmark_kind !== kind ||
      !compatible(baseline, current)
    ) {
      return null;
    }
    return {
      baseId: baseline.id,
      currId: current.id,
      kind,
      key: `${kind}:${baseline.id}:${current.id}`,
    };
  });
  const [loadedCompareData, { refetch: retryCompareData }] = createResource(
    compareRequest,
    async (request) => ({
      key: request.key,
      data:
        compareMode() === "runtime"
          ? await api.getRuntimeCompare(request.baseId, request.currId).then((response) => ({
              ...response.baseline,
              comparisons: response.comparisons.map((comparison) => ({
                ...comparison,
                current_ns: comparison.compared_ns,
                change_percent: comparison.duration_change_percent,
                current_result_id: comparison.compared_result_id,
              })),
            }))
          : await api.getCompare(request.baseId, request.currId, request.kind),
    }),
  );
  const compareFailure = createMemo(() => {
    if (!compareRequest() || loadedCompareData.loading) return;
    const error = loadedCompareData.error;
    if (!error) return;
    const unavailable = error instanceof ApiError && (error.status === 400 || error.status === 404);
    return {
      message: unavailable
        ? "These runs are unavailable or incompatible."
        : "Could not load comparison.",
      retryable: !(error instanceof ApiError) || error.status >= 500,
    };
  });
  const compareData = createMemo(() => {
    const request = compareRequest();
    if (!request || loadedCompareData.loading || compareFailure()) return;

    const loaded = loadedCompareData();
    return loaded?.key === request.key ? loaded.data : undefined;
  });
  const { filteredResults: filteredComparisons, categories } = useFilteredBenchmarks(
    () => compareData()?.comparisons ?? [],
  );

  const isBaseOptionDisabled = (run: Run) => {
    const current = selectedCurrRun();
    return current !== null && !compatible(run, current);
  };

  const isCurrOptionDisabled = (run: Run) => {
    const baseline = selectedBaseRun();
    return baseline !== null && !compatible(run, baseline);
  };

  const handleBaseChange = (e: Event) => {
    const val = (e.target as HTMLSelectElement).value;
    setSearchParams({ ...searchParams, base: val });
  };

  const handleCurrChange = (e: Event) => {
    const val = (e.target as HTMLSelectElement).value;
    setSearchParams({ ...searchParams, curr: val });
  };

  const swapRuns = () => setSearchParams({ base: currId(), curr: baseId() });

  const [sortBy, setSortBy] = createSignal<string>("change_percent");
  const [sortDesc, setSortDesc] = createSignal(true);

  const sortedComparisons = createMemo(() => {
    const data = filteredComparisons();
    if (!data) return [];
    return [...data].sort((a: any, b: any) => {
      const va = a[sortBy()];
      const vb = b[sortBy()];
      if (va < vb) return sortDesc() ? 1 : -1;
      if (va > vb) return sortDesc() ? -1 : 1;
      return 0;
    });
  });

  const handleSort = (field: string) => {
    if (sortBy() === field) {
      setSortDesc(!sortDesc());
    } else {
      setSortBy(field);
      setSortDesc(true);
    }
  };

  const handleBenchmarkClick = (resultId: number, baselineResultId: number) => {
    const request = compareRequest();
    if (!request || !compareData()) return;

    const params = new URLSearchParams();
    params.set("bench_id", String(resultId));
    params.set("benchmark_kind", request.kind);
    params.set("from", "compare");
    params.set("compare_base", String(request.baseId));
    params.set("compare_base_result", String(baselineResultId));
    params.set("compare_curr", String(request.currId));
    navigate(`/benchmarks/${request.currId}?${params.toString()}`);
  };

  const copyCompareResults = () => {
    const comparisons = sortedComparisons();
    if (!comparisons.length) return;

    const baselineLabel =
      benchmarkKind() === "js" && selectedBaseRun()
        ? `${runtimeName(selectedBaseRun()!)} ${runtimeVersion(selectedBaseRun()!)}`
        : "Baseline";
    const comparedLabel =
      benchmarkKind() === "js" && selectedCurrRun()
        ? `${runtimeName(selectedCurrRun()!)} ${runtimeVersion(selectedCurrRun()!)}`
        : "Current";
    const md =
      `| Benchmark | ${baselineLabel} | ${comparedLabel} | Delta (compared / baseline) |\n|---|---|---|---|\n` +
      comparisons
        .map((c) => {
          const delta =
            c.change_percent > 0
              ? `+${c.change_percent.toFixed(1)}%`
              : `${c.change_percent.toFixed(1)}%`;
          return `| ${c.name} | ${formatNs(c.baseline_ns)} | ${formatNs(c.current_ns)} | ${delta} |`;
        })
        .join("\n");

    navigator.clipboard.writeText(md).then(() => {
      setCopyToast(true);
      setTimeout(() => setCopyToast(false), 2000);
    });
  };

  // Listen for 'y' keyboard shortcut
  createEffect(() => {
    const trigger = copyTrigger();
    if (trigger > 0) {
      copyCompareResults();
    }
  });

  return (
    <div class="flex flex-col h-full font-ui">
      <div class="flex-none p-6 border-b border-border bg-bg-dark h-[57px] flex items-center justify-between">
        <div class="flex items-center gap-4">
          <h2 class="text-[14px] font-bold text-black uppercase tracking-widest">COMPARE</h2>
          <Show when={benchmarkKind() === "js"}>
            <div class="flex border border-border text-[9px] font-bold uppercase tracking-wider">
              <button
                class={`px-2 py-1 ${compareMode() === "history" ? "bg-black text-white" : "bg-white text-text-muted"}`}
                onClick={() => setCompareMode("history")}
              >
                History
              </button>
              <button
                class={`px-2 py-1 ${compareMode() === "runtime" ? "bg-black text-white" : "bg-white text-text-muted"}`}
                onClick={() => setCompareMode("runtime")}
              >
                Runtimes
              </button>
            </div>
          </Show>
        </div>
        <Button onClick={copyCompareResults} disabled={!sortedComparisons().length}>
          Copy
        </Button>
      </div>

      <div class="flex-none px-6 py-5 bg-bg-dark border-b border-border">
        <Show
          when={runs()}
          fallback={
            <div class="bg-bg-panel p-6 rounded-md border border-border text-center text-text-muted text-[13px]">
              Loading runs...
            </div>
          }
        >
          <div class="grid grid-cols-1 lg:grid-cols-[1fr_auto_1fr] gap-4 lg:gap-6 items-stretch bg-bg-panel p-4 sm:p-6 rounded-md border border-border">
            <div class="flex flex-col gap-3">
              <div class="flex items-center justify-between">
                <label
                  for="baseline-select"
                  class="text-[11px] font-bold text-text-muted uppercase tracking-widest"
                >
                  {compareMode() === "runtime" && selectedBaseRun()
                    ? `${runtimeName(selectedBaseRun()!)} baseline`
                    : "Baseline"}
                </label>
              </div>
              <div class="flex flex-col gap-2">
                <div class="flex flex-col gap-2">
                  <select
                    id="baseline-select"
                    class="p-2.5 pr-9 border border-border rounded-none text-[12px] bg-bg-dark text-text-main outline-none focus:border-accent appearance-none bg-[url('data:image/svg+xml;charset=UTF-8,%3csvg%20xmlns=\'http://www.w3.org/2000/svg\'%20viewBox=\'0%200%2024%2024\'%20fill=\'none\'%20stroke=\'currentColor\'%20stroke-width=\'2\'%20stroke-linecap=\'round\'%20stroke-linejoin=\'round\'%3e%3cpolyline%20points=\'6%209%2012%2015%2018%209\'%3e%3c/polyline%3e%3c/svg%3e')] bg-[length:12px] bg-[right_8px_center] bg-no-repeat cursor-pointer hover:border-black transition-colors"
                    value={baseId()}
                    onChange={handleBaseChange}
                  >
                    <option value="">Select Run</option>
                    <Show
                      when={hasMultipleBranches()}
                      fallback={
                        <For each={runs()}>
                          {(r) => <RunOption run={r} disabled={isBaseOptionDisabled(r)} />}
                        </For>
                      }
                    >
                      <For each={grouped()}>
                        {(group) => (
                          <optgroup label={group.branch}>
                            <For each={group.runs}>
                              {(r) => <RunOption run={r} disabled={isBaseOptionDisabled(r)} />}
                            </For>
                          </optgroup>
                        )}
                      </For>
                    </Show>
                  </select>
                  <Show when={selectedBaseRun()}>
                    {(run) => (
                      <div class="flex items-center justify-between text-[10px] text-text-muted">
                        <div class="flex items-center gap-1.5">
                          <a
                            href={`https://github.com/anomalyco/opentui/commit/${run().commit_hash}`}
                            target="_blank"
                            class="font-mono text-text-main underline decoration-dotted underline-offset-2 hover:decoration-solid"
                          >
                            #{run().commit_hash.substring(0, 7)}
                          </a>
                          <Show
                            when={run().branch && run().branch !== "" && run().branch !== "main"}
                          >
                            <span class="px-1 py-0.5 text-[9px] font-mono font-medium bg-purple-100 text-purple-700 rounded-sm">
                              {run().branch}
                            </span>
                          </Show>
                        </div>
                        <span>{formatDate(run().run_date)}</span>
                      </div>
                    )}
                  </Show>
                </div>
              </div>
            </div>

            <div class="flex lg:flex flex-row lg:flex-col items-center justify-center text-text-muted font-semibold text-[11px] uppercase tracking-widest">
              <div class="w-8 h-[1px] bg-border"></div>
              <button
                class="my-3 border border-border px-2 py-1 hover:border-black"
                onClick={swapRuns}
                title="Swap comparison direction"
              >
                SWAP
              </button>
              <div class="w-8 h-[1px] bg-border"></div>
            </div>

            <div class="flex flex-col gap-3">
              <div class="flex items-center justify-between">
                <label
                  for="current-select"
                  class="text-[11px] font-bold text-text-muted uppercase tracking-widest"
                >
                  {compareMode() === "runtime" && selectedCurrRun()
                    ? `${runtimeName(selectedCurrRun()!)} compared`
                    : "Current"}
                </label>
              </div>
              <div class="flex flex-col gap-2">
                <select
                  id="current-select"
                  class="p-2.5 pr-9 border border-border rounded-none text-[12px] bg-bg-dark text-text-main outline-none focus:border-accent appearance-none bg-[url('data:image/svg+xml;charset=UTF-8,%3csvg%20xmlns=\'http://www.w3.org/2000/svg\'%20viewBox=\'0%200%2024%2024\'%20fill=\'none\'%20stroke=\'currentColor\'%20stroke-width=\'2\'%20stroke-linecap=\'round\'%20stroke-linejoin=\'round\'%3e%3cpolyline%20points=\'6%209%2012%2015%2018%209\'%3e%3c/polyline%3e%3c/svg%3e')] bg-[length:12px] bg-[right_8px_center] bg-no-repeat cursor-pointer hover:border-black transition-colors"
                  value={currId()}
                  onChange={handleCurrChange}
                >
                  <option value="">Select Run</option>
                  <Show
                    when={hasMultipleBranches()}
                    fallback={
                      <For each={runs()}>
                        {(r) => <RunOption run={r} disabled={isCurrOptionDisabled(r)} />}
                      </For>
                    }
                  >
                    <For each={grouped()}>
                      {(group) => (
                        <optgroup label={group.branch}>
                          <For each={group.runs}>
                            {(r) => <RunOption run={r} disabled={isCurrOptionDisabled(r)} />}
                          </For>
                        </optgroup>
                      )}
                    </For>
                  </Show>
                </select>
                <Show when={selectedCurrRun()}>
                  {(run) => (
                    <div class="flex items-center justify-between text-[10px] text-text-muted">
                      <div class="flex items-center gap-1.5">
                        <a
                          href={`https://github.com/anomalyco/opentui/commit/${run().commit_hash}`}
                          target="_blank"
                          class="font-mono text-text-main underline decoration-dotted underline-offset-2 hover:decoration-solid"
                        >
                          #{run().commit_hash.substring(0, 7)}
                        </a>
                        <Show when={run().branch && run().branch !== "" && run().branch !== "main"}>
                          <span class="px-1 py-0.5 text-[9px] font-mono font-medium bg-purple-100 text-purple-700 rounded-sm">
                            {run().branch}
                          </span>
                        </Show>
                      </div>
                      <span>{formatDate(run().run_date)}</span>
                    </div>
                  )}
                </Show>
              </div>
            </div>
          </div>
        </Show>
        <Show when={bookmarkLoadFailed()}>
          <div class="mt-3 flex items-center justify-between gap-3 border border-warning px-3 py-2 text-[11px] text-text-main">
            <span>Could not load a bookmarked run. Check your connection and retry.</span>
            <Button onClick={() => void retryBookmarkedRuns()}>Retry</Button>
          </div>
        </Show>
      </div>

      <BenchmarkFilterBar
        run={null}
        filter={globalFilter()}
        setFilter={setGlobalFilter}
        category={globalCategory()}
        setCategory={setGlobalCategory}
        categories={categories()}
        resultCount={filteredComparisons().length}
        onCopy={copyCompareResults}
        hasResults={filteredComparisons().length > 0}
        showRunInfo={false}
        showCopy={false}
      />

      <Show when={benchmarkKind() === "js"}>
        <div class="flex-none border-b border-border bg-bg-panel px-4 py-2 text-[11px] text-text-muted sm:px-6">
          {compareMode() === "runtime"
            ? "Runtime comparison uses P50 duration. Delta = (compared - baseline) / baseline; lower is better."
            : "JavaScript statistical inference is disabled; deltas are descriptive."}
        </div>
      </Show>

      <div class="flex-1 overflow-auto bg-bg-dark">
        <Show
          when={compareData()}
          fallback={
            <div class="p-8 text-center text-text-muted text-[13px]">
              <Show
                when={compareFailure()}
                fallback={
                  compareRequest() && loadedCompareData.loading
                    ? "Loading comparison..."
                    : "Select two compatible runs to compare"
                }
              >
                {(failure) => (
                  <div class="inline-flex items-center gap-3">
                    <span>{failure().message}</span>
                    <Show when={failure().retryable}>
                      <Button onClick={() => void retryCompareData()}>Retry</Button>
                    </Show>
                  </div>
                )}
              </Show>
            </div>
          }
        >
          <Show when={compareMode() === "runtime"}>
            <div class="divide-y divide-border sm:hidden">
              <For each={sortedComparisons()}>
                {(comparison) => {
                  const speedRatio =
                    comparison.speed_ratio ?? comparison.baseline_ns / comparison.current_ns;
                  const speed =
                    Math.abs(comparison.change_percent) < 0.05
                      ? "Same duration"
                      : speedRatio >= 1
                        ? `${runtimeName(selectedCurrRun()!)} ${speedRatio.toFixed(2)}x faster`
                        : `${runtimeName(selectedBaseRun()!)} ${(1 / speedRatio).toFixed(2)}x faster`;
                  return (
                    <button
                      type="button"
                      class="grid w-full grid-cols-2 gap-x-4 gap-y-3 px-4 py-4 text-left hover:bg-bg-hover"
                      onClick={() =>
                        handleBenchmarkClick(
                          comparison.current_result_id,
                          comparison.baseline_result_id,
                        )
                      }
                    >
                      <span class="col-span-2 font-ui text-[12px] font-semibold text-text-main">
                        {comparison.name}
                      </span>
                      <span>
                        <span class="block font-ui text-[9px] font-bold uppercase tracking-widest text-text-muted">
                          Baseline
                        </span>
                        <span class="font-mono text-[12px]">
                          {formatNs(comparison.baseline_ns)}
                        </span>
                      </span>
                      <span class="text-right">
                        <span class="block font-ui text-[9px] font-bold uppercase tracking-widest text-text-muted">
                          Compared
                        </span>
                        <span class="font-mono text-[12px]">{formatNs(comparison.current_ns)}</span>
                      </span>
                      <span class="col-span-2 flex items-baseline justify-between border-t border-border pt-2 font-mono">
                        <span class="text-[11px] font-bold">
                          {comparison.change_percent > 0 ? "+" : ""}
                          {comparison.change_percent.toFixed(1)}%
                        </span>
                        <span class="text-[10px] text-text-muted">{speed}</span>
                      </span>
                    </button>
                  );
                }}
              </For>
            </div>
          </Show>
          <table
            class={`w-full text-left border-collapse text-[12px] font-mono ${compareMode() === "runtime" ? "hidden sm:table" : ""}`}
          >
            <thead class="bg-bg-dark sticky top-0 z-10 border-b-2 border-black font-ui text-[10px] uppercase tracking-widest text-text-main">
              <tr>
                <th
                  class="px-4 py-2.5 font-semibold cursor-pointer hover:bg-bg-hover hover:text-text-main"
                  onClick={() => handleSort("name")}
                >
                  Benchmark
                </th>
                <th
                  class="px-4 py-2.5 font-semibold text-right cursor-pointer hover:bg-bg-hover hover:text-text-main"
                  onClick={() => handleSort("baseline_ns")}
                >
                  Baseline
                </th>
                <th
                  class="px-4 py-2.5 font-semibold text-right cursor-pointer hover:bg-bg-hover hover:text-text-main"
                  onClick={() => handleSort("current_ns")}
                >
                  {compareMode() === "runtime" ? "Compared" : "Current"}
                </th>
                <th
                  class="px-4 py-2.5 font-semibold text-right cursor-pointer hover:bg-bg-hover hover:text-text-main"
                  onClick={() => handleSort("change_percent")}
                >
                  {compareMode() === "runtime" ? "Duration delta / speed" : "Delta %"}
                </th>
              </tr>
            </thead>
            <tbody class="divide-y divide-bg-hover">
              <For each={sortedComparisons()}>
                {(c) => {
                  const isPos = c.change_percent > 0;
                  const isNeg = c.change_percent < 0;
                  const speedRatio = c.speed_ratio ?? c.baseline_ns / c.current_ns;
                  const colorClass =
                    benchmarkKind() === "js"
                      ? "text-text-main"
                      : isPos
                        ? "text-danger"
                        : isNeg
                          ? "text-success"
                          : "text-text-muted";

                  return (
                    <tr
                      class="hover:bg-bg-hover cursor-pointer"
                      onClick={() =>
                        handleBenchmarkClick(c.current_result_id, c.baseline_result_id)
                      }
                    >
                      <td class="px-4 py-2.5 font-medium text-text-main font-ui">{c.name}</td>
                      <td class="px-4 py-2.5 text-right text-text-muted">
                        {formatNs(c.baseline_ns)}
                      </td>
                      <td class="px-4 py-2.5 text-right text-text-main">
                        {formatNs(c.current_ns)}
                      </td>
                      <td class={`px-4 py-2.5 text-right font-bold ${colorClass}`}>
                        {isPos ? "+" : ""}
                        {c.change_percent.toFixed(1)}%
                        <Show when={compareMode() === "runtime"}>
                          <span class="block text-[10px] font-normal text-text-muted">
                            {Math.abs(c.change_percent) < 0.05
                              ? "same"
                              : speedRatio >= 1
                                ? `${runtimeName(selectedCurrRun()!)} ${speedRatio.toFixed(2)}x faster`
                                : `${runtimeName(selectedBaseRun()!)} ${(1 / speedRatio).toFixed(2)}x faster`}
                          </span>
                        </Show>
                      </td>
                    </tr>
                  );
                }}
              </For>
            </tbody>
          </table>
        </Show>
      </div>

      {/* Toast notification */}
      <Show when={copyToast()}>
        <div class="fixed bottom-6 right-6 bg-success text-white px-6 py-3 rounded-md font-medium shadow-lg z-50">
          Copied to clipboard
        </div>
      </Show>
    </div>
  );
};

export default Compare;
