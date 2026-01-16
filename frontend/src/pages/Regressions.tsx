import { createResource, Show, For } from "solid-js";
import type { Component } from "solid-js";
import { useNavigate } from "@solidjs/router";
import { api } from "../services/api";
import type { Regression } from "../services/api";
import { formatNs } from "../utils/format";
import { Check, AlertTriangle, Loader2, ArrowRight } from "lucide-solid";

const formatRelativeDate = (dateStr: string): string => {
  const date = new Date(dateStr);
  const now = new Date();
  const diffMs = now.getTime() - date.getTime();
  const diffDays = Math.floor(diffMs / (1000 * 60 * 60 * 24));
  
  if (diffDays === 0) return "today";
  if (diffDays === 1) return "yesterday";
  if (diffDays < 7) return `${diffDays}d ago`;
  if (diffDays < 30) return `${Math.floor(diffDays / 7)}w ago`;
  return `${Math.floor(diffDays / 30)}mo ago`;
};

const RegressionRow: Component<{ regression: Regression; runId?: number | null }> = (props) => {
  const navigate = useNavigate();
  const reg = () => props.regression;

  const handleClick = () => {
    const targetRunId = props.runId ?? reg().baseline_run_id;
    // Navigate to the run that contains the latest result
    navigate(`/benchmarks/${targetRunId}?bench_id=${reg().latest_result_id}`);
  };

  return (
    <tr 
      class="border-b border-border hover:bg-bg-hover cursor-pointer transition-colors"
      onClick={handleClick}
    >
      <td class="py-3 px-4">
        <div class="font-medium text-[13px] text-black">{reg().name}</div>
        <div class="text-[11px] text-text-muted">{reg().category}</div>
      </td>
      <td class="py-3 px-4 font-mono text-[13px] text-danger">
        +{reg().change_percent.toFixed(1)}%
      </td>
      <td class="py-3 px-4">
        <div class="font-mono text-[12px] text-accent hover:underline">
          {reg().baseline_commit_hash?.slice(0, 7)}
        </div>
        <div class="text-[11px] text-text-muted">
          {formatNs(reg().baseline_ci_lower_ns)} - {formatNs(reg().baseline_ci_upper_ns)}
        </div>
      </td>
      <td class="py-3 px-4">
        <Show when={reg().introduced_commit_hash} fallback={
          <span class="text-text-muted text-[12px]">-</span>
        }>
          <div class="font-mono text-[12px] text-accent hover:underline">
            {reg().introduced_commit_hash?.slice(0, 7)}
          </div>
          <Show when={reg().introduced_run_date}>
            <div class="text-[11px] text-text-muted">
              {formatRelativeDate(reg().introduced_run_date!)}
            </div>
          </Show>
        </Show>
      </td>
      <td class="py-3 px-4 text-right">
        <ArrowRight size={16} class="text-text-muted inline-block" />
      </td>
    </tr>
  );
};

const Regressions: Component = () => {
  const [data] = createResource(() => api.getRegressions());

  const regressionCount = () => data()?.regressions?.length ?? 0;
  const hasRegressions = () => regressionCount() > 0;
  const insufficientHistory = () => !!data()?.insufficient_history;
  const showInsufficientHistory = () => insufficientHistory() && !hasRegressions();

  return (
    <div class="flex flex-col h-full w-full">
      {/* Header */}
      <div class="flex-none h-[57px] px-6 border-b border-border bg-bg-dark flex justify-between items-center">
        <h2 class="text-[14px] font-bold text-black uppercase tracking-widest">Regressions</h2>
      </div>

      {/* Status Row */}
      <div class="flex-none border-b border-border py-4 px-6 bg-white">
        <Show when={data.loading}>
          <div class="flex items-center gap-2 text-text-muted">
            <Loader2 size={18} class="animate-spin" />
            <span class="text-[14px]">Checking for regressions...</span>
          </div>
        </Show>

        <Show when={!data.loading && data()}>
          <Show when={showInsufficientHistory()} fallback={
            <Show when={hasRegressions()} fallback={
              <div class="flex items-center gap-2 text-success">
                <Check size={18} strokeWidth={3} />
                <span class="text-[14px] font-medium">All benchmarks healthy</span>
              </div>
            }>
              <div class="flex items-center gap-2 text-danger">
                <AlertTriangle size={18} />
                <span class="text-[14px] font-medium">
                  {regressionCount()} regression{regressionCount() !== 1 ? 's' : ''} detected
                </span>
              </div>
            </Show>
          }>
            <div class="flex items-center gap-2 text-warning">
              <AlertTriangle size={18} />
              <span class="text-[14px] font-medium">Not enough history for analysis</span>
            </div>
          </Show>
        </Show>

        <Show when={data.error}>
          <div class="flex items-center gap-2 text-warning">
            <AlertTriangle size={18} />
            <span class="text-[14px]">Unable to check regressions</span>
          </div>
        </Show>
      </div>

      {/* Regressions Table */}
      <div class="flex-1 overflow-auto">
        <Show when={hasRegressions()}>
          <table class="w-full">
            <thead class="sticky top-0 bg-white border-b border-border">
              <tr class="text-left text-[11px] text-text-muted uppercase tracking-wider">
                <th class="py-3 px-4 font-medium">Benchmark</th>
                <th class="py-3 px-4 font-medium">Change</th>
                <th class="py-3 px-4 font-medium">Baseline</th>
                <th class="py-3 px-4 font-medium">Introduced</th>
                <th class="py-3 px-4 font-medium w-10"></th>
              </tr>
            </thead>
            <tbody>
              <For each={data()?.regressions}>
                {(regression) => <RegressionRow regression={regression} runId={data()?.run_id} />}
              </For>
            </tbody>
          </table>
        </Show>

        <Show when={!data.loading && !hasRegressions() && !data.error && !showInsufficientHistory()}>
          <div class="flex items-center justify-center h-full text-text-muted">
            <div class="text-center">
              <div class="text-[14px] mb-2">No regressions to show</div>
              <div class="text-[12px]">
                Performance is stable across recent runs
              </div>
            </div>
          </div>
        </Show>
      </div>
    </div>
  );
};

export default Regressions;
