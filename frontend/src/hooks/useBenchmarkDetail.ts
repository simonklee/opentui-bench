import { createResource, createSignal, createMemo, createEffect } from "solid-js";
import { useParams, useSearchParams, useNavigate } from "@solidjs/router";
import { api } from "../services/api";
import type { BenchmarkResult, RegressionDFMode, TrendResponse } from "../services/api";
import { globalCategory, globalFilter, setGlobalCategory, setGlobalFilter } from "../store";
import { useFilteredBenchmarks } from "./useFilteredBenchmarks";
import { useFilterParams } from "./useFilterParams";

/** Returns true if the branch represents a non-main feature branch. */
function isBranchRun(branch: string | undefined | null): boolean {
  return !!branch && branch !== "" && branch !== "main";
}

function firstSearchParam(value: string | string[] | undefined): string | undefined {
  if (Array.isArray(value)) {
    return value[0];
  }
  return value;
}

function parseSearchParamInt(value: string | string[] | undefined): number | undefined {
  const raw = firstSearchParam(value);
  if (!raw) return undefined;
  const parsed = Number.parseInt(raw, 10);
  return Number.isNaN(parsed) ? undefined : parsed;
}

function parseSearchParamFloat(value: string | string[] | undefined): number | undefined {
  const raw = firstSearchParam(value);
  if (!raw) return undefined;
  const parsed = Number.parseFloat(raw);
  return Number.isNaN(parsed) ? undefined : parsed;
}

export interface RegressionNavigationContext {
  latestRunId: number;
  latestResultId: number;
  latestCommitHash: string;
  latestCommitHashFull?: string;
  latestRunDate?: string;
  introducedRunId?: number;
  introducedResultId?: number;
  introducedCommitHash?: string;
  introducedCommitHashFull?: string;
  introducedRunDate?: string;
  changePercent?: number;
  branch: string;
  dfMode: RegressionDFMode;
}

export function useBenchmarkDetail() {
  const params = useParams();
  const [searchParams, setSearchParams] = useSearchParams();
  const navigate = useNavigate();
  useFilterParams(searchParams, setSearchParams);

  const [run] = createResource(() => {
    if (!params.id) return undefined;
    const id = parseInt(params.id);
    return isNaN(id) ? undefined : id;
  }, api.getRunDetails);

  const [selectedBenchmarkId, setSelectedBenchmarkId] = createSignal<number | null>(null);
  const [sortBy, setSortBy] = createSignal<keyof BenchmarkResult | "mem_stats">("avg_ns");
  const [sortDesc, setSortDesc] = createSignal(false);
  const [hasCpuProfile, setHasCpuProfile] = createSignal(false);

  const buildSearchString = (overrides: Record<string, string | number | null | undefined>) => {
    const next = new URLSearchParams();
    for (const [key, value] of Object.entries(searchParams)) {
      const raw = firstSearchParam(value);
      if (raw !== undefined) {
        next.set(key, raw);
      }
    }
    for (const [key, value] of Object.entries(overrides)) {
      if (value === null || value === undefined) {
        next.delete(key);
      } else {
        next.set(key, String(value));
      }
    }
    return next.toString();
  };

  // Sync URL params with state
  createEffect(() => {
    const bid = searchParams.bench_id;
    if (typeof bid === "string") {
      setSelectedBenchmarkId(parseInt(bid));
    } else {
      setSelectedBenchmarkId(null);
    }
  });

  // Handle name-based navigation (e.g. from Compare view)
  createEffect(() => {
    const nameParam = searchParams.name;
    const r = run();
    if (typeof nameParam === "string" && r) {
      const found = r.results.find((b) => b.name === nameParam);
      if (found) {
        setSelectedBenchmarkId(found.id);
        setSearchParams({
          ...searchParams,
          bench_id: found.id,
          name: undefined,
        });
      }
    }
  });

  const { filteredResults: filteredData, categories } = useFilteredBenchmarks(() => run()?.results);

  const filteredResults = createMemo(() => {
    const data = filteredData();
    return [...data].sort((a, b) => {
      let va: number | string = 0;
      let vb: number | string = 0;
      const field = sortBy();

      if (field === "mem_stats") {
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

  const handleSort = (field: keyof BenchmarkResult | "mem_stats") => {
    if (sortBy() === field) {
      setSortDesc(!sortDesc());
    } else {
      setSortBy(field);
      setSortDesc(false);
    }
  };

  const selectBenchmark = (id: number) => {
    setSelectedBenchmarkId(id);
    setSearchParams({ ...searchParams, bench_id: id });
  };

  const closeDetail = () => {
    if (searchParams.from === "compare") {
      const base = searchParams.compare_base;
      const curr = searchParams.compare_curr;
      const params = new URLSearchParams();
      if (base) params.set("base", base as string);
      if (curr) params.set("curr", curr as string);
      navigate(`/compare?${params.toString()}`);
      return;
    }
    if (searchParams.from === "regressions") {
      const params = new URLSearchParams();
      const branch = firstSearchParam(searchParams.regression_branch);
      const dfMode = firstSearchParam(searchParams.regression_df_mode);
      if (branch && branch !== "main") {
        params.set("branch", branch);
      }
      if (dfMode && dfMode !== "baseline") {
        params.set("df_mode", dfMode);
      }
      navigate(params.toString() ? `/?${params.toString()}` : "/");
      return;
    }
    setSelectedBenchmarkId(null);
    setSearchParams({ ...searchParams, bench_id: null });
  };

  const navigateToBenchmark = (runId: number, resultId: number) => {
    const query = buildSearchString({ bench_id: resultId });
    navigate(`/benchmarks/${runId}${query ? `?${query}` : ""}`);
  };

  const regressionContext = createMemo<RegressionNavigationContext | undefined>(() => {
    if (firstSearchParam(searchParams.from) !== "regressions") {
      return undefined;
    }

    const latestRunId = parseSearchParamInt(searchParams.regression_run_id);
    const latestResultId = parseSearchParamInt(searchParams.regression_result_id);
    const latestCommitHash = firstSearchParam(searchParams.regression_commit_hash);
    if (!latestRunId || !latestResultId || !latestCommitHash) {
      return undefined;
    }

    const dfModeRaw = firstSearchParam(searchParams.regression_df_mode);
    return {
      latestRunId,
      latestResultId,
      latestCommitHash,
      latestCommitHashFull: firstSearchParam(searchParams.regression_commit_hash_full),
      latestRunDate: firstSearchParam(searchParams.regression_run_date),
      introducedRunId: parseSearchParamInt(searchParams.regression_intro_run_id),
      introducedResultId: parseSearchParamInt(searchParams.regression_intro_result_id),
      introducedCommitHash: firstSearchParam(searchParams.regression_intro_commit_hash),
      introducedCommitHashFull: firstSearchParam(searchParams.regression_intro_commit_hash_full),
      introducedRunDate: firstSearchParam(searchParams.regression_intro_run_date),
      changePercent: parseSearchParamFloat(searchParams.regression_change_pct),
      branch: firstSearchParam(searchParams.regression_branch) || "main",
      dfMode: dfModeRaw === "latest" ? "latest" : "baseline",
    };
  });

  const selectedBenchmark = createMemo(() => {
    return run()?.results.find((r) => r.id === selectedBenchmarkId());
  });

  // The branch of the current run (empty/null/"main" = main branch)
  const runBranch = createMemo(() => run()?.branch ?? "");
  const isOnBranch = createMemo(() => isBranchRun(runBranch()));

  // Primary trend data: always fetch main's history for baseline context
  // When on a branch, explicitly filter to main; otherwise fetch unfiltered (same as before)
  const [trendData] = createResource(
    () => {
      const name = selectedBenchmark()?.name;
      if (!name) return null;
      const branch = isOnBranch() ? "main" : undefined;
      return { name, limit: 100, branch };
    },
    async ({ name, limit, branch }) => {
      return api.getTrend(name, limit, branch);
    },
  );

  // Overlay trend data: only fetched when viewing a non-main branch
  const [branchTrendData] = createResource(
    () => {
      const name = selectedBenchmark()?.name;
      const branch = runBranch();
      if (!name || !isBranchRun(branch)) return null;
      return { name, limit: 100, branch };
    },
    async ({ name, limit, branch }) => {
      return api.getTrend(name, limit, branch);
    },
  );

  // Combined trend response: main data with branch overlay info
  const combinedTrendData = createMemo((): TrendResponse | undefined => {
    const main = trendData();
    if (!main) return undefined;
    return main;
  });

  // Check artifacts
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
        setHasCpuProfile(
          Array.isArray(artifacts) && artifacts.some((a: any) => a.kind === "cpu.pprof"),
        );
      } else {
        setHasCpuProfile(false);
      }
    } catch {
      setHasCpuProfile(false);
    }
  });

  return {
    run,
    filter: globalFilter,
    setFilter: setGlobalFilter,
    category: globalCategory,
    setCategory: setGlobalCategory,
    categories,
    filteredResults,
    sortBy,
    sortDesc,
    handleSort,
    selectedBenchmarkId,
    selectBenchmark,
    selectedBenchmark,
    trendData: combinedTrendData,
    branchTrendData,
    isOnBranch,
    runBranch,
    hasCpuProfile,
    closeDetail,
    navigateToBenchmark,
    regressionContext,
    navigate,
    searchParams,
  };
}
