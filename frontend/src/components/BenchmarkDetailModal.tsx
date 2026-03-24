import { Show, createSignal, For } from "solid-js";
import type { Component } from "solid-js";
import { useSearchParams } from "@solidjs/router";
import { formatNs, formatBytes } from "../utils/format";
import { Button } from "./Button";
import TrendChart from "./TrendChart";
import FlamegraphViewer from "./FlamegraphViewer";
import type { BenchmarkResult, TrendResponse } from "../services/api";
import TrendIndicator from "./TrendIndicator";
import type { RegressionNavigationContext } from "../hooks/useBenchmarkDetail";

const GITHUB_REPO_URL = "https://github.com/anomalyco/opentui";

const CommitHashLink: Component<{ hash: string; hashFull?: string; class?: string }> = (props) => {
  const href = `${GITHUB_REPO_URL}/commit/${props.hashFull ?? props.hash}`;

  return (
    <a
      href={href}
      target="_blank"
      rel="noopener noreferrer"
      class={`hover:text-black hover:underline ${props.class ?? ""}`}
    >
      #{props.hash.slice(0, 7)}
    </a>
  );
};

interface BenchmarkDetailModalProps {
  benchmark: BenchmarkResult;
  runId: number;
  commitHash: string;
  branch: string;
  trendData: TrendResponse | undefined;
  branchTrendData?: TrendResponse | undefined;
  flamegraphView: "flamegraph" | "callgraph";
  setFlamegraphView: (v: "flamegraph" | "callgraph") => void;
  hasCpuProfile: boolean;
  chartRange: number;
  setChartRange: (v: number) => void;
  regressionContext?: RegressionNavigationContext;
  onClose: () => void;
  onDownloadCpu: () => void;
  onOpenPProf: () => void;
  onTrendClick?: (runId: number, resultId: number) => void;
}

const BenchmarkDetailModal: Component<BenchmarkDetailModalProps> = (props) => {
  const [showProfileHelp, setShowProfileHelp] = createSignal(false);
  const [showTrendHelp, setShowTrendHelp] = createSignal(false);
  const [chartValueMode, setChartValueMode] = createSignal<"absolute" | "index">("absolute");
  const [reasonFilter, setReasonFilter] = createSignal<"all" | "compared" | "pre_epoch">("all");
  const [searchParams] = useSearchParams();

  const latestGlobalShift = () => {
    const shifts = props.trendData?.global_shifts;
    if (!shifts || shifts.length === 0) return undefined;
    return shifts.reduce((latest, curr) => (curr.run_id > latest.run_id ? curr : latest));
  };

  const reasonLabel = (reason: string) => {
    if (reason === "pre_epoch_not_compared") return "pre-epoch context";
    if (reason === "insufficient_baseline_history") return "insufficient baseline history";
    if (reason === "baseline_reference") return "baseline reference";
    return reason.replaceAll("_", " ");
  };

  const topTrendReasons = () => {
    const points = props.trendData?.points || [];
    const counts = new Map<string, number>();
    for (const p of points) {
      if (!p.regression_reason) continue;
      counts.set(p.regression_reason, (counts.get(p.regression_reason) || 0) + 1);
    }
    return [...counts.entries()].sort((a, b) => b[1] - a[1]).slice(0, 3);
  };

  const isViewingLatestRegression = () => props.regressionContext?.latestRunId === props.runId;
  const currentCommitHashFull = () => {
    const context = props.regressionContext;
    if (!context) return undefined;
    if (context.latestRunId === props.runId) return context.latestCommitHashFull;
    if (context.introducedRunId === props.runId) return context.introducedCommitHashFull;
    return undefined;
  };

  const regressionSummary = () => {
    const context = props.regressionContext;
    if (!context) return undefined;

    const changeLabel =
      context.changePercent !== undefined ? ` (+${context.changePercent.toFixed(1)}%)` : "";

    if (isViewingLatestRegression()) {
      if (context.introducedCommitHash) {
        return (
          <>
            <span>Latest regression sighting: </span>
            <CommitHashLink
              hash={context.latestCommitHash}
              hashFull={context.latestCommitHashFull}
              class="text-text-muted"
            />
            <span>{changeLabel}</span>
            <span> · introduced at </span>
            <CommitHashLink
              hash={context.introducedCommitHash}
              hashFull={context.introducedCommitHashFull}
              class="text-text-muted"
            />
          </>
        );
      }
      return (
        <>
          <span>Latest regression sighting: </span>
          <CommitHashLink
            hash={context.latestCommitHash}
            hashFull={context.latestCommitHashFull}
            class="text-text-muted"
          />
          <span>{changeLabel}</span>
        </>
      );
    }

    return (
      <>
        <span>Viewing </span>
        <CommitHashLink
          hash={props.commitHash}
          hashFull={currentCommitHashFull()}
          class="text-text-muted"
        />
        <span> in the context of regression </span>
        <CommitHashLink
          hash={context.latestCommitHash}
          hashFull={context.latestCommitHashFull}
          class="text-text-muted"
        />
        <span>{changeLabel}</span>
      </>
    );
  };

  // Helper for stat blocks
  const StatBlock = (p: { label: string; value: any; sub?: any }) => (
    <div class="flex flex-col gap-0.5 md:gap-1">
      <div class="text-[9px] md:text-[10px] uppercase tracking-widest text-text-muted font-bold">
        {p.label}
      </div>
      <div class="text-[14px] md:text-[16px] font-mono font-medium text-black truncate">
        {p.value}
      </div>
      <Show when={p.sub}>
        <div class="text-[10px] md:text-[11px] text-text-muted font-mono truncate">{p.sub}</div>
      </Show>
    </div>
  );

  return (
    <div class="absolute inset-0 bg-white z-50 flex flex-col font-ui">
      {/* Header */}
      <div class="flex-none px-4 md:px-8 py-3 md:py-4 border-b border-black flex justify-between items-center bg-white">
        <div class="min-w-0 flex-1">
          <nav class="flex items-center gap-2 md:gap-3 text-[13px] overflow-hidden">
            <button
              onClick={props.onClose}
              class="text-text-muted hover:text-black uppercase tracking-wider font-bold cursor-pointer whitespace-nowrap text-[11px] md:text-[13px]"
            >
              Benchmarks
            </button>
            <span class="text-text-muted">/</span>
            <span class="font-mono text-[11px] md:text-[13px] text-text-muted">
              <CommitHashLink hash={props.commitHash} hashFull={currentCommitHashFull()} />
            </span>
            <Show when={props.branch && props.branch !== "main" && props.branch !== ""}>
              <span class="ml-1 px-1.5 py-0.5 text-[10px] font-mono font-medium bg-purple-100 text-purple-700 rounded-sm">
                {props.branch}
              </span>
            </Show>
            <span class="text-text-muted">/</span>
            <span class="font-mono font-bold text-black text-[13px] md:text-[15px] truncate">
              {props.benchmark.name}
            </span>
          </nav>
          <Show when={props.regressionContext}>
            <div class="mt-1 text-[10px] md:text-[11px] font-mono text-text-muted truncate">
              {regressionSummary()}
            </div>
          </Show>
        </div>
        <div class="flex-none ml-4">
          <Button
            onClick={props.onClose}
            class="!border-transparent hover:!bg-transparent hover:!text-black hover:underline whitespace-nowrap"
          >
            Close <span class="hidden sm:inline ml-1">[Esc]</span>
          </Button>
        </div>
      </div>
      <div class="flex-1 overflow-auto p-4 md:p-8 bg-white">
        {/* Stats Grid */}
        <div class="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-6 gap-y-4 gap-x-2 md:gap-8 mb-4 md:mb-12 pb-4 md:pb-8 border-b border-border">
          <StatBlock label="Average" value={formatNs(props.benchmark.avg_ns)} />
          <StatBlock
            label="P50 / P99"
            value={formatNs(props.benchmark.p50_ns)}
            sub={formatNs(props.benchmark.p99_ns)}
          />
          <StatBlock
            label="Min — Max"
            value={formatNs(props.benchmark.min_ns)}
            sub={formatNs(props.benchmark.max_ns)}
          />
          <StatBlock label="Std Dev" value={formatNs(props.benchmark.std_dev_ns)} />

          <div class="flex flex-col gap-0.5 md:gap-1">
            <div class="text-[9px] md:text-[10px] uppercase tracking-widest text-text-muted font-bold">
              Trend
            </div>
            <div class="h-[21px] md:h-[24px] flex items-center">
              <TrendIndicator
                trendData={props.trendData?.points}
                benchmarkName={props.benchmark.name}
                currentRunId={props.runId}
                fromCompare={searchParams.from === "compare"}
                compareBaseRunId={searchParams.compare_base as string | undefined}
              />
            </div>
          </div>

          <div class="flex flex-col gap-0.5 md:gap-1">
            <div class="text-[9px] md:text-[10px] uppercase tracking-widest text-text-muted font-bold">
              Memory
            </div>
            <div class="flex flex-col gap-0.5 md:gap-1">
              <For each={props.benchmark.mem_stats}>
                {(m) => (
                  <div class="font-mono text-[10px] md:text-[12px] truncate">
                    <span class="text-text-muted mr-2">{m.name}:</span>
                    <span>{formatBytes(m.bytes)}</span>
                  </div>
                )}
              </For>
              <Show when={!props.benchmark.mem_stats?.length}>
                <span class="text-text-muted text-[10px] md:text-[12px] italic">None</span>
              </Show>
            </div>
          </div>
        </div>

        <div class="flex flex-col gap-12 pb-12">
          {/* Trend Column */}
          <div class="flex flex-col">
            <div class="flex flex-col md:flex-row md:justify-between md:items-center gap-4 md:gap-0 mb-6 border-b border-border pb-2">
              <div class="flex items-center gap-2 relative">
                <h3 class="text-[12px] font-bold text-black uppercase tracking-widest">
                  Performance History
                </h3>
                <button
                  onClick={() => setShowTrendHelp(!showTrendHelp())}
                  class="w-4 h-4 rounded-full border border-text-muted text-[10px] text-text-muted flex items-center justify-center hover:border-black hover:text-black hover:bg-black/5 transition-colors"
                >
                  ?
                </button>
                <Show when={showTrendHelp()}>
                  <button
                    type="button"
                    aria-label="Close visualization guide"
                    class="fixed inset-0 z-[60] border-0 bg-transparent p-0"
                    onClick={() => setShowTrendHelp(false)}
                  ></button>
                  <div class="absolute left-0 top-full mt-2 w-[320px] bg-white border border-black shadow-2xl z-[70] p-4 text-[12px] text-black">
                    <div class="font-bold uppercase tracking-widest mb-3 border-b border-border pb-2">
                      Visualization Guide
                    </div>

                    <div class="space-y-4 font-ui">
                      <div>
                        <div class="font-bold mb-1">Error Bars (95% CI)</div>
                        <p class="text-text-muted leading-relaxed">
                          Shows the 95% Confidence Interval around the median estimate. Narrower
                          bars indicate higher precision (more stable results or more samples).
                        </p>
                      </div>

                      <div>
                        <div class="font-bold mb-1">Shaded Band (Standard Deviation)</div>
                        <p class="text-text-muted leading-relaxed">
                          The light gray background band represents +-1 Standard Deviation around
                          the median, showing the variability of individual benchmark runs.
                        </p>
                      </div>

                      <div>
                        <div class="font-bold mb-1">Interaction</div>
                        <p class="text-text-muted leading-relaxed">
                          Click any data point to inspect that specific run's details.
                        </p>
                      </div>

                      <div>
                        <div class="font-bold mb-1">Global Shift Markers</div>
                        <p class="text-text-muted leading-relaxed">
                          Orange dashed vertical lines indicate harness-level shifts detected across
                          many benchmarks. Compare within an epoch for fair trend interpretation.
                        </p>
                      </div>

                      <div>
                        <div class="font-bold mb-1">Value Modes</div>
                        <p class="text-text-muted leading-relaxed">
                          Use <span class="font-mono">ns</span> for absolute timings and
                          <span class="font-mono"> index</span> for normalized history where 100 is
                          the epoch anchor.
                        </p>
                      </div>

                      <div>
                        <div class="font-bold mb-1">Status Reasons</div>
                        <p class="text-text-muted leading-relaxed">
                          Tooltips include reason codes for baseline/insufficient points and
                          pre-epoch context points so you can see why a point is or is not compared.
                        </p>
                      </div>

                      <div>
                        <div class="font-bold mb-1">Point Filters</div>
                        <p class="text-text-muted leading-relaxed">
                          Use filter buttons to view all points, only compared points, or only
                          pre-epoch context points.
                        </p>
                        <p class="text-text-muted leading-relaxed mt-1">
                          The chart legend also shows point-color meaning for compared vs pre-epoch
                          context.
                        </p>
                      </div>
                    </div>
                  </div>
                </Show>
              </div>
              <div class="self-start md:self-auto">
                <div class="flex items-center gap-2 md:gap-3 flex-wrap">
                  <div class="flex items-center gap-1">
                    <Button
                      active={props.chartRange === 30}
                      onClick={() => props.setChartRange(30)}
                    >
                      30
                    </Button>
                    <Button
                      active={props.chartRange === 70}
                      onClick={() => props.setChartRange(70)}
                    >
                      70
                    </Button>
                    <Button
                      active={props.chartRange === 100}
                      onClick={() => props.setChartRange(100)}
                    >
                      MAX
                    </Button>
                  </div>
                  <label class="flex items-center gap-1">
                    <span class="text-[9px] sm:text-[10px] uppercase tracking-wider text-text-muted font-bold">
                      Value
                    </span>
                    <div class="relative">
                      <select
                        class="appearance-none pl-2 pr-6 py-1 border border-border rounded-none text-[11px] bg-white text-black outline-none cursor-pointer font-mono font-medium hover:border-black transition-colors"
                        value={chartValueMode()}
                        onChange={(e) =>
                          setChartValueMode(e.currentTarget.value as "absolute" | "index")
                        }
                      >
                        <option value="absolute">ns</option>
                        <option value="index">index</option>
                      </select>
                      <div class="pointer-events-none absolute inset-y-0 right-0 flex items-center px-1.5 text-black">
                        <svg
                          class="h-2.5 w-2.5 fill-current"
                          xmlns="http://www.w3.org/2000/svg"
                          viewBox="0 0 20 20"
                        >
                          <path d="M9.293 12.95l.707.707L15.657 8l-1.414-1.414L10 10.828 5.757 6.586 4.343 8z" />
                        </svg>
                      </div>
                    </div>
                  </label>
                  <label class="flex items-center gap-1">
                    <span class="text-[9px] sm:text-[10px] uppercase tracking-wider text-text-muted font-bold">
                      Filter
                    </span>
                    <div class="relative">
                      <select
                        class="appearance-none pl-2 pr-6 py-1 border border-border rounded-none text-[11px] bg-white text-black outline-none cursor-pointer font-mono font-medium hover:border-black transition-colors"
                        value={reasonFilter()}
                        onChange={(e) =>
                          setReasonFilter(e.currentTarget.value as "all" | "compared" | "pre_epoch")
                        }
                      >
                        <option value="all">all points</option>
                        <option value="compared">compared</option>
                        <option value="pre_epoch">pre-epoch</option>
                      </select>
                      <div class="pointer-events-none absolute inset-y-0 right-0 flex items-center px-1.5 text-black">
                        <svg
                          class="h-2.5 w-2.5 fill-current"
                          xmlns="http://www.w3.org/2000/svg"
                          viewBox="0 0 20 20"
                        >
                          <path d="M9.293 12.95l.707.707L15.657 8l-1.414-1.414L10 10.828 5.757 6.586 4.343 8z" />
                        </svg>
                      </div>
                    </div>
                  </label>
                </div>
              </div>
            </div>
            <div class="h-[300px] relative border border-border p-4">
              <Show
                when={props.trendData}
                fallback={
                  <div class="flex items-center justify-center h-full text-text-muted font-mono text-xs">
                    Loading trend data...
                  </div>
                }
              >
                <TrendChart
                  data={props.trendData!.points}
                  changePoints={props.trendData!.change_points}
                  globalShifts={props.trendData!.global_shifts}
                  overlayData={props.branchTrendData?.points}
                  overlayBranch={props.branch}
                  range={props.chartRange}
                  valueMode={chartValueMode()}
                  reasonFilter={reasonFilter()}
                  currentRunId={props.runId}
                  baselineCILowerNs={props.trendData!.baseline_ci_lower_ns}
                  baselineCIUpperNs={props.trendData!.baseline_ci_upper_ns}
                  onPointClick={props.onTrendClick}
                />
              </Show>
            </div>
            <div class="mt-3 flex justify-between text-[10px] text-text-muted font-mono uppercase tracking-wider">
              <span>Error Bars: 95% CI</span>
              <span>Shaded: ±1 SD</span>
            </div>
            <Show when={latestGlobalShift()}>
              <div class="mt-2 text-[10px] text-amber-800 font-mono uppercase tracking-wider">
                Global shift at run #{latestGlobalShift()!.run_id}: +
                {latestGlobalShift()!.geo_increase_pct.toFixed(1)}% geometric increase across{" "}
                {latestGlobalShift()!.compared_benchmarks} benchmarks
              </div>
            </Show>
            <Show when={topTrendReasons().length > 0}>
              <div class="mt-1 text-[10px] text-text-muted font-mono uppercase tracking-wider">
                Status reasons:{" "}
                {topTrendReasons()
                  .map(([k, v]) => `${reasonLabel(k)}=${v}`)
                  .join(", ")}
              </div>
            </Show>
          </div>

          {/* Flamegraph Column */}
          <div class="flex flex-col h-auto md:h-[600px]">
            <div class="flex flex-col md:flex-row md:justify-between md:items-center gap-4 md:gap-0 mb-6 border-b border-border pb-2">
              <div class="flex items-center gap-2 relative">
                <h3 class="text-[12px] font-bold text-black uppercase tracking-widest">
                  Execution Profile
                </h3>
                <button
                  onClick={() => setShowProfileHelp(!showProfileHelp())}
                  class="w-4 h-4 rounded-full border border-text-muted text-[10px] text-text-muted flex items-center justify-center hover:border-black hover:text-black hover:bg-black/5 transition-colors"
                >
                  ?
                </button>
                <Show when={showProfileHelp()}>
                  <button
                    type="button"
                    aria-label="Close CPU profile help"
                    class="fixed inset-0 z-[60] border-0 bg-transparent p-0"
                    onClick={() => setShowProfileHelp(false)}
                  ></button>
                  <div class="absolute left-0 top-full mt-2 w-[320px] bg-white border border-black shadow-2xl z-[70] p-4 text-[12px] text-black">
                    <div class="font-bold uppercase tracking-widest mb-3 border-b border-border pb-2">
                      CPU Profile
                    </div>
                    <p class="text-text-muted mb-4 font-ui leading-relaxed">
                      A pprof CPU profile captured during the benchmark run. This visualizes where
                      the program spent its time.
                    </p>
                    <div class="space-y-3 font-ui">
                      <div class="flex flex-col gap-1">
                        <div class="font-bold flex items-center gap-2">
                          <span class="w-2 h-2 bg-black rounded-full"></span>
                          Interactive
                        </div>
                        <p class="text-text-muted pl-4">
                          Opens the full pprof web UI in a new tab for deep analysis.
                        </p>
                      </div>
                      <div class="flex flex-col gap-1">
                        <div class="font-bold flex items-center gap-2">
                          <span class="w-2 h-2 bg-black rounded-full"></span>
                          Download
                        </div>
                        <p class="text-text-muted pl-4">
                          Downloads the{" "}
                          <code class="bg-bg-hover px-1 py-0.5 rounded-none font-mono text-[10px]">
                            .pprof
                          </code>{" "}
                          file for local use with{" "}
                          <code class="bg-bg-hover px-1 py-0.5 rounded-none font-mono text-[10px]">
                            go tool pprof
                          </code>
                          .
                        </p>
                      </div>
                    </div>
                  </div>
                </Show>
              </div>
              <div class="flex gap-2 items-center self-start md:self-auto flex-wrap">
                <Button
                  active={props.flamegraphView === "flamegraph"}
                  onClick={() => props.setFlamegraphView("flamegraph")}
                >
                  Flamegraph
                </Button>
                <Button disabled={!props.hasCpuProfile} onClick={props.onOpenPProf}>
                  Interactive
                </Button>
                <Button disabled={!props.hasCpuProfile} onClick={props.onDownloadCpu}>
                  Download
                </Button>
              </div>
            </div>
            <div class="h-[500px] md:h-auto md:flex-1 bg-white border border-border relative">
              <FlamegraphViewer
                runId={props.runId}
                resultId={props.benchmark.id}
                view={props.flamegraphView}
              />
            </div>
          </div>
        </div>
      </div>
    </div>
  );
};

export default BenchmarkDetailModal;
