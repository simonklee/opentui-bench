import { createResource } from "solid-js";
import type { Component } from "solid-js";
import { api } from "../services/api";
import StatsBar from "../components/StatsBar";
import RunsTable from "../components/RunsTable";

const RunsList: Component = () => {
  const [runs] = createResource(() => api.getRuns(100));

  return (
    <div class="flex flex-col h-full w-full">
      <div class="flex-none h-[57px] px-6 border-b border-border bg-bg-dark flex justify-between items-center">
        <h2 class="text-[16px] font-semibold text-text-main">Recorded Runs</h2>
      </div>
      
      <StatsBar runs={runs()} loading={runs.loading} />
      <RunsTable runs={runs()} loading={runs.loading} />
    </div>
  );
};

export default RunsList;
