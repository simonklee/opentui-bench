export type BenchmarkKind = "zig" | "js";

export interface RunIdentity {
  benchmark_kind: BenchmarkKind;
  benchmark_suite: string;
  protocol_version: number;
  bun_version: string;
  zig_version: string;
  manifest_hash: string;
  manifest_json?: string;
  machine_id: string;
  zig_optimize: string;
}

export interface Run extends RunIdentity {
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
  sample_avg_variance_ns2: number | null;
  sample_data_version: number;
  summary_version: number;
  samples: {
    sample_index: number;
    avg_ns: number;
    inner_rsd_ppm?: number;
    batches?: { batch_index: number; iterations: number; elapsed_ns: number }[];
  }[];
  iterations: number;
  mem_stats?: { name: string; bytes: number }[];
}

export interface RunDetails extends Run {
  results: BenchmarkResult[];
}

export interface TrendPoint extends RunIdentity {
  run_id: number;
  result_id: number;
  commit_hash: string;
  commit_message?: string;
  branch: string;
  run_date: string;
  avg_ns: number;
  median_ns: number; // Descriptive p50 only
  min_ns: number;
  max_ns: number;
  std_dev_ns: number;
  sample_count: number;
  ci_lower_ns?: number; // Invocation-mean CI around avg_ns
  ci_upper_ns?: number;
  sem_ns?: number;
}

export interface TrendResponse {
  points: TrendPoint[];
  algorithm_version: string;
  metric: string;
  estimator: string;
  cohort_policy: string;
  family_definition: string;
  calibration_status: string;
  calibration_caveat: string;
  fdr_level: number;
  current_status: {
    run_id: number;
    status: "scored" | "insufficient" | "disabled";
    reason?: string;
  };
}

export interface CompareResult extends RunIdentity {
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

export interface Job extends Partial<RunIdentity> {
  id: number;
  status: string;
  kind: string;
  branch: string;
  commit_hash?: string;
  error?: string;
  created_at: string;
  completed_at?: string;
  requested_by?: string;
}

function withBenchmarkKind(path: string, kind: BenchmarkKind): string {
  const separator = path.includes("?") ? "&" : "?";
  return `${path}${separator}benchmark_kind=${kind}`;
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
  absolute_change_ns: number;
  baseline_ns: number;
  min_effect_percent: number;
  p_value?: number;
  adjusted_p_value?: number;
  detection_method: "log_avg_prediction_score";
  t_score?: number;
  degrees_of_freedom: number;
  change_point_diagnostic?: {
    run_id: number;
    p_value: number;
    effect_percent: number;
    magnitude_ns: number;
    recent: boolean;
  };
}

export interface BroadShiftIncident {
  detected: boolean;
  cause: "unclassified";
  positive_share: number;
  geometric_change_percent: number;
  compared_benchmarks: number;
  meaning: "many benchmarks moved together; cause unknown";
}

export interface RegressionsResponse {
  run_id: number | null;
  branch: string;
  window: number;
  compared_runs?: number;
  min_points: number;
  effective_min_points?: number;
  baseline_offset: number;
  algorithm_version: string;
  metric: string;
  estimator: string;
  cohort_policy: string;
  family_definition: string;
  calibration_status: string;
  calibration_caveat: string;
  fdr_level: number;
  hypothesis_count: number;
  total_benchmarks?: number;
  analyzed_benchmarks?: number;
  insufficient_history?: boolean;
  insufficient_reason?: string;
  exclusion_counts?: Record<string, number>;
  broad_shift: BroadShiftIncident;
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
  total_benchmarks: number;
  analyzed_benchmarks: number;
  insufficient_history: boolean;
  insufficient_reason?: string;
  broad_shift: BroadShiftIncident;
  regressions: Regression[];
}

export interface RegressionHistoryResponse {
  branch: string;
  window: number;
  min_points: number;
  baseline_offset: number;
  algorithm_version: string;
  metric: string;
  estimator: string;
  cohort_policy: string;
  family_definition: string;
  calibration_status: string;
  calibration_caveat: string;
  fdr_level: number;
  generation_key: string;
  scanned_runs: number;
  entry_count: number;
  cached_runs: number;
  computed_runs: number;
  entries: RegressionHistoryEntry[];
}

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
  ) {
    super(message);
  }
}

async function fetchJson<T>(url: string): Promise<T> {
  const res = await fetch(url);
  if (!res.ok) {
    throw new ApiError(`API call failed: ${res.status} ${res.statusText}`, res.status);
  }
  return (await res.json()) as T;
}

export const api = {
  getRuns: async (limit = 100, kind: BenchmarkKind = "zig", identity?: RunIdentity) => {
    const params = new URLSearchParams({ benchmark_kind: kind, limit: String(limit) });
    if (identity) {
      params.set("benchmark_suite", identity.benchmark_suite);
      params.set("protocol_version", String(identity.protocol_version));
      params.set("bun_version", identity.bun_version);
      params.set("zig_version", identity.zig_version);
      params.set("manifest_hash", identity.manifest_hash);
      params.set("machine_id", identity.machine_id);
      if (identity.benchmark_kind === "zig") {
        params.set("zig_optimize", identity.zig_optimize);
      }
    }
    return fetchJson<Run[]>(`/api/runs?${params}`);
  },
  getRunDetails: async (id: number, kind: BenchmarkKind = "zig") => {
    return fetchJson<RunDetails>(withBenchmarkKind(`/api/runs/${id}`, kind));
  },
  getLatest: async (kind: BenchmarkKind = "zig", branch = "") => {
    const params = new URLSearchParams({ benchmark_kind: kind });
    if (branch) params.set("branch", branch);
    return fetchJson<RunIdentity & { commit_hash: string | null; commit_hash_full: string | null }>(
      `/api/latest-commit?${params}`,
    );
  },
  getCatalog: async (kind: BenchmarkKind = "zig") => {
    return fetchJson<(RunIdentity & { category: string; name: string })[]>(
      withBenchmarkKind("/api/benchmarks", kind),
    );
  },
  getCompare: async (baseId: number, currId: number, kind: BenchmarkKind = "zig") => {
    return fetchJson<CompareResult>(
      withBenchmarkKind(`/api/compare?id_a=${baseId}&id_b=${currId}`, kind),
    );
  },
  getTrend: async (resultId: number, limit = 100, kind: BenchmarkKind = "zig") => {
    return fetchJson<TrendResponse>(
      withBenchmarkKind(`/api/trend?result_id=${resultId}&limit=${limit}`, kind),
    );
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
      branch?: string;
    },
  ) => {
    const params = new URLSearchParams({ benchmark_kind: "zig" });
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
    const query = params.toString();
    const url = query ? `/api/regressions?${query}` : "/api/regressions";
    return fetchJson<RegressionsResponse>(url);
  },
  getRegressionHistory: async (options?: {
    window?: number;
    minPoints?: number;
    baselineOffset?: number;
    branch?: string;
    limit?: number;
  }) => {
    const params = new URLSearchParams({ benchmark_kind: "zig" });
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
    if (options?.limit) {
      params.set("limit", String(options.limit));
    }
    const query = params.toString();
    const url = query ? `/api/regressions/history?${query}` : "/api/regressions/history";
    return fetchJson<RegressionHistoryResponse>(url);
  },
  getBranches: async (kind: BenchmarkKind = "zig") => {
    return fetchJson<string[]>(withBenchmarkKind("/api/branches", kind));
  },
  getJobs: async (kind: BenchmarkKind, status?: string, limit = 50, requestedBy?: string) => {
    const params = new URLSearchParams({ benchmark_kind: kind, limit: String(limit) });
    if (status) params.set("status", status);
    if (requestedBy) params.set("requested_by", requestedBy);
    return fetchJson<Job[]>(`/api/jobs?${params}`);
  },
};
