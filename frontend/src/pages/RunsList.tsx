import { createResource, For, Show } from "solid-js";
import type { Component } from "solid-js";
import { api } from "../services/api";
import StatsBar from "../components/StatsBar";
import RunsTable from "../components/RunsTable";
import { benchmarkKind } from "../store";

const RunsList: Component = () => {
  const [runs] = createResource(benchmarkKind, (kind) => api.getRuns(100, kind));
  const [failedJobs] = createResource(benchmarkKind, (kind) =>
    kind === "js" ? api.getJobs(kind, "failed", 50, "automatic") : Promise.resolve([]),
  );

  return (
    <div class="flex flex-col h-full w-full">
      <div class="flex-none h-[57px] px-6 border-b border-border bg-bg-dark flex justify-between items-center">
        <h2 class="text-[14px] font-bold text-black uppercase tracking-widest">
          Recorded {benchmarkKind() === "js" ? "JavaScript" : "Zig"} Runs
        </h2>
      </div>

      <Show when={benchmarkKind() === "js" && (failedJobs()?.length ?? 0) > 0}>
        <details class="flex-none border-b border-danger/30 bg-red-50">
          <summary class="cursor-pointer px-4 py-2.5 sm:px-6 text-[10px] font-bold uppercase tracking-widest text-danger">
            Failed automatic JavaScript jobs ({failedJobs()?.length})
          </summary>
          <div class="max-h-32 overflow-y-auto border-t border-danger/20 px-4 py-2 sm:px-6">
            <For each={failedJobs()}>
              {(job) => (
                <div class="flex flex-col gap-0.5 py-1 text-[11px] font-mono sm:flex-row sm:items-baseline sm:gap-3">
                  <span class="font-bold whitespace-nowrap">
                    #{job.id} {job.branch}
                  </span>
                  <span class="text-text-muted truncate" title={job.error}>
                    {job.error || "Unknown error"}
                  </span>
                </div>
              )}
            </For>
          </div>
        </details>
      </Show>

      <StatsBar runs={runs()} loading={runs.loading} />
      <RunsTable runs={runs()} loading={runs.loading} />
    </div>
  );
};

export default RunsList;
