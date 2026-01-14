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
  const thClass = "px-4 py-3 font-bold cursor-pointer hover:bg-black hover:text-white transition-colors select-none whitespace-nowrap border-r border-transparent last:border-0 hover:border-black";
  
  return (
    <div class="flex-1 overflow-auto bg-bg-dark">
        <table class="w-full text-left border-collapse text-[12px] font-mono">
            <thead class="bg-bg-dark sticky top-0 z-10 border-b-2 border-black font-ui text-[10px] uppercase tracking-widest text-text-main">
                <tr>
                    <th class={`${thClass} hidden sm:table-cell`} onClick={() => props.onSort('category')}>Cat</th>
                    <th class={thClass} onClick={() => props.onSort('name')}>Name</th>
                    <th class={`${thClass} text-right`} onClick={() => props.onSort('avg_ns')}>Avg</th>
                    <th class={`${thClass} text-right hidden lg:table-cell`} onClick={() => props.onSort('p50_ns')}>P50</th>
                    <th class={`${thClass} text-right hidden md:table-cell`} onClick={() => props.onSort('p99_ns')}>P99</th>
                    <th class={`${thClass} text-right hidden xl:table-cell`} onClick={() => props.onSort('min_ns')}>Min</th>
                    <th class={`${thClass} text-right hidden xl:table-cell`} onClick={() => props.onSort('max_ns')}>Max</th>
                    <th class={`${thClass} text-right hidden md:table-cell`} onClick={() => props.onSort('std_dev_ns')}>SD</th>
                    <th class={`${thClass} text-right hidden sm:table-cell`} onClick={() => props.onSort('mem_stats')}>Mem</th>
                    <th class={`${thClass} text-right hidden 2xl:table-cell`} onClick={() => props.onSort('sample_count')}>N</th>
                    <th class={`${thClass} text-right hidden 2xl:table-cell`} onClick={() => props.onSort('iterations')}>Iter</th>
                </tr>
            </thead>
            <tbody>
                <For each={props.results}>
                    {item => (
                        <tr 
                            class={`cursor-pointer transition-colors border-b border-border hover:bg-bg-hover group ${props.selectedId === item.id ? 'bg-black text-white hover:bg-black' : ''}`}
                            onClick={() => props.onSelect(item.id)}
                        >
                            <td class={`px-4 py-2 text-[11px] font-ui opacity-70 group-hover:opacity-100 hidden sm:table-cell ${props.selectedId === item.id ? 'opacity-100' : ''}`}>{item.category}</td>
                            <td class="px-4 py-2 font-medium max-w-[300px] truncate font-ui" title={item.name}>{item.name}</td>
                            <td class="px-4 py-2 text-right font-bold">{formatNs(item.avg_ns)}</td>
                            <td class="px-4 py-2 text-right opacity-80 hidden lg:table-cell">{formatNs(item.p50_ns)}</td>
                            <td class="px-4 py-2 text-right opacity-80 hidden md:table-cell">{formatNs(item.p99_ns)}</td>
                            <td class="px-4 py-2 text-right opacity-60 text-[11px] hidden xl:table-cell">{formatNs(item.min_ns)}</td>
                            <td class="px-4 py-2 text-right opacity-60 text-[11px] hidden xl:table-cell">{formatNs(item.max_ns)}</td>
                            <td class="px-4 py-2 text-right opacity-60 text-[11px] hidden md:table-cell">{formatNs(item.std_dev_ns)}</td>
                            <td class="px-4 py-2 text-right opacity-80 hidden sm:table-cell">
                                {formatBytes(item.mem_stats?.[0]?.bytes)}
                            </td>
                            <td class="px-4 py-2 text-right opacity-60 hidden 2xl:table-cell">{item.sample_count}</td>
                            <td class="px-4 py-2 text-right opacity-60 hidden 2xl:table-cell">{item.iterations || '-'}</td>
                        </tr>
                    )}
                </For>
            </tbody>
        </table>
    </div>
  );
};

export default BenchmarkResultsTable;
