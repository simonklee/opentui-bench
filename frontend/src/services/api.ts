import { refreshAuthSession } from "./auth";

export type BenchmarkKind = "zig" | "js";
export type JSRuntime = "bun" | "node";
export type JSRuntimeFilter = JSRuntime | "all";

export interface RunIdentity {
  benchmark_kind: BenchmarkKind;
  benchmark_suite: string;
  protocol_version: number;
  js_runtime?: JSRuntime;
  runtime_version?: string;
  /** @deprecated Use runtime_version. */
  bun_version?: string;
  zig_version: string;
  manifest_hash: string;
  manifest_json?: string;
  machine_id: string;
  zig_optimize: string;
}

export interface Run extends RunIdentity {
  id: number;
  commit_hash: string;
  commit_hash_full?: string;
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

export interface CaptureStatus {
  result_count: number;
  profile_count: number;
  complete: boolean;
  missing_reason?: string;
}

export interface RunDetails extends Run {
  results: BenchmarkResult[];
  purpose?: string;
  capture?: CaptureStatus;
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
    speed_ratio?: number;
  }[];
}

export interface RuntimeComparison {
  category: string;
  name: string;
  baseline_result_id: number;
  compared_result_id: number;
  baseline_ns: number;
  compared_ns: number;
  duration_change_percent: number;
  speed_ratio: number;
}

export interface RuntimeCompareResponse {
  metric: "p50_ns";
  lower_is_better: true;
  baseline: RunIdentity & { id: number; commit_hash: string; commit_hash_full?: string };
  compared: RunIdentity & { id: number; commit_hash: string; commit_hash_full?: string };
  comparisons: RuntimeComparison[];
}

export interface RuntimeTrendResponse {
  baseline_runtime: JSRuntime;
  baseline_runtime_version?: string;
  compared_runtime: JSRuntime;
  compared_runtime_version?: string;
  pairs: {
    baseline_result_id: number | null;
    compared_result_id: number | null;
    commit_hash: string;
    run_date: string;
    baseline_p50_ns: number | null;
    compared_p50_ns: number | null;
  }[];
}

export function runtimeName(identity: Pick<RunIdentity, "js_runtime">): string {
  return identity.js_runtime === "node" ? "Node" : "Bun";
}

export function runtimeVersion(identity: RunIdentity): string {
  return identity.runtime_version || identity.bun_version || "unknown";
}

export interface Job extends Partial<RunIdentity> {
  id: number;
  status: string;
  kind: string;
  branch: string;
  commit_hash?: string;
  samples: number;
  profile: string;
  notes?: string;
  error?: string;
  created_at: string;
  started_at?: string;
  completed_at?: string;
  run_id?: number;
  requested_by?: string;
  category?: string;
  name?: string;
  attempt_key?: string;
  investigation_id?: number;
  baseline_commit?: string;
  role?: string;
}

export interface Investigation {
  id: number;
  identity_key: string;
  trigger_result_id?: number;
  category: string;
  name: string;
  benchmark_kind: string;
  baseline_commit: string;
  target_commit: string;
  statistical_reference_run_id?: number;
  status: string;
  created_at: string;
  updated_at: string;
}

export interface InvestigationAttempt {
  id: number;
  attempt_key: string;
  role: string;
  commit_hash: string;
  branch: string;
  samples: number;
  profile: string;
  job_id?: number;
  run_id?: number;
  baseline_run_id?: number;
  target_run_id?: number;
  status: string;
  recipe_json?: string;
  error?: string;
  created_at: string;
  updated_at: string;
}

export interface InvestigationEvidence {
  investigation: Investigation;
  attempts: InvestigationAttempt[];
  timing: {
    metric: string;
    quantity: string;
    lower_is_better: boolean;
    baseline_ns?: number;
    target_ns?: number;
    candidate_ns?: number;
    change_percent?: number;
    reproduced?: boolean;
    comparison_status?: string;
    comparison_attempt_id?: number;
    comparison_attempt_role?: string;
    insufficient_reason?: string;
    candidate_improved?: boolean;
    candidate_comparison_status?: string;
    candidate_change_percent?: number;
    candidate_baseline_change_percent?: number;
    baseline_run_id?: number;
    target_run_id?: number;
    candidate_run_id?: number;
  };
  profiles: {
    quantity: string;
    unit: string;
    capture_scope: string;
    meaning: string;
    status?: string;
    missing_reason?: string;
    error?: string;
    baseline?: { insufficient_reason?: string; sample_type: string; sample_unit: string };
    target?: { insufficient_reason?: string; sample_type: string; sample_unit: string };
    functions?: {
      name: string;
      baseline_samples: number;
      target_samples: number;
      baseline_share: number;
      target_share: number;
      sample_delta: number;
      share_delta: number;
      quantity: string;
    }[];
  };
  actions: string[];
  uncalibrated_regression_score: boolean;
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
  remaining_runs: number;
  complete: boolean;
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

async function postJson<T>(url: string, body: unknown): Promise<T> {
  const res = await fetch(url, {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    const text = await res.text();
    if (res.status === 401) await refreshAuthSession();
    throw new ApiError(
      res.status === 401
        ? "Sign in with GitHub, then retry. Your pending submission is kept."
        : text || `API call failed: ${res.status}`,
      res.status,
    );
  }
  return (await res.json()) as T;
}

export const api = {
  getRuns: async (
    limit = 100,
    kind: BenchmarkKind = "zig",
    identity?: RunIdentity,
    jsRuntime?: JSRuntimeFilter,
  ) => {
    const params = new URLSearchParams({ benchmark_kind: kind, limit: String(limit) });
    if (kind === "js" && jsRuntime && jsRuntime !== "all") params.set("js_runtime", jsRuntime);
    if (identity) {
      params.set("benchmark_suite", identity.benchmark_suite);
      params.set("protocol_version", String(identity.protocol_version));
      if (identity.js_runtime) params.set("js_runtime", identity.js_runtime);
      if (identity.runtime_version) params.set("runtime_version", identity.runtime_version);
      else if (identity.bun_version) params.set("bun_version", identity.bun_version);
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
  getLatest: async (
    kind: BenchmarkKind = "zig",
    branch = "",
    jsRuntime: JSRuntimeFilter = "bun",
  ) => {
    const params = new URLSearchParams({ benchmark_kind: kind });
    if (kind === "js") params.set("js_runtime", jsRuntime);
    if (branch) params.set("branch", branch);
    return fetchJson<RunIdentity & { commit_hash: string | null; commit_hash_full: string | null }>(
      `/api/latest-commit?${params}`,
    );
  },
  getCatalog: async (kind: BenchmarkKind = "zig", jsRuntime: JSRuntimeFilter = "bun") => {
    const params = new URLSearchParams({ benchmark_kind: kind });
    if (kind === "js") params.set("js_runtime", jsRuntime);
    return fetchJson<(RunIdentity & { category: string; name: string })[]>(
      `/api/benchmarks?${params}`,
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
  getRuntimeCompare: async (baselineRunId: number, comparedRunId: number) => {
    const params = new URLSearchParams({
      baseline_run_id: String(baselineRunId),
      compared_run_id: String(comparedRunId),
    });
    return fetchJson<RuntimeCompareResponse>(`/api/runtime-compare?${params}`);
  },
  getRuntimeTrend: async (
    resultId: number,
    baselineRuntime: JSRuntime,
    comparedRuntime: JSRuntime,
    limit = 100,
  ) => {
    const params = new URLSearchParams({
      result_id: String(resultId),
      baseline_runtime: baselineRuntime,
      compared_runtime: comparedRuntime,
      limit: String(limit),
    });
    return fetchJson<RuntimeTrendResponse>(`/api/runtime-trend?${params}`);
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
  getJobs: async (
    kind?: BenchmarkKind,
    status?: string,
    limit = 50,
    requestedBy?: string,
    jsRuntime: JSRuntimeFilter = "bun",
  ) => {
    const params = new URLSearchParams({ limit: String(limit) });
    if (kind) params.set("benchmark_kind", kind);
    if (kind === "js" && jsRuntime !== "all") params.set("js_runtime", jsRuntime);
    if (status) params.set("status", status);
    if (requestedBy) params.set("requested_by", requestedBy);
    return fetchJson<Job[]>(`/api/jobs?${params}`);
  },
  getInvestigations: async (limit = 50) => {
    return fetchJson<Investigation[]>(`/api/investigations?limit=${limit}`);
  },
  getInvestigationEvidence: async (id: number) => {
    return fetchJson<InvestigationEvidence>(`/api/investigations/${id}/evidence`);
  },
  createInvestigation: async (body: {
    trigger_result_id: number;
    statistical_reference_run_id?: number;
    baseline_commit?: string;
    target_commit?: string;
    requested_by?: string;
  }) => {
    return postJson<{
      investigation: Investigation;
      created: boolean;
      attempt?: InvestigationAttempt;
      job?: Job;
    }>("/api/investigations", body);
  },
  createInvestigationAttempt: async (
    investigationId: number,
    body: { attempt_key: string; samples?: number; profile?: string },
  ) => {
    return postJson<{ attempt: InvestigationAttempt; job: Job; created: boolean }>(
      `/api/investigations/${investigationId}/attempts`,
      body,
    );
  },
  submitCandidate: async (
    investigationId: number,
    body: { commit_hash: string; branch?: string; attempt_key?: string },
  ) => {
    return postJson<{ attempt: InvestigationAttempt; job: Job; created: boolean }>(
      `/api/investigations/${investigationId}/candidates`,
      body,
    );
  },
};
