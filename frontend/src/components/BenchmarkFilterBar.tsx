import { For } from "solid-js";
import type { Component } from "solid-js";
import { Button } from "./Button";

interface BenchmarkFilterBarProps {
  run: any;
  filter: string;
  setFilter: (v: string) => void;
  category: string;
  setCategory: (v: string) => void;
  categories: string[];
  resultCount: number;
  onCopy: () => void;
  hasResults: boolean;
}

const BenchmarkFilterBar: Component<BenchmarkFilterBarProps> = (props) => {
  return (
    <div class="flex-none p-4 px-6 border-b border-border bg-bg-dark flex justify-between items-center h-[57px]">
        <div>
            <h2 class="text-[16px] font-semibold text-text-main flex items-center gap-3">
                Benchmarks
                {props.run && (
                    <div class="text-[11px] text-text-muted flex gap-2 items-center font-normal">
                        <span class="font-mono text-accent">#{props.run.commit_hash.substring(0,7)}</span>
                        <span class="font-semibold text-text-main">{props.run.branch}</span>
                        <span>• {props.run.commit_message}</span>
                    </div>
                )}
            </h2>
        </div>
        <div class="flex gap-2">
            <input 
                type="text" 
                placeholder="Filter benchmarks..." 
                class="w-[240px] px-3 py-1.5 border border-border rounded-md text-[12px] bg-bg-panel text-text-main focus:bg-bg-dark focus:border-accent outline-none shadow-none"
                value={props.filter}
                onInput={(e) => props.setFilter(e.currentTarget.value)}
                onKeyDown={(e) => { if (e.key === 'Escape') e.currentTarget.blur(); }}
            />
            <select 
                class="px-3 py-1.5 pr-8 border border-border rounded-md text-[12px] bg-bg-dark text-text-main outline-none cursor-pointer appearance-none bg-[url('data:image/svg+xml;charset=UTF-8,%3csvg%20xmlns=\'http://www.w3.org/2000/svg\'%20viewBox=\'0%200%2024%2024\'%20fill=\'none\'%20stroke=\'currentColor\'%20stroke-width=\'2\'%20stroke-linecap=\'round\'%20stroke-linejoin=\'round\'%3e%3cpolyline%20points=\'6%209%2012%2015%2018%209\'%3e%3c/polyline%3e%3c/svg%3e')] bg-[length:12px] bg-[right_8px_center] bg-no-repeat"
                value={props.category}
                onChange={(e) => props.setCategory(e.currentTarget.value)}
            >
                <option value="">All Categories</option>
                <For each={props.categories}>
                    {c => <option value={c}>{c}</option>}
                </For>
            </select>
            <span class="text-[11px] font-semibold text-text-muted ml-2 flex items-center">{props.resultCount} RESULTS</span>
            <Button 
                onClick={props.onCopy}
                disabled={!props.hasResults}
            >
                Copy
            </Button>
        </div>
    </div>
  );
};

export default BenchmarkFilterBar;
