import { createMemo, createResource, For, Show } from "solid-js";
import type { Component } from "solid-js";
import { api } from "../services/api";
import StatsBar from "../components/StatsBar";
import RunsTable from "../components/RunsTable";
import { benchmarkKind } from "../store";

const RunsList: Component = () => {
  const [runs] = createResource(benchmarkKind, (kind) => api.getRuns(100, kind));
  const [failedJobs] = createResource(benchmarkKind, (kind) =>
    kind === "js" ? api.getJobs(kind, "failed", 50) : Promise.resolve([]),
  );
  const failedAutomaticJobs = createMemo(() =>
    (failedJobs() ?? []).filter((job) => job.requested_by === "automatic"),
  );

  return (
    <div class="flex flex-col h-full w-full">
      <div class="flex-none h-[57px] px-6 border-b border-border bg-bg-dark flex justify-between items-center">
        <h2 class="text-[14px] font-bold text-black uppercase tracking-widest">
          Recorded {benchmarkKind() === "js" ? "JavaScript" : "Zig"} Runs
        </h2>
      </div>

      <Show when={benchmarkKind() === "js" && failedAutomaticJobs().length > 0}>
        <div class="flex-none border-b border-danger/30 bg-red-50 px-4 sm:px-6 py-3">
          <div class="text-[10px] font-bold uppercase tracking-widest text-danger mb-1">
            Failed automatic JavaScript jobs
          </div>
          <For each={failedAutomaticJobs()}>
            {(job) => (
              <div class="flex flex-col sm:flex-row sm:items-baseline gap-1 sm:gap-3 text-[11px] font-mono">
                <span class="font-bold">
                  #{job.id} {job.branch}
                </span>
                <span class="text-text-muted truncate" title={job.error}>
                  {job.error || "Unknown error"}
                </span>
              </div>
            )}
          </For>
        </div>
      </Show>

      <StatsBar runs={runs()} loading={runs.loading} />
      <RunsTable runs={runs()} loading={runs.loading} />
    </div>
  );
};

export default RunsList;
