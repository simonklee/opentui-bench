import { For } from "solid-js";
import type { Component } from "solid-js";
import { formatNs, formatBytes } from "../utils/format";
import type { BenchmarkResult } from "../services/api";

interface BenchmarkResultsTableProps {
  results: BenchmarkResult[];
  selectedId: number | null;
  onSelect: (id: number) => void;
  sortBy: keyof BenchmarkResult | 'mem_stats';
  sortDesc: boolean;
  onSort: (field: keyof BenchmarkResult | 'mem_stats') => void;
}

const BenchmarkResultsTable: Component<BenchmarkResultsTableProps> = (props) => {
  return (
    <div class="flex-1 overflow-auto bg-bg-dark">
        <table class="w-full text-left border-collapse text-[12px] font-mono">
            <thead class="bg-bg-panel sticky top-0 z-10 border-b border-border font-ui text-[11px] uppercase tracking-wider text-text-muted select-none">
                <tr>
                    <th class="px-4 py-2.5 font-semibold cursor-pointer hover:bg-bg-hover hover:text-text-main" onClick={() => props.onSort('category')}>Cat</th>
                    <th class="px-4 py-2.5 font-semibold cursor-pointer hover:bg-bg-hover hover:text-text-main" onClick={() => props.onSort('name')}>Name</th>
                    <th class="px-4 py-2.5 font-semibold text-right cursor-pointer hover:bg-bg-hover hover:text-text-main" onClick={() => props.onSort('avg_ns')}>Avg</th>
                    <th class="px-4 py-2.5 font-semibold text-right cursor-pointer hover:bg-bg-hover hover:text-text-main" onClick={() => props.onSort('p50_ns')}>P50</th>
                    <th class="px-4 py-2.5 font-semibold text-right cursor-pointer hover:bg-bg-hover hover:text-text-main" onClick={() => props.onSort('p99_ns')}>P99</th>
                    <th class="px-4 py-2.5 font-semibold text-right cursor-pointer hover:bg-bg-hover hover:text-text-main" onClick={() => props.onSort('min_ns')}>Min</th>
                    <th class="px-4 py-2.5 font-semibold text-right cursor-pointer hover:bg-bg-hover hover:text-text-main" onClick={() => props.onSort('max_ns')}>Max</th>
                    <th class="px-4 py-2.5 font-semibold text-right cursor-pointer hover:bg-bg-hover hover:text-text-main" onClick={() => props.onSort('std_dev_ns')}>StdDev</th>
                    <th class="px-4 py-2.5 font-semibold text-right cursor-pointer hover:bg-bg-hover hover:text-text-main" onClick={() => props.onSort('mem_stats')}>Mem</th>
                    <th class="px-4 py-2.5 font-semibold text-right cursor-pointer hover:bg-bg-hover hover:text-text-main" onClick={() => props.onSort('sample_count')}>N</th>
                    <th class="px-4 py-2.5 font-semibold text-right cursor-pointer hover:bg-bg-hover hover:text-text-main" onClick={() => props.onSort('iterations')}>Iter</th>
                </tr>
            </thead>
            <tbody class="divide-y divide-bg-hover">
                <For each={props.results}>
                    {item => (
                        <tr 
                            class={`hover:bg-bg-hover cursor-pointer ${props.selectedId === item.id ? 'bg-bg-panel' : ''}`}
                            onClick={() => props.onSelect(item.id)}
                        >
                            <td class="px-4 py-2.5 text-text-muted text-[11px] font-ui">{item.category}</td>
                            <td class="px-4 py-2.5 font-medium text-text-main max-w-[300px] truncate font-ui" title={item.name}>{item.name}</td>
                            <td class="px-4 py-2.5 text-right font-semibold text-accent">{formatNs(item.avg_ns)}</td>
                            <td class="px-4 py-2.5 text-right text-text-muted">{formatNs(item.p50_ns)}</td>
                            <td class="px-4 py-2.5 text-right text-text-muted">{formatNs(item.p99_ns)}</td>
                            <td class="px-4 py-2.5 text-right text-text-muted">{formatNs(item.min_ns)}</td>
                            <td class="px-4 py-2.5 text-right text-text-muted">{formatNs(item.max_ns)}</td>
                            <td class="px-4 py-2.5 text-right text-text-muted">{formatNs(item.std_dev_ns)}</td>
                            <td class="px-4 py-2.5 text-right text-text-muted">
                                {formatBytes(item.mem_stats?.[0]?.bytes)}
                            </td>
                            <td class="px-4 py-2.5 text-right text-text-main">{item.sample_count}</td>
                            <td class="px-4 py-2.5 text-right text-text-main">{item.iterations || '-'}</td>
                        </tr>
                    )}
                </For>
            </tbody>
        </table>
    </div>
  );
};

export default BenchmarkResultsTable;
