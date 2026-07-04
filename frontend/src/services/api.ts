export interface Run {
  id: number;
  commit_hash: string;
  commit_message: string;
  branch: string;
  run_date: string;
  result_count: number;
}

export interface BenchmarkResult {
  id: number;
  name: string;
  category: string;
  avg_ns: number;
  p50_ns: number;
  p99_ns: number;
  min_ns: number;
  max_ns: number;
  std_dev_ns: number;
  sample_count: number;
  iterations: number;
  mem_stats?: { name: string; bytes: number }[];
}

export interface RunDetails extends Run {
  results: BenchmarkResult[];
}

export interface TrendPoint {
  run_id: number;
  result_id: number;
  commit_hash: string;
  commit_message?: string;
  branch: string;
  run_date: string;
  avg_ns: number;
  median_ns: number; // Primary metric for regression detection (p50)
  min_ns: number;
  max_ns: number;
  std_dev_ns: number;
  sample_count: number;
  ci_lower_ns?: number;
  ci_upper_ns?: number;
  sem_ns?: number;
  regression_status?: "ok" | "regressed" | "baseline" | "insufficient";
  regression_reason?: string;
  baseline_run_id?: number;
  change_percent?: number;
}

export interface TrendResponse {
  points: TrendPoint[];
  change_points?: { run_id: number; magnitude_ns: number; p_value: number }[];
  global_shifts?: {
    run_id: number;
    positive_share: number;
    geo_increase_pct: number;
    compared_benchmarks: number;
  }[];
  epoch_run_id?: number;
  baseline_run_id?: number;
  baseline_ci_lower_ns?: number;
  baseline_ci_upper_ns?: number;
}

export type RegressionDFMode = "baseline" | "latest";

export interface CompareResult {
  comparisons: {
    name: string;
    category: string;
    baseline_ns: number;
    current_ns: number;
    change_percent: number;
    baseline_result_id: number;
    current_result_id: number;
  }[];
}

export interface Regression {
  name: string;
  category: string;
  latest_result_id: number;
  latest_ci_lower_ns: number;
  latest_ci_upper_ns: number;
  baseline_run_id: number;
  baseline_commit_hash: string;
  baseline_commit_hash_full: string;
  baseline_ci_lower_ns: number;
  baseline_ci_upper_ns: number;
  change_percent: number;
  min_effect_percent: number;
  p_value?: number;
  adjusted_p_value?: number;
  detection_method: "t_test" | "change_point";
  alpha: number;
  introduced_run_id?: number;
  introduced_result_id?: number;
  introduced_commit_hash?: string;
  introduced_commit_hash_full?: string;
  introduced_commit_message?: string;
  introduced_run_date?: string;
}

export interface RegressionsResponse {
  run_id: number | null;
  branch: string;
  window: number;
  compared_runs?: number;
  min_points: number;
  effective_min_points?: number;
  baseline_offset: number;
  df_mode?: RegressionDFMode;
  epoch_run_id?: number;
  total_benchmarks?: number;
  analyzed_benchmarks?: number;
  insufficient_history?: boolean;
  insufficient_reason?: string;
  exclusion_counts?: Record<string, number>;
  global_shift_detected?: boolean;
  global_shift_positive_share?: number;
  global_shift_geo_increase_pct?: number;
  global_shift_compared_benchmarks?: number;
  regressions: Regression[];
}

export interface RegressionHistoryEntry {
  run_id: number;
  commit_hash: string;
  commit_hash_full: string;
  commit_message: string;
  run_date: string;
  branch: string;
  cached: boolean;
  cached_at?: string;
  regression_count: number;
  compared_runs: number;
  min_points: number;
  effective_min_points: number;
  baseline_offset: number;
  epoch_run_id?: number;
  total_benchmarks: number;
  analyzed_benchmarks: number;
  insufficient_history: boolean;
  insufficient_reason?: string;
  global_shift_detected: boolean;
  global_shift_positive_share: number;
  global_shift_geo_increase_pct: number;
  global_shift_compared_benchmarks: number;
  regressions: Regression[];
}

export interface RegressionHistoryResponse {
  branch: string;
  window: number;
  min_points: number;
  baseline_offset: number;
  df_mode: RegressionDFMode;
  generation_key: string;
  scanned_runs: number;
  entry_count: number;
  cached_runs: number;
  computed_runs: number;
  entries: RegressionHistoryEntry[];
}

async function fetchJson<T>(url: string): Promise<T> {
  const res = await fetch(url);
  if (!res.ok) {
    throw new Error(`API call failed: ${res.status} ${res.statusText}`);
  }
  return (await res.json()) as T;
}

export const api = {
  getRuns: async (limit = 100) => {
    return fetchJson<Run[]>(`/api/runs?limit=${limit}`);
  },
  getRunDetails: async (id: number) => {
    return fetchJson<RunDetails>(`/api/runs/${id}`);
  },
  getCompare: async (baseId: number, currId: number) => {
    return fetchJson<CompareResult>(`/api/compare?id_a=${baseId}&id_b=${currId}`);
  },
  getTrend: async (resultId: number, limit = 100) => {
    return fetchJson<TrendResponse>(`/api/trend?result_id=${resultId}&limit=${limit}`);
  },
  getFlamegraphs: async (runId: number) => {
    return fetchJson<{ result_id: number; type: string }[]>(`/api/runs/${runId}/flamegraphs`);
  },
  getRegressions: async (
    runId?: number,
    options?: {
      window?: number;
      minPoints?: number;
      baselineOffset?: number;
      dfMode?: RegressionDFMode;
      branch?: string;
    },
  ) => {
    const params = new URLSearchParams();
    if (runId) {
      params.set("run_id", String(runId));
    }
    if (options?.branch) {
      params.set("branch", options.branch);
    }
    if (options?.window) {
      params.set("window", String(options.window));
    }
    if (options?.minPoints) {
      params.set("min_points", String(options.minPoints));
    }
    if (options?.baselineOffset !== undefined) {
      params.set("baseline_offset", String(options.baselineOffset));
    }
    if (options?.dfMode) {
      params.set("df_mode", options.dfMode);
    }
    const query = params.toString();
    const url = query ? `/api/regressions?${query}` : "/api/regressions";
    return fetchJson<RegressionsResponse>(url);
  },
  getRegressionHistory: async (options?: {
    window?: number;
    minPoints?: number;
    baselineOffset?: number;
    dfMode?: RegressionDFMode;
    branch?: string;
    limit?: number;
  }) => {
    const params = new URLSearchParams();
    if (options?.branch) {
      params.set("branch", options.branch);
    }
    if (options?.window) {
      params.set("window", String(options.window));
    }
    if (options?.minPoints) {
      params.set("min_points", String(options.minPoints));
    }
    if (options?.baselineOffset !== undefined) {
      params.set("baseline_offset", String(options.baselineOffset));
    }
    if (options?.dfMode) {
      params.set("df_mode", options.dfMode);
    }
    if (options?.limit) {
      params.set("limit", String(options.limit));
    }
    const query = params.toString();
    const url = query ? `/api/regressions/history?${query}` : "/api/regressions/history";
    return fetchJson<RegressionHistoryResponse>(url);
  },
  getBranches: async () => {
    return fetchJson<string[]>("/api/branches");
  },
};
