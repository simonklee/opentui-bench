import { createResource, createSignal, For, Show, createMemo, createEffect } from "solid-js";
import type { Component } from "solid-js";
import { useSearchParams, useNavigate } from "@solidjs/router";
import { api } from "../services/api";
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
} from "../store";
import { useFilteredBenchmarks } from "../hooks/useFilteredBenchmarks";
import { useFilterParams } from "../hooks/useFilterParams";

const Compare: Component = () => {
  const [searchParams, setSearchParams] = useSearchParams();
  const navigate = useNavigate();
  useFilterParams(searchParams, setSearchParams);
  const [runs] = createResource(() => api.getRuns(100));
  const [copyToast, setCopyToast] = createSignal(false);

  // Check URL params at mount time (before any async operations) to avoid race conditions
  const urlParams = new URLSearchParams(window.location.search);
  const hadUrlParamsOnMount = urlParams.has("base") || urlParams.has("curr");
  const [didAutoSelect, setDidAutoSelect] = createSignal(false);

  // Auto-select runs only on first load with no URL params
  // Uses lastViewedRunId if available to maintain context
  createEffect(() => {
    const r = runs();
    // Skip if URL had params on mount, or we already auto-selected
    if (hadUrlParamsOnMount || didAutoSelect()) return;

    if (r && r.length > 0) {
      setDidAutoSelect(true);

      // Use lastViewedRunId as "current" if available, otherwise use latest run
      const contextRunId = lastViewedRunId();
      const current = contextRunId ?? r[0]!.id;

      // Find the previous run relative to "current" for baseline
      const currentIndex = r.findIndex((run) => run.id === current);
      const baseline =
        currentIndex >= 0 && currentIndex < r.length - 1
          ? r[currentIndex + 1]!.id // previous run in chronological order
          : r.length > 1
            ? r[1]!.id
            : current;

      setSearchParams(
        {
          ...searchParams,
          base: baseline,
          curr: current,
        },
        { replace: true },
      );
    }
  });

  // Keep as strings to match select option values
  const baseId = createMemo(() => {
    const val = searchParams.base;
    return typeof val === "string" ? val : "";
  });
  const currId = createMemo(() => {
    const val = searchParams.curr;
    return typeof val === "string" ? val : "";
  });

  const [compareData] = createResource(
    () => {
      const b = baseId();
      const c = currId();
      if (b && c) return { baseId: parseInt(b), currId: parseInt(c) };
      return null;
    },
    ({ baseId, currId }) => api.getCompare(baseId, currId),
  );
  const { filteredResults: filteredComparisons, categories } = useFilteredBenchmarks(
    () => compareData()?.comparisons ?? [],
  );

  const selectedBaseRun = createMemo(() => {
    const list = runs();
    const base = baseId();
    if (!list || !base) return null;
    const baseNum = parseInt(base, 10);
    if (Number.isNaN(baseNum)) return null;
    return list.find((run) => run.id === baseNum) ?? null;
  });

  const selectedCurrRun = createMemo(() => {
    const list = runs();
    const curr = currId();
    if (!list || !curr) return null;
    const currNum = parseInt(curr, 10);
    if (Number.isNaN(currNum)) return null;
    return list.find((run) => run.id === currNum) ?? null;
  });

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

  const handleBenchmarkClick = (benchmarkName: string) => {
    const curr = currId();
    const base = baseId();
    if (curr) {
      const params = new URLSearchParams();
      params.set("name", benchmarkName);
      params.set("from", "compare");
      if (base) params.set("compare_base", String(base));
      if (curr) params.set("compare_curr", String(curr));
      navigate(`/benchmarks/${curr}?${params.toString()}`);
    }
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
                    <For each={runs()}>
                      {(r) => (
                        <option value={String(r.id)}>
                          #{r.commit_hash.substring(0, 7)} · {r.commit_message?.substring(0, 50)}
                          {r.commit_message?.length > 50 ? "..." : ""}
                          {r.branch ? ` (${r.branch})` : ""}
                        </option>
                      )}
                    </For>
                  </select>
                  <Show when={selectedBaseRun()}>
                    {(run) => (
                      <div class="flex items-center justify-between text-[10px] text-text-muted">
                        <a
                          href={`https://github.com/anomalyco/opentui/commit/${run().commit_hash}`}
                          target="_blank"
                          class="font-mono text-text-main underline decoration-dotted underline-offset-2 hover:decoration-solid"
                        >
                          #{run().commit_hash.substring(0, 7)}
                        </a>
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
                  <For each={runs()}>
                    {(r) => (
                      <option value={String(r.id)}>
                        #{r.commit_hash.substring(0, 7)} · {r.commit_message?.substring(0, 50)}
                        {r.commit_message?.length > 50 ? "..." : ""}
                        {r.branch ? ` (${r.branch})` : ""}
                      </option>
                    )}
                  </For>
                </select>
                <Show when={selectedCurrRun()}>
                  {(run) => (
                    <div class="flex items-center justify-between text-[10px] text-text-muted">
                      <a
                        href={`https://github.com/anomalyco/opentui/commit/${run().commit_hash}`}
                        target="_blank"
                        class="font-mono text-text-main underline decoration-dotted underline-offset-2 hover:decoration-solid"
                      >
                        #{run().commit_hash.substring(0, 7)}
                      </a>
                      <span>{formatDate(run().run_date)}</span>
                    </div>
                  )}
                </Show>
              </div>
            </div>
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

      <div class="flex-1 overflow-auto bg-bg-dark">
        <Show
          when={compareData()}
          fallback={
            <div class="p-8 text-center text-text-muted text-[13px]">
              {baseId() && currId() ? "Loading comparison..." : "Select two runs to compare"}
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
                  const colorClass = isPos
                    ? "text-danger"
                    : isNeg
                      ? "text-success"
                      : "text-text-muted";

                  return (
                    <tr
                      class="hover:bg-bg-hover cursor-pointer"
                      onClick={() => handleBenchmarkClick(c.name)}
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
