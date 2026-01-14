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
}

export interface CompareResult {
    comparisons: {
        name: string;
        baseline_ns: number;
        current_ns: number;
        change_percent: number;
    }[];
}

export const api = {
    getRuns: async (limit = 100) => {
        const res = await fetch(`/api/runs?limit=${limit}`);
        return await res.json() as Run[];
    },
    getRunDetails: async (id: number) => {
        const res = await fetch(`/api/runs/${id}`);
        return await res.json() as RunDetails;
    },
    getCompare: async (baseId: number, currId: number) => {
        const res = await fetch(`/api/compare?id_a=${baseId}&id_b=${currId}`);
        return await res.json() as CompareResult;
    },
    getTrend: async (name: string, limit = 100) => {
        const res = await fetch(`/api/trend?name=${encodeURIComponent(name)}&limit=${limit}`);
        return await res.json() as TrendPoint[];
    },
    getFlamegraphs: async (runId: number) => {
        const res = await fetch(`/api/runs/${runId}/flamegraphs`);
        return await res.json() as { result_id: number, type: string }[];
    }
};
