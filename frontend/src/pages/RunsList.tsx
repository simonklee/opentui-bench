import { createResource, For, Show } from "solid-js";
import type { Component } from "solid-js";
import { A, useNavigate } from "@solidjs/router";
import { api } from "../services/api";
import { formatDate } from "../utils/format";

const RunsList: Component = () => {
  const [runs] = createResource(() => api.getRuns(100));
  const navigate = useNavigate();

  return (
    <div class="flex flex-col h-full w-full">
      <div class="flex-none h-[57px] px-6 border-b border-border bg-bg-dark flex justify-between items-center">
        <h2 class="text-[16px] font-semibold text-text-main">Recorded Runs</h2>
      </div>
      
      {/* Stats Bar */}
      <div class="flex-none px-6 py-3 border-b border-border bg-bg-panel flex gap-6 text-[12px] text-text-muted">
         <div class="flex gap-1">
            <span>Total Runs:</span>
            <strong class="text-text-main font-semibold"><Show when={runs()} fallback="-">{runs()?.length}</Show></strong>
         </div>
         <div class="flex gap-1">
            <span>Latest:</span>
            <strong class="text-text-main font-semibold">
                <Show when={runs() && runs()![0]}>
                    {(run) => (
                        <a href={`https://github.com/anomalyco/opentui/commit/${run().commit_hash}`} target="_blank" class="font-mono text-accent hover:underline decoration-accent">
                            #{run().commit_hash.substring(0, 7)}
                        </a>
                    )}
                </Show>
                <Show when={!runs() || runs()!.length === 0}>-</Show>
            </strong>
         </div>
         <div class="flex gap-1">
            <span>Date:</span>
            <strong class="text-text-main font-semibold">
                <Show when={runs() && runs()![0]}>
                    {(run) => formatDate(run().run_date)}
                </Show>
                <Show when={!runs() || runs()!.length === 0}>-</Show>
            </strong>
         </div>
      </div>

      <div class="flex-1 overflow-auto bg-bg-dark relative">
        <table class="w-full text-left border-collapse text-[12px] font-mono">
          <thead class="bg-bg-panel sticky top-0 z-10 border-b border-border font-ui text-[11px] uppercase tracking-wider text-text-muted select-none">
            <tr>
              <th class="px-4 py-2.5 font-semibold">Commit</th>
              <th class="px-4 py-2.5 font-semibold">Message</th>
              <th class="px-4 py-2.5 font-semibold">Branch</th>
              <th class="px-4 py-2.5 font-semibold">Date</th>
              <th class="px-4 py-2.5 font-semibold">Results</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-bg-hover">
            <For each={runs()}>
              {(run) => (
                <tr 
                    class="hover:bg-bg-hover cursor-pointer group transition-colors duration-100"
                    onClick={() => navigate(`/benchmarks/${run.id}`)}
                >
                  <td class="px-4 py-2.5 text-text-main">
                    <a 
                        href={`https://github.com/anomalyco/opentui/commit/${run.commit_hash}`} 
                        target="_blank"
                        class="font-mono text-accent bg-bg-panel px-1.5 py-0.5 rounded border border-border text-[11px] no-underline hover:bg-white hover:border-accent"
                        onClick={(e) => e.stopPropagation()}
                    >
                        #{run.commit_hash.substring(0, 7)}
                    </a>
                  </td>
                  <td class="px-4 py-2.5 max-w-[300px] truncate font-medium text-text-main font-ui" title={run.commit_message}>
                     {run.commit_message}
                  </td>
                  <td class="px-4 py-2.5 text-text-main">{run.branch}</td>
                  <td class="px-4 py-2.5 text-text-main">{new Date(run.run_date).toLocaleString()}</td>
                  <td class="px-4 py-2.5 text-text-main">{run.result_count}</td>
                </tr>
              )}
            </For>
          </tbody>
        </table>
        <Show when={runs.loading}>
            <div class="absolute inset-0 flex items-center justify-center bg-bg-dark/50 z-20">
                <div class="w-6 h-6 border-[3px] border-bg-panel border-t-accent rounded-full animate-spin"></div>
            </div>
        </Show>
      </div>
    </div>
  );
};

export default RunsList;
