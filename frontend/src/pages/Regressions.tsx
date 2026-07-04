import { createResource, Show, For } from "solid-js";
import type { Component } from "solid-js";
import { useNavigate, useSearchParams } from "@solidjs/router";
import { api } from "../services/api";
import type { Regression, RegressionHistoryEntry } from "../services/api";
import { formatNs } from "../utils/format";
import { Check, AlertTriangle, Loader2, ArrowRight, GitBranch } from "lucide-solid";

type RegressionWithContext = Regression & {
  run_id: number;
  run_date: string;
  commit_hash: string;
  commit_hash_full: string;
  commit_message: string;
  branch: string;
};

const GITHUB_REPO_URL = "https://github.com/anomalyco/opentui";
const defaultRegressionHistoryLimit = 100;

const regressionEpisodeKey = (regression: RegressionWithContext): string =>
  [regression.category, regression.name, regression.run_id, regression.detection_method].join("::");

const uniqueRegressionEpisodes = (
  regressions: RegressionWithContext[],
): RegressionWithContext[] => {
  const episodes = new Map<string, RegressionWithContext>();
  for (const regression of regressions) {
    const key = regressionEpisodeKey(regression);
    if (!episodes.has(key)) {
      episodes.set(key, regression);
    }
  }

  return Array.from(episodes.values()).sort(
    (a, b) => b.run_id - a.run_id || b.change_percent - a.change_percent,
  );
};

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
    const regression = reg();
    const targetRunId = regression.run_id;
    const targetResultId = regression.latest_result_id;

    const params = new URLSearchParams();
    params.set("bench_id", String(targetResultId));
    params.set("from", "regressions");
    params.set("regression_run_id", String(regression.run_id));
    params.set("regression_result_id", String(regression.latest_result_id));
    params.set("regression_commit_hash", regression.commit_hash);
    params.set("regression_commit_hash_full", regression.commit_hash_full);
    params.set("regression_run_date", regression.run_date);
    params.set("regression_change_pct", String(regression.change_percent));
    params.set("regression_branch", regression.branch || "main");

    navigate(`/benchmarks/${targetRunId}?${params.toString()}`);
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
        <span class="inline-block text-[9px] font-mono px-1.5 py-0.5 rounded-sm bg-gray-100 text-gray-600">
          log-avg score
        </span>
      </td>
      <td class="py-3 px-4">
        <CommitLink hash={reg().baseline_commit_hash} hashFull={reg().baseline_commit_hash_full} />
        <div class="text-[11px] text-text-muted">
          {formatNs(reg().baseline_ci_lower_ns)} - {formatNs(reg().baseline_ci_upper_ns)}
        </div>
      </td>
      <td class="py-3 px-4 text-right">
        <ArrowRight size={16} class="text-text-muted inline-block" />
      </td>
    </tr>
  );
};

const RegressionTable: Component<{ regressions: RegressionWithContext[] }> = (props) => (
  <table class="w-full">
    <thead class="sticky top-0 bg-white border-b border-border">
      <tr class="text-left text-[11px] text-text-muted uppercase tracking-wider">
        <th class="py-3 px-4 font-medium">Latest Seen</th>
        <th class="py-3 px-4 font-medium">Benchmark</th>
        <th class="py-3 px-4 font-medium">Change</th>
        <th class="py-3 px-4 font-medium">Baseline</th>
        <th class="py-3 px-4 font-medium w-10"></th>
      </tr>
    </thead>
    <tbody>
      <For each={props.regressions}>
        {(regression) => <RegressionRow regression={regression} />}
      </For>
    </tbody>
  </table>
);

const Regressions: Component = () => {
  const [searchParams, setSearchParams] = useSearchParams();
  const [branches] = createResource(() => api.getBranches());
  const branch = (): string => {
    const b = searchParams.branch;
    if (Array.isArray(b)) return b[0] || "main";
    return b || "main";
  };
  const regressionKey = () => `${branch()}:${defaultRegressionHistoryLimit}`;
  const [history] = createResource(regressionKey, () =>
    api.getRegressionHistory({
      branch: branch(),
      limit: defaultRegressionHistoryLimit,
    }),
  );

  const setBranch = (next: string) => {
    setSearchParams({ branch: next === "main" ? undefined : next });
  };
  const historyEntries = () => history()?.entries ?? [];
  const regressionsWithContext = (entry: RegressionHistoryEntry): RegressionWithContext[] =>
    (entry.regressions ?? []).map((regression) => ({
      ...regression,
      run_id: entry.run_id,
      run_date: entry.run_date,
      commit_hash: entry.commit_hash,
      commit_hash_full: entry.commit_hash_full,
      commit_message: entry.commit_message,
      branch: entry.branch,
    }));
  const latestRegressionEntry = () =>
    historyEntries().find((entry) => (entry.regressions?.length ?? 0) > 0);
  const allRegressions = (): RegressionWithContext[] =>
    historyEntries().flatMap(regressionsWithContext);
  const regressionEpisodes = (): RegressionWithContext[] =>
    uniqueRegressionEpisodes(allRegressions());
  const regressionCount = () => regressionEpisodes().length;
  const hasRegressions = () => regressionCount() > 0;
  const scannedRuns = () => history()?.scanned_runs ?? 0;
  const regressionRuns = () =>
    historyEntries().filter((entry) => (entry.regressions?.length ?? 0) > 0).length;
  const cachedRuns = () => history()?.cached_runs ?? 0;
  const computedRuns = () => history()?.computed_runs ?? 0;
  const broadShiftEntries = () => historyEntries().filter((entry) => entry.broad_shift.detected);
  const ordinaryRegressions = (): RegressionWithContext[] =>
    uniqueRegressionEpisodes(
      historyEntries()
        .filter((entry) => !entry.broad_shift.detected)
        .flatMap(regressionsWithContext),
    );

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
                  <Show
                    when={broadShiftEntries().length > 0}
                    fallback={
                      <div class="flex items-center gap-2 text-success">
                        <Check size={18} strokeWidth={3} />
                        <span class="text-[14px] font-medium">
                          No regression episodes found across {scannedRuns()} scanned run
                          {scannedRuns() !== 1 ? "s" : ""}
                        </span>
                      </div>
                    }
                  >
                    <div class="flex items-center gap-2 text-amber-800">
                      <AlertTriangle size={18} />
                      <span class="text-[14px] font-medium">
                        Broad movement detected; no individual benchmark crossed alert thresholds
                      </span>
                    </div>
                  </Show>
                }
              >
                <div class="flex items-center gap-2 text-danger">
                  <AlertTriangle size={18} />
                  <span class="text-[14px] font-medium">
                    {regressionCount()} regression episode{regressionCount() !== 1 ? "s" : ""}{" "}
                    across {regressionRuns()} historical run{regressionRuns() !== 1 ? "s" : ""}
                  </span>
                </div>
              </Show>

              <Show when={latestRegressionEntry()}>
                <div class="mt-2 text-[11px] font-mono text-text-muted flex items-center gap-1.5">
                  <span>Latest sighting:</span>
                  <CommitLink
                    hash={latestRegressionEntry()?.commit_hash}
                    hashFull={latestRegressionEntry()?.commit_hash_full}
                  />
                  <span>{formatRelativeDate(latestRegressionEntry()!.run_date)}</span>
                </div>
              </Show>

              <Show when={broadShiftEntries().length > 0}>
                <div class="mt-2 inline-flex items-center gap-2 rounded-sm border border-amber-300 bg-amber-50 px-2 py-1 text-[11px] font-mono text-amber-800">
                  <span>
                    Broad movement in {broadShiftEntries().length} historical run
                    {broadShiftEntries().length !== 1 ? "s" : ""}; many benchmarks moved together,
                    cause unknown
                  </span>
                </div>
              </Show>

              <div class="mt-2 text-[11px] font-mono text-text-muted">
                View: latest {scannedRuns()} scanned runs, grouped by episode
              </div>

              <div class="mt-1 text-[11px] font-mono text-text-muted">
                Cache: {cachedRuns()} reused, {computedRuns()} computed on this load
              </div>

              <div class="mt-1 text-[11px] font-mono text-text-muted">
                {history()?.calibration_status}: {history()?.metric}
              </div>

              <div class="mt-1 max-w-[760px] text-[11px] font-mono text-text-muted">
                {history()?.calibration_caveat}
              </div>
            </Show>

            <Show when={history.error}>
              <div class="flex items-center gap-2 text-warning">
                <AlertTriangle size={18} />
                <span class="text-[14px]">Unable to load regression history</span>
              </div>
            </Show>
          </div>
        </div>
      </div>

      {/* Regressions Table */}
      <div class="flex-1 overflow-auto">
        <Show when={broadShiftEntries().length > 0}>
          <For each={broadShiftEntries()}>
            {(entry) => (
              <section class="m-4 border border-amber-300 bg-amber-50/40">
                <div class="border-b border-amber-300 bg-amber-50 px-4 py-3">
                  <div class="flex flex-wrap items-baseline gap-x-3 gap-y-1">
                    <span class="text-[12px] font-bold uppercase tracking-wider text-amber-900">
                      Broad-shift co-occurrence
                    </span>
                    <CommitLink hash={entry.commit_hash} hashFull={entry.commit_hash_full} />
                    <span class="text-[11px] font-mono text-amber-800">
                      {(entry.broad_shift.positive_share * 100).toFixed(0)}% moved slower ·
                      geometric change +{entry.broad_shift.geometric_change_percent.toFixed(1)}% ·
                      {entry.broad_shift.compared_benchmarks} compared
                    </span>
                  </div>
                  <div class="mt-1 text-[11px] text-amber-900">
                    Many benchmarks moved together in this run. Cause is unknown and unclassified;
                    this grouping records co-occurrence, not causal attribution. Any individual
                    benchmark alerts are retained below.
                  </div>
                </div>
                <Show
                  when={entry.regressions.length > 0}
                  fallback={
                    <div class="px-4 py-3 text-[12px] text-text-muted">
                      No individual benchmark crossed the alert thresholds for this incident.
                    </div>
                  }
                >
                  <RegressionTable regressions={regressionsWithContext(entry)} />
                </Show>
              </section>
            )}
          </For>
        </Show>

        <Show when={ordinaryRegressions().length > 0}>
          <RegressionTable regressions={ordinaryRegressions()} />
        </Show>

        <Show
          when={
            !history.loading &&
            !hasRegressions() &&
            broadShiftEntries().length === 0 &&
            !history.error
          }
        >
          <div class="flex items-center justify-center h-full text-text-muted">
            <div class="text-center">
              <div class="text-[14px] mb-2">No historical regression episodes to show</div>
              <div class="text-[12px]">Performance is stable across the recent scanned runs</div>
            </div>
          </div>
        </Show>
      </div>
    </div>
  );
};

export default Regressions;
