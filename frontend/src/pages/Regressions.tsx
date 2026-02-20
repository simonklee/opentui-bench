import { createResource, createSignal, Show, For } from "solid-js";
import type { Component } from "solid-js";
import { useNavigate, useSearchParams } from "@solidjs/router";
import { api } from "../services/api";
import type { Regression, RegressionDFMode } from "../services/api";
import { formatNs } from "../utils/format";
import { Check, AlertTriangle, Loader2, ArrowRight, GitBranch } from "lucide-solid";

type DetectionFilter = "all" | "t_test" | "change_point";
type SensitivityMode = "balanced" | "conservative";

type RegressionWithContext = Regression & {
  run_id: number;
  run_date: string;
  commit_hash: string;
  commit_hash_full: string;
  commit_message: string;
  branch: string;
};

const GITHUB_REPO_URL = "https://github.com/anomalyco/opentui";

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

const truncateMessage = (msg: string, maxLen = 50): string => {
  const firstLine = msg.split("\n")[0] ?? "";
  if (firstLine.length <= maxLen) return firstLine;
  return firstLine.slice(0, maxLen - 1) + "…";
};

const CommitLink: Component<{ hash?: string; hashFull?: string }> = (props) => {
  const handleClick = (e: MouseEvent) => {
    e.stopPropagation();
  };

  return (
    <Show when={props.hash && props.hashFull}>
      <a
        href={`${GITHUB_REPO_URL}/commit/${props.hashFull}`}
        target="_blank"
        rel="noopener noreferrer"
        class="font-mono text-[12px] text-accent hover:underline"
        onClick={handleClick}
      >
        {props.hash?.slice(0, 7)}
      </a>
    </Show>
  );
};

const RegressionRow: Component<{ regression: RegressionWithContext }> = (props) => {
  const navigate = useNavigate();
  const reg = () => props.regression;

  const handleClick = () => {
    // Navigate to the run that introduced the regression
    const targetRunId = reg().introduced_run_id ?? reg().run_id ?? reg().baseline_run_id;
    const targetResultId = reg().introduced_result_id ?? reg().latest_result_id;
    navigate(`/benchmarks/${targetRunId}?bench_id=${targetResultId}`);
  };

  return (
    <tr
      class="border-b border-border hover:bg-bg-hover cursor-pointer transition-colors"
      onClick={handleClick}
    >
      <td class="py-3 px-4 min-w-[180px]">
        <div class="flex items-baseline gap-1.5">
          <CommitLink hash={reg().commit_hash} hashFull={reg().commit_hash_full} />
          <span class="text-[11px] text-text-muted">{formatRelativeDate(reg().run_date)}</span>
        </div>
        <Show when={reg().commit_message}>
          <div class="text-[11px] text-text-muted truncate max-w-[240px]">
            {truncateMessage(reg().commit_message)}
          </div>
        </Show>
      </td>
      <td class="py-3 px-4">
        <div class="font-medium text-[13px] text-black">{reg().name}</div>
        <div class="text-[11px] text-text-muted">{reg().category}</div>
      </td>
      <td class="py-3 px-4 font-mono text-[13px] text-danger">
        <div>+{reg().change_percent.toFixed(1)}%</div>
        <Show when={reg().adjusted_p_value !== undefined}>
          {(() => {
            const adjusted = reg().adjusted_p_value!;
            const raw = reg().p_value;
            const title =
              raw !== undefined
                ? `raw p=${raw.toExponential(2)} · adjusted p=${adjusted.toExponential(2)}`
                : `adjusted p=${adjusted.toExponential(2)}`;
            return (
              <div class="text-[10px] text-text-muted" title={title}>
                adj p {adjusted.toExponential(2)}
              </div>
            );
          })()}
        </Show>
        <span
          class={`inline-block text-[9px] font-mono px-1.5 py-0.5 rounded-sm ${
            reg().detection_method === "change_point"
              ? "bg-blue-50 text-blue-700"
              : "bg-gray-100 text-gray-600"
          }`}
        >
          {reg().detection_method === "change_point" ? "change-point" : "t-test"}
        </span>
      </td>
      <td class="py-3 px-4">
        <CommitLink hash={reg().baseline_commit_hash} hashFull={reg().baseline_commit_hash_full} />
        <div class="text-[11px] text-text-muted">
          {formatNs(reg().baseline_ci_lower_ns)} - {formatNs(reg().baseline_ci_upper_ns)}
        </div>
      </td>
      <td class="py-3 px-4">
        <Show
          when={reg().introduced_commit_hash}
          fallback={<span class="text-text-muted text-[12px]">-</span>}
        >
          <div class="flex items-baseline gap-1.5">
            <CommitLink
              hash={reg().introduced_commit_hash}
              hashFull={reg().introduced_commit_hash_full}
            />
            <Show when={reg().introduced_commit_message}>
              <span class="text-[11px] text-text-muted truncate max-w-[200px]">
                {truncateMessage(reg().introduced_commit_message!)}
              </span>
            </Show>
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
  const [searchParams, setSearchParams] = useSearchParams();
  const [detectionFilter, setDetectionFilter] = createSignal<DetectionFilter>("all");
  const [branches] = createResource(() => api.getBranches());
  const branch = (): string => {
    const b = searchParams.branch;
    if (Array.isArray(b)) return b[0] || "main";
    return b || "main";
  };
  const dfMode = (): RegressionDFMode =>
    searchParams.df_mode === "latest" ? "latest" : "baseline";
  const sensitivity = (): SensitivityMode => (dfMode() === "latest" ? "conservative" : "balanced");

  const regressionKey = () => `${dfMode()}:${branch()}`;
  const [history] = createResource(regressionKey, () =>
    api.getRegressionHistory({
      dfMode: dfMode(),
      branch: branch(),
      limit: 200,
    }),
  );

  const setBranch = (next: string) => {
    setSearchParams({ branch: next === "main" ? undefined : next });
  };
  const setSensitivity = (next: SensitivityMode) => {
    setSearchParams({
      df_mode: next === "conservative" ? "latest" : "baseline",
    });
  };

  const historyEntries = () => history()?.entries ?? [];
  const latestRegressionEntry = () =>
    historyEntries().find((entry) => (entry.regressions?.length ?? 0) > 0);
  const allRegressions = (): RegressionWithContext[] =>
    historyEntries().flatMap((entry) =>
      (entry.regressions ?? []).map((regression) => ({
        ...regression,
        run_id: entry.run_id,
        run_date: entry.run_date,
        commit_hash: entry.commit_hash,
        commit_hash_full: entry.commit_hash_full,
        commit_message: entry.commit_message,
        branch: entry.branch,
      })),
    );
  const filteredRegressions = () => {
    const filter = detectionFilter();
    if (filter === "all") return allRegressions();
    return allRegressions().filter((r) => r.detection_method === filter);
  };
  const regressionCount = () => allRegressions().length;
  const hasRegressions = () => regressionCount() > 0;
  const scannedRuns = () => history()?.scanned_runs ?? 0;
  const regressionRuns = () =>
    new Set(allRegressions().map((regression) => regression.run_id)).size;
  const cachedRuns = () => history()?.cached_runs ?? 0;
  const computedRuns = () => history()?.computed_runs ?? 0;
  const globalShiftRuns = () =>
    historyEntries().filter((entry) => entry.global_shift_detected).length;

  // Count by detection method for filter badges
  const tTestCount = () => allRegressions().filter((r) => r.detection_method === "t_test").length;
  const changePointCount = () =>
    allRegressions().filter((r) => r.detection_method === "change_point").length;

  return (
    <div class="flex flex-col h-full w-full">
      {/* Header */}
      <div class="flex-none min-h-[57px] px-4 sm:px-6 py-2 sm:py-0 border-b border-border bg-bg-dark flex items-center gap-2 sm:gap-3 flex-wrap sm:flex-nowrap">
        <h2 class="text-[13px] sm:text-[14px] font-bold text-black uppercase tracking-widest shrink-0">
          Regressions
        </h2>
        <div class="hidden sm:block flex-1 min-w-0" />
        <div class="ml-auto w-full sm:w-auto flex items-center justify-end gap-1 sm:gap-2 flex-wrap">
          <Show when={branches()}>
            <div class="relative shrink-0">
              <select
                class="appearance-none pl-2 pr-6 py-1 border border-border rounded-none text-[10px] sm:text-[11px] bg-white text-black outline-none cursor-pointer font-mono font-medium hover:border-black transition-colors"
                style={{ width: `${Math.min(Math.max(branch().length * 7 + 28, 64), 150)}px` }}
                value={branch()}
                onChange={(e) => setBranch(e.currentTarget.value)}
              >
                <For each={branches()}>{(b) => <option value={b}>{b}</option>}</For>
              </select>
              <div class="pointer-events-none absolute inset-y-0 right-0 flex items-center px-1 text-black">
                <GitBranch size={11} />
              </div>
            </div>
          </Show>
          <div class="relative shrink-0">
            <select
              class="appearance-none pl-2 pr-5 py-1 border border-border rounded-none text-[10px] sm:text-[11px] bg-white text-black outline-none cursor-pointer font-mono font-medium hover:border-black transition-colors"
              value={sensitivity()}
              onChange={(e) => setSensitivity(e.currentTarget.value as SensitivityMode)}
              title="Regression sensitivity mode"
            >
              <option value="balanced">Balanced sensitivity</option>
              <option value="conservative">Conservative sensitivity</option>
            </select>
            <div class="pointer-events-none absolute inset-y-0 right-0 flex items-center px-1 text-black">
              <svg
                class="h-2.5 w-2.5 fill-current"
                xmlns="http://www.w3.org/2000/svg"
                viewBox="0 0 20 20"
              >
                <path d="M9.293 12.95l.707.707L15.657 8l-1.414-1.414L10 10.828 5.757 6.586 4.343 8z" />
              </svg>
            </div>
          </div>
        </div>
      </div>

      {/* Status Row */}
      <div class="flex-none border-b border-border py-4 px-6 bg-white">
        <div class="flex items-center justify-between">
          <div>
            <Show when={history.loading}>
              <div class="flex items-center gap-2 text-text-muted">
                <Loader2 size={18} class="animate-spin" />
                <span class="text-[14px]">Loading regression history...</span>
              </div>
            </Show>

            <Show when={!history.loading && history()}>
              <Show
                when={hasRegressions()}
                fallback={
                  <div class="flex items-center gap-2 text-success">
                    <Check size={18} strokeWidth={3} />
                    <span class="text-[14px] font-medium">
                      No regressions found across {scannedRuns()} scanned run
                      {scannedRuns() !== 1 ? "s" : ""}
                    </span>
                  </div>
                }
              >
                <div class="flex items-center gap-2 text-danger">
                  <AlertTriangle size={18} />
                  <span class="text-[14px] font-medium">
                    {regressionCount()} regression{regressionCount() !== 1 ? "s" : ""} across{" "}
                    {regressionRuns()} historical run{regressionRuns() !== 1 ? "s" : ""}
                  </span>
                </div>
              </Show>

              <Show when={latestRegressionEntry()}>
                <div class="mt-2 text-[11px] font-mono text-text-muted flex items-center gap-1.5">
                  <span>Latest regression run:</span>
                  <CommitLink
                    hash={latestRegressionEntry()?.commit_hash}
                    hashFull={latestRegressionEntry()?.commit_hash_full}
                  />
                  <span>{formatRelativeDate(latestRegressionEntry()!.run_date)}</span>
                </div>
              </Show>

              <Show when={globalShiftRuns() > 0}>
                <div class="mt-2 inline-flex items-center gap-2 rounded-sm border border-amber-300 bg-amber-50 px-2 py-1 text-[11px] font-mono text-amber-800">
                  <span>
                    Global shift detected in {globalShiftRuns()} historical run
                    {globalShiftRuns() !== 1 ? "s" : ""}
                  </span>
                </div>
              </Show>

              <div class="mt-2 text-[11px] font-mono text-text-muted">
                Cache: {cachedRuns()} reused, {computedRuns()} computed on this load
              </div>

              <div class="mt-1 text-[11px] font-mono text-text-muted">
                Sensitivity:{" "}
                {sensitivity() === "balanced"
                  ? "balanced (DF baseline, recommended)"
                  : "conservative (DF latest)"}
              </div>
            </Show>

            <Show when={history.error}>
              <div class="flex items-center gap-2 text-warning">
                <AlertTriangle size={18} />
                <span class="text-[14px]">Unable to load regression history</span>
              </div>
            </Show>
          </div>

          {/* Detection method filter */}
          <Show when={hasRegressions() && tTestCount() > 0 && changePointCount() > 0}>
            <div class="flex items-center gap-1">
              <button
                class={`px-2 py-1 text-[10px] font-mono border transition-colors ${
                  detectionFilter() === "all"
                    ? "border-black bg-black text-white"
                    : "border-border bg-white text-text-muted hover:text-black"
                }`}
                onClick={() => setDetectionFilter("all")}
              >
                All ({regressionCount()})
              </button>
              <Show when={tTestCount() > 0}>
                <button
                  class={`px-2 py-1 text-[10px] font-mono border transition-colors ${
                    detectionFilter() === "t_test"
                      ? "border-black bg-black text-white"
                      : "border-border bg-white text-text-muted hover:text-black"
                  }`}
                  onClick={() => setDetectionFilter("t_test")}
                >
                  t-test ({tTestCount()})
                </button>
              </Show>
              <Show when={changePointCount() > 0}>
                <button
                  class={`px-2 py-1 text-[10px] font-mono border transition-colors ${
                    detectionFilter() === "change_point"
                      ? "border-black bg-black text-white"
                      : "border-border bg-white text-text-muted hover:text-black"
                  }`}
                  onClick={() => setDetectionFilter("change_point")}
                >
                  change-point ({changePointCount()})
                </button>
              </Show>
            </div>
          </Show>
        </div>
      </div>

      {/* Regressions Table */}
      <div class="flex-1 overflow-auto">
        <Show when={hasRegressions()}>
          <table class="w-full">
            <thead class="sticky top-0 bg-white border-b border-border">
              <tr class="text-left text-[11px] text-text-muted uppercase tracking-wider">
                <th class="py-3 px-4 font-medium">Run</th>
                <th class="py-3 px-4 font-medium">Benchmark</th>
                <th class="py-3 px-4 font-medium">Change</th>
                <th class="py-3 px-4 font-medium">Baseline</th>
                <th class="py-3 px-4 font-medium">Introduced</th>
                <th class="py-3 px-4 font-medium w-10"></th>
              </tr>
            </thead>
            <tbody>
              <For each={filteredRegressions()}>
                {(regression) => <RegressionRow regression={regression} />}
              </For>
            </tbody>
          </table>
        </Show>

        <Show when={!history.loading && !hasRegressions() && !history.error}>
          <div class="flex items-center justify-center h-full text-text-muted">
            <div class="text-center">
              <div class="text-[14px] mb-2">No historical regressions to show</div>
              <div class="text-[12px]">Performance is stable across scanned runs</div>
            </div>
          </div>
        </Show>
      </div>
    </div>
  );
};

export default Regressions;
