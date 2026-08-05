import { createResource, createSignal, For, Show, createMemo, createEffect } from "solid-js";
import type { Component } from "solid-js";
import { useSearchParams, useNavigate } from "@solidjs/router";
import { api, ApiError } from "../services/api";
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
  return `#${r.commit_hash.substring(0, 7)} · ${r.commit_message?.substring(0, 50)}${r.commit_message?.length > 50 ? "..." : ""}`;
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
    a.bun_version === b.bun_version &&
    a.zig_version === b.zig_version &&
    a.manifest_hash === b.manifest_hash &&
    (a.benchmark_kind !== "zig" || a.zig_optimize === b.zig_optimize)
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
  const [loadedRecentRuns] = createResource(benchmarkKind, (kind) => api.getRuns(100, kind));
  const recentRuns = createMemo(() => {
    const loaded = loadedRecentRuns();
    return loaded?.every((run) => run.benchmark_kind === benchmarkKind()) ? loaded : undefined;
  });
  const [copyToast, setCopyToast] = createSignal(false);

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

  // Auto-select runs only on first load with no URL params.
  // Use the router's searchParams (not window.location.search) so that
  // navigations like navigate("/compare?base=103&curr=105") are detected
  // immediately when this component mounts.
  createEffect(() => {
    const r = runs();
    // Skip if URL already has base/curr params, or we already auto-selected
    if (searchParams.base || searchParams.curr || didAutoSelect()) return;

    if (r && r.length > 0) {
      setDidAutoSelect(true);

      const pair = findDefaultPair(r, lastViewedRunId());
      if (!pair) return;

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
      !hasSameIdentity(baseline, current)
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
      data: await api.getCompare(request.baseId, request.currId, request.kind),
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
    return current !== null && !hasSameIdentity(run, current);
  };

  const isCurrOptionDisabled = (run: Run) => {
    const baseline = selectedBaseRun();
    return baseline !== null && !hasSameIdentity(run, baseline);
  };

  const handleBaseChange = (e: Event) => {
    const val = (e.target as HTMLSelectElement).value;
    setSearchParams({ ...searchParams, base: val });
  };

  const handleCurrChange = (e: Event) => {
    const val = (e.target as HTMLSelectElement).value;
    setSearchParams({ ...searchParams, curr: val });
  };

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

    const md =
      "| Benchmark | Baseline | Current | Delta |\n|---|---|---|---|\n" +
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
        <h2 class="text-[14px] font-bold text-black uppercase tracking-widest">COMPARE</h2>
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
                  Baseline
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

            <div class="hidden lg:flex flex-col items-center justify-center text-text-muted font-semibold text-[11px] uppercase tracking-widest">
              <div class="w-8 h-[1px] bg-border"></div>
              <span class="my-3">VS</span>
              <div class="w-8 h-[1px] bg-border"></div>
            </div>

            <div class="flex flex-col gap-3">
              <div class="flex items-center justify-between">
                <label
                  for="current-select"
                  class="text-[11px] font-bold text-text-muted uppercase tracking-widest"
                >
                  Current
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
          JavaScript statistical inference is disabled; deltas are descriptive.
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
          <table class="w-full text-left border-collapse text-[12px] font-mono">
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
                  Current
                </th>
                <th
                  class="px-4 py-2.5 font-semibold text-right cursor-pointer hover:bg-bg-hover hover:text-text-main"
                  onClick={() => handleSort("change_percent")}
                >
                  Delta %
                </th>
              </tr>
            </thead>
            <tbody class="divide-y divide-bg-hover">
              <For each={sortedComparisons()}>
                {(c) => {
                  const isPos = c.change_percent > 0;
                  const isNeg = c.change_percent < 0;
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
