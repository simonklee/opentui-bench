import { createResource, createSignal, Show, For } from "solid-js";
import type { Component } from "solid-js";
import { useNavigate, useSearchParams } from "@solidjs/router";
import { api } from "../services/api";
import type { Regression, RegressionsMethod, RegressionDFMode } from "../services/api";
import { formatNs } from "../utils/format";
import { Check, AlertTriangle, Loader2, ArrowRight, GitBranch } from "lucide-solid";

type DetectionFilter = "all" | "t_test" | "change_point";

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

const formatReason = (reason?: string): string => {
  if (!reason) return "unknown";
  return reason.replaceAll("_", " ");
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

const RegressionRow: Component<{ regression: Regression; runId?: number | null }> = (props) => {
  const navigate = useNavigate();
  const reg = () => props.regression;

  const handleClick = () => {
    // Navigate to the run that introduced the regression
    const targetRunId = reg().introduced_run_id ?? props.runId ?? reg().baseline_run_id;
    const targetResultId = reg().introduced_result_id ?? reg().latest_result_id;
    navigate(`/benchmarks/${targetRunId}?bench_id=${targetResultId}`);
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
  const method = (): RegressionsMethod =>
    searchParams.method === "legacy" ? "legacy" : "hybrid";
  const dfMode = (): RegressionDFMode =>
    searchParams.df_mode === "latest" ? "latest" : "baseline";

  const regressionKey = () => `${method()}:${dfMode()}:${branch()}`;
  const [data] = createResource(regressionKey, () =>
    api.getRegressions(undefined, { method: method(), dfMode: dfMode(), branch: branch() }),
  );

  const setBranch = (next: string) => {
    setSearchParams({ branch: next === "main" ? undefined : next });
  };
  const setMethod = (next: RegressionsMethod) => {
    setSearchParams({ method: next });
  };
  const setDFMode = (next: RegressionDFMode) => {
    setSearchParams({ df_mode: next });
  };

  const allRegressions = () => data()?.regressions ?? [];
  const filteredRegressions = () => {
    const filter = detectionFilter();
    if (filter === "all") return allRegressions();
    return allRegressions().filter((r) => r.detection_method === filter);
  };
  const regressionCount = () => allRegressions().length;
  const hasRegressions = () => regressionCount() > 0;
  const insufficientHistory = () => !!data()?.insufficient_history;
  const showInsufficientHistory = () => insufficientHistory() && !hasRegressions();
  const globalShiftDetected = () => !!data()?.global_shift_detected;
  const effectiveMinPoints = () => data()?.effective_min_points ?? data()?.min_points;
  const isAdaptiveMinPoints = () => effectiveMinPoints() !== data()?.min_points;
  const comparedRuns = () => data()?.compared_runs ?? 0;
  const analyzedBenchmarks = () => data()?.analyzed_benchmarks ?? 0;
  const totalBenchmarks = () => data()?.total_benchmarks ?? 0;
  const topExclusions = () => {
    const counts = data()?.exclusion_counts;
    if (!counts) return [] as [string, number][];
    return Object.entries(counts)
      .sort((a, b) => b[1] - a[1])
      .slice(0, 3);
  };

  // Count by detection method for filter badges
  const tTestCount = () => allRegressions().filter((r) => r.detection_method === "t_test").length;
  const changePointCount = () => allRegressions().filter((r) => r.detection_method === "change_point").length;

  return (
    <div class="flex flex-col h-full w-full">
      {/* Header */}
      <div class="flex-none h-[57px] px-6 border-b border-border bg-bg-dark flex justify-between items-center">
        <div class="flex items-center gap-3">
          <h2 class="text-[14px] font-bold text-black uppercase tracking-widest">Regressions</h2>
          <Show when={branches()}>
            <div class="flex items-center gap-1">
              <GitBranch size={12} class="text-text-muted" />
              <For each={branches()}>
                {(b) => (
                  <button
                    class={`px-1.5 py-0.5 text-[10px] font-mono font-medium rounded-sm transition-colors ${
                      branch() === b
                        ? b === "main"
                          ? "bg-black text-white"
                          : "bg-purple-100 text-purple-700 ring-1 ring-purple-300"
                        : "bg-bg-hover text-text-muted hover:text-black"
                    }`}
                    onClick={() => setBranch(b)}
                  >
                    {b}
                  </button>
                )}
              </For>
            </div>
          </Show>
        </div>
        <div class="flex items-center gap-1">
          <button
            class={`px-2.5 py-1 text-[11px] font-mono border transition-colors ${
              method() === "legacy"
                ? "border-black bg-black text-white"
                : "border-border bg-white text-text-muted hover:text-black"
            }`}
            onClick={() => setMethod("legacy")}
          >
            Legacy
          </button>
          <button
            class={`px-2.5 py-1 text-[11px] font-mono border transition-colors ${
              method() === "hybrid"
                ? "border-black bg-black text-white"
                : "border-border bg-white text-text-muted hover:text-black"
            }`}
            onClick={() => setMethod("hybrid")}
          >
            Hybrid
          </button>
          <Show when={method() === "hybrid"}>
            <div class="ml-2 flex items-center gap-1">
              <button
                class={`px-2.5 py-1 text-[11px] font-mono border transition-colors ${
                  dfMode() === "baseline"
                    ? "border-black bg-black text-white"
                    : "border-border bg-white text-text-muted hover:text-black"
                }`}
                onClick={() => setDFMode("baseline")}
                title="Degrees of freedom from baseline history (more sensitive)"
              >
                DF baseline
              </button>
              <button
                class={`px-2.5 py-1 text-[11px] font-mono border transition-colors ${
                  dfMode() === "latest"
                    ? "border-black bg-black text-white"
                    : "border-border bg-white text-text-muted hover:text-black"
                }`}
                onClick={() => setDFMode("latest")}
                title="Degrees of freedom from latest run samples (more conservative)"
              >
                DF latest
              </button>
            </div>
          </Show>
        </div>
      </div>

      {/* Status Row */}
      <div class="flex-none border-b border-border py-4 px-6 bg-white">
        <div class="flex items-center justify-between">
          <div>
            <Show when={data.loading}>
              <div class="flex items-center gap-2 text-text-muted">
                <Loader2 size={18} class="animate-spin" />
                <span class="text-[14px]">Checking for regressions...</span>
              </div>
            </Show>

            <Show when={!data.loading && data()}>
              <Show
                when={showInsufficientHistory()}
                fallback={
                  <Show
                    when={hasRegressions()}
                    fallback={
                      <div class="flex items-center gap-2 text-success">
                        <Check size={18} strokeWidth={3} />
                        <span class="text-[14px] font-medium">All benchmarks healthy</span>
                      </div>
                    }
                  >
                    <div class="flex items-center gap-2 text-danger">
                      <AlertTriangle size={18} />
                      <span class="text-[14px] font-medium">
                        {regressionCount()} regression{regressionCount() !== 1 ? "s" : ""} detected
                      </span>
                    </div>
                  </Show>
                }
              >
                <div class="flex items-center gap-2 text-warning">
                  <AlertTriangle size={18} />
                  <span class="text-[14px] font-medium">Not enough history for analysis</span>
                </div>
                <div class="mt-2 text-[11px] font-mono text-text-muted">
                  Primary reason: {formatReason(data()?.insufficient_reason)}
                </div>
                <Show when={topExclusions().length > 0}>
                  <div class="mt-1 text-[11px] font-mono text-text-muted">
                    Exclusions: {topExclusions().map(([k, v]) => `${formatReason(k)}=${v}`).join(", ")}
                  </div>
                </Show>
              </Show>

              <Show when={globalShiftDetected()}>
                <div class="mt-2 inline-flex items-center gap-2 rounded-sm border border-amber-300 bg-amber-50 px-2 py-1 text-[11px] font-mono text-amber-800">
                  <span>Global shift detected</span>
                  <span>
                    +{(data()?.global_shift_geo_increase_pct ?? 0).toFixed(1)}% geo
                  </span>
                  <span>
                    {Math.round((data()?.global_shift_positive_share ?? 0) * 100)}% benchmarks
                  </span>
                </div>
              </Show>

              <Show when={isAdaptiveMinPoints()}>
                <div class="mt-2 text-[11px] font-mono text-text-muted">
                  Adaptive baseline warmup active (min points {data()?.min_points} to {effectiveMinPoints()})
                </div>
              </Show>

              <Show when={method() === "hybrid"}>
                <div class="mt-2 text-[11px] font-mono text-text-muted">
                  Statistical mode: {dfMode() === "baseline" ? "DF baseline (sensitive)" : "DF latest (conservative)"}
                </div>
              </Show>

              <Show when={!data()?.insufficient_history}>
                <div class="mt-2 text-[11px] font-mono text-text-muted">
                  Coverage: {analyzedBenchmarks()}/{totalBenchmarks()} benchmarks analyzed over {comparedRuns()} comparable runs
                  <Show when={data()?.epoch_run_id}> (epoch starts at run #{data()?.epoch_run_id})</Show>
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

          {/* Detection method filter */}
          <Show when={hasRegressions() && method() === "hybrid"}>
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
                <th class="py-3 px-4 font-medium">Benchmark</th>
                <th class="py-3 px-4 font-medium">Change</th>
                <th class="py-3 px-4 font-medium">Baseline</th>
                <th class="py-3 px-4 font-medium">Introduced</th>
                <th class="py-3 px-4 font-medium w-10"></th>
              </tr>
            </thead>
            <tbody>
              <For each={filteredRegressions()}>
                {(regression) => <RegressionRow regression={regression} runId={data()?.run_id} />}
              </For>
            </tbody>
          </table>
        </Show>

        <Show
          when={!data.loading && !hasRegressions() && !data.error && !showInsufficientHistory()}
        >
          <div class="flex items-center justify-center h-full text-text-muted">
            <div class="text-center">
              <div class="text-[14px] mb-2">No regressions to show</div>
              <div class="text-[12px]">Performance is stable across recent runs</div>
            </div>
          </div>
        </Show>
      </div>
    </div>
  );
};

export default Regressions;
