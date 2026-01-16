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
    run_date: string;
    avg_ns: number;
    min_ns: number;
    max_ns: number;
    std_dev_ns: number;
    sample_count: number;
    ci_lower_ns?: number;
    ci_upper_ns?: number;
    sem_ns?: number;
    regression_status?: "ok" | "regressed" | "baseline" | "insufficient";
    baseline_run_id?: number;
    change_percent?: number;
}

export interface TrendResponse {
    points: TrendPoint[];
    baseline_run_id?: number;
    baseline_ci_lower_ns?: number;
    baseline_ci_upper_ns?: number;
}

export interface CompareResult {
    comparisons: {
        name: string;
        category: string;
        baseline_ns: number;
        current_ns: number;
        change_percent: number;
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
    baseline_ci_lower_ns: number;
    baseline_ci_upper_ns: number;
    change_percent: number;
    min_effect_percent: number;
    p_value?: number;
    alpha: number;
    introduced_run_id?: number;
    introduced_commit_hash?: string;
    introduced_run_date?: string;
}

export interface RegressionsResponse {
    run_id: number | null;
    window: number;
    min_points: number;
    insufficient_history?: boolean;
    regressions: Regression[];
}

async function fetchJson<T>(url: string): Promise<T> {
    const res = await fetch(url);
    if (!res.ok) {
        throw new Error(`API call failed: ${res.status} ${res.statusText}`);
    }
    return await res.json() as T;
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
    getTrend: async (name: string, limit = 100) => {
        return fetchJson<TrendResponse>(`/api/trend?name=${encodeURIComponent(name)}&limit=${limit}`);
    },
    getFlamegraphs: async (runId: number) => {
        return fetchJson<{ result_id: number, type: string }[]>(`/api/runs/${runId}/flamegraphs`);
    },
    getRegressions: async (runId?: number) => {
        const url = runId ? `/api/regressions?run_id=${runId}` : '/api/regressions';
        return fetchJson<RegressionsResponse>(url);
    }
};
