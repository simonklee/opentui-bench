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
    <div class="flex-none p-3 sm:px-6 border-b border-border bg-bg-dark flex flex-col md:flex-row md:justify-between md:items-center gap-3 md:gap-4 min-h-[57px]">
        <div class="flex flex-col gap-1 overflow-hidden">
            <h2 class="text-[14px] font-bold text-black uppercase tracking-widest flex items-center gap-2 sm:gap-3">
                Benchmarks
                {props.run && (
                    <span class="font-mono text-text-muted text-[11px] normal-case hidden sm:inline-block bg-bg-hover px-1.5 py-0.5 rounded-none">
                        #{props.run.commit_hash.substring(0,7)}
                    </span>
                )}
            </h2>
            {props.run && (
                <div class="text-[11px] text-text-muted font-mono truncate hidden md:block max-w-[400px]">
                    {props.run.commit_message}
                </div>
            )}
        </div>

        <div class="flex gap-2 w-full md:w-auto overflow-x-auto pb-1 md:pb-0 [&::-webkit-scrollbar]:hidden [-ms-overflow-style:'none'] [scrollbar-width:'none']">
            <input 
                type="text" 
                placeholder="FILTER..." 
                class="flex-1 min-w-[120px] md:w-[200px] px-3 py-1.5 border border-border rounded-none text-[11px] bg-white text-black focus:border-black outline-none shadow-none uppercase tracking-wide placeholder:text-text-muted transition-colors font-medium"
                value={props.filter}
                onInput={(e) => props.setFilter(e.currentTarget.value)}
                onKeyDown={(e) => { if (e.key === 'Escape') e.currentTarget.blur(); }}
            />
            <div class="relative flex-none">
                <select 
                    class="appearance-none pl-3 pr-8 py-1.5 border border-border rounded-none text-[11px] bg-white text-black outline-none cursor-pointer uppercase tracking-wide font-medium hover:border-black transition-colors"
                    value={props.category}
                    onChange={(e) => props.setCategory(e.currentTarget.value)}
                >
                    <option value="">All Categories</option>
                    <For each={props.categories}>
                        {c => <option value={c}>{c}</option>}
                    </For>
                </select>
                <div class="pointer-events-none absolute inset-y-0 right-0 flex items-center px-2 text-black">
                    <svg class="h-3 w-3 fill-current" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20"><path d="M9.293 12.95l.707.707L15.657 8l-1.414-1.414L10 10.828 5.757 6.586 4.343 8z"/></svg>
                </div>
            </div>
            
            <Button 
                onClick={props.onCopy}
                disabled={!props.hasResults}
                class="hidden sm:flex"
            >
                Copy
            </Button>
        </div>
    </div>
  );
};

export default BenchmarkFilterBar;
