import { For, Show } from "solid-js";
import type { Component } from "solid-js";
import { useNavigate } from "@solidjs/router";
import type { Run } from "../services/api";

interface RunsTableProps {
  runs: Run[] | undefined;
  loading: boolean;
}

const RunsTable: Component<RunsTableProps> = (props) => {
  const navigate = useNavigate();

  return (
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
            <For each={props.runs}>
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
        <Show when={props.loading}>
            <div class="absolute inset-0 flex items-center justify-center bg-bg-dark/50 z-20">
                <div class="w-6 h-6 border-[3px] border-bg-panel border-t-accent rounded-full animate-spin"></div>
            </div>
        </Show>
      </div>
  );
};

export default RunsTable;
