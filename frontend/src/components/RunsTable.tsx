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
  const thClass = "px-4 py-3 font-bold select-none whitespace-nowrap";

  return (
    <div class="flex-1 overflow-auto bg-bg-dark relative">
        <table class="w-full text-left border-collapse text-[12px] font-mono">
          <thead class="bg-bg-dark sticky top-0 z-10 border-b-2 border-black font-ui text-[10px] uppercase tracking-widest text-text-main">
            <tr>
              <th class={thClass}>Commit</th>
              <th class={thClass}>Message</th>
              <th class={thClass}>Branch</th>
              <th class={thClass}>Date</th>
              <th class={thClass}>Results</th>
            </tr>
          </thead>
          <tbody>
            <For each={props.runs}>
              {(run) => (
                <tr 
                    class="hover:bg-bg-hover cursor-pointer transition-colors duration-75 border-b border-border group"
                    onClick={() => navigate(`/benchmarks/${run.id}`)}
                >
                  <td class="px-4 py-3">
                    <a 
                        href={`https://github.com/anomalyco/opentui/commit/${run.commit_hash}`} 
                        target="_blank"
                        class="font-mono text-[11px] underline decoration-dotted hover:decoration-solid underline-offset-2 text-text-main"
                        onClick={(e) => e.stopPropagation()}
                    >
                        {run.commit_hash.substring(0, 7)}
                    </a>
                  </td>
                  <td class="px-4 py-3 max-w-[400px] truncate font-medium font-ui" title={run.commit_message}>
                     {run.commit_message}
                  </td>
                  <td class="px-4 py-3 opacity-80">{run.branch}</td>
                  <td class="px-4 py-3 opacity-80">{new Date(run.run_date).toLocaleString()}</td>
                  <td class="px-4 py-3 font-bold">{run.result_count}</td>
                </tr>
              )}
            </For>
          </tbody>
        </table>
        <Show when={props.loading}>
            <div class="absolute inset-0 flex items-center justify-center bg-white/80 z-20">
                <div class="w-6 h-6 border-[2px] border-black border-t-transparent rounded-full animate-spin"></div>
            </div>
        </Show>
      </div>
  );
};

export default RunsTable;
