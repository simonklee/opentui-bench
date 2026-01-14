import { createResource, createSignal, createMemo, createEffect } from "solid-js";
import { useParams, useSearchParams, useNavigate } from "@solidjs/router";
import { api } from "../services/api";
import type { BenchmarkResult } from "../services/api";

export function useBenchmarkDetail() {
    const params = useParams();
    const [searchParams, setSearchParams] = useSearchParams();
    const navigate = useNavigate();

    const [run] = createResource(() => {
        if (!params.id) return undefined;
        const id = parseInt(params.id);
        return isNaN(id) ? undefined : id;
    }, api.getRunDetails);

    const [filter, setFilter] = createSignal("");
    const [category, setCategory] = createSignal("");
    const [selectedBenchmarkId, setSelectedBenchmarkId] = createSignal<number | null>(null);
    const [sortBy, setSortBy] = createSignal<keyof BenchmarkResult | 'mem_stats'>("avg_ns");
    const [sortDesc, setSortDesc] = createSignal(false);
    const [hasCpuProfile, setHasCpuProfile] = createSignal(false);

    // Sync URL params with state
    createEffect(() => {
        const bid = searchParams.bench_id;
        if (typeof bid === 'string') {
            setSelectedBenchmarkId(parseInt(bid));
        } else {
            setSelectedBenchmarkId(null);
        }
    });

    // Handle name-based navigation (e.g. from Compare view)
    createEffect(() => {
        const nameParam = searchParams.name;
        const r = run();
        if (typeof nameParam === 'string' && r) {
            const found = r.results.find(b => b.name === nameParam);
            if (found) {
                setSelectedBenchmarkId(found.id);
                setSearchParams({
                    bench_id: found.id,
                    name: undefined
                });
            }
        }
    });

    const categories = createMemo(() => {
        const r = run();
        if (!r) return [];
        return [...new Set(r.results.map(i => i.category))];
    });

    const filteredResults = createMemo(() => {
        const r = run();
        if (!r) return [];
        let data = r.results;
        
        if (category()) data = data.filter(i => i.category === category());
        if (filter()) {
            const term = filter().toLowerCase();
            data = data.filter(i => i.name.toLowerCase().includes(term) || i.category.toLowerCase().includes(term));
        }
        
        return [...data].sort((a, b) => {
            let va: number | string = 0;
            let vb: number | string = 0;
            const field = sortBy();
            
            if (field === 'mem_stats') {
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

    const handleSort = (field: keyof BenchmarkResult | 'mem_stats') => {
        if (sortBy() === field) {
            setSortDesc(!sortDesc());
        } else {
            setSortBy(field);
            setSortDesc(false);
        }
    };

    const selectBenchmark = (id: number) => {
        setSelectedBenchmarkId(id);
        setSearchParams({ bench_id: id });
    };

    const closeDetail = () => {
        if (searchParams.from === 'compare') {
            const base = searchParams.compare_base;
            const curr = searchParams.compare_curr;
            const params = new URLSearchParams();
            if (base) params.set('base', base as string);
            if (curr) params.set('curr', curr as string);
            navigate(`/compare?${params.toString()}`);
            return;
        }
        setSelectedBenchmarkId(null);
        setSearchParams({ bench_id: null });
    };

    const selectedBenchmark = createMemo(() => {
        return run()?.results.find(r => r.id === selectedBenchmarkId());
    });

    const [trendData] = createResource(
        () => {
            const name = selectedBenchmark()?.name;
            return name ? { name, limit: 100 } : null;
        },
        ({ name, limit }) => api.getTrend(name, limit)
    );

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
                setHasCpuProfile(Array.isArray(artifacts) && artifacts.some((a: any) => a.kind === 'cpu.pprof'));
            } else {
                setHasCpuProfile(false);
            }
        } catch {
            setHasCpuProfile(false);
        }
    });

    return {
        run,
        filter, setFilter,
        category, setCategory,
        categories,
        filteredResults,
        sortBy, sortDesc, handleSort,
        selectedBenchmarkId, selectBenchmark,
        selectedBenchmark,
        trendData,
        hasCpuProfile,
        closeDetail,
        navigate,
        searchParams
    };
}
