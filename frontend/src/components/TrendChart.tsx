import { Show } from "solid-js";
import type { Component } from "solid-js";
import {
  Chart,
  Title,
  Tooltip,
  Legend,
  Colors,
  LineController,
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  Filler,
} from "chart.js";
import { Line } from "solid-chartjs";
import type { TrendPoint } from "../services/api";
import { formatNs } from "../utils/format";

// Plugin to draw baseline band
const baselineBandPlugin = {
  id: "baselineBand",
  beforeDatasetsDraw(chart: any) {
    const yScale = chart.scales?.y;
    if (!yScale) return;

    const options = chart.options?.plugins?.baselineBand;
    if (options?.lower === undefined || options?.upper === undefined) return;

    const { ctx } = chart;
    const chartArea = chart.chartArea;

    const yLower = yScale.getPixelForValue(options.lower);
    const yUpper = yScale.getPixelForValue(options.upper);

    ctx.save();
    ctx.fillStyle = "rgba(34, 197, 94, 0.1)"; // light green
    ctx.fillRect(chartArea.left, yUpper, chartArea.right - chartArea.left, yLower - yUpper);
    ctx.restore();
  },
};

const errorBarPlugin = {
  id: "errorBars",
  afterDatasetsDraw(chart: any) {
    const yScale = chart.scales?.y;
    if (!yScale) {
      return;
    }

    const datasets = chart.data?.datasets;
    if (!datasets) {
      return;
    }

    // Draw error bars for both "Average" and branch datasets
    for (let dsIndex = 0; dsIndex < datasets.length; dsIndex++) {
      const dataset = datasets[dsIndex] as any;
      const ciLower = dataset.ciLower as number[] | undefined;
      const ciUpper = dataset.ciUpper as number[] | undefined;
      if (!ciLower || !ciUpper) continue;

      const meta = chart.getDatasetMeta(dsIndex);
      const points = meta?.data || [];
      const stroke = typeof dataset.borderColor === "string" ? dataset.borderColor : "#000000";
      const cap = 3;

      const { ctx } = chart;
      ctx.save();
      ctx.strokeStyle = stroke;
      ctx.lineWidth = 1;
      points.forEach((pt: any, i: number) => {
        const lower = ciLower[i];
        const upper = ciUpper[i];
        if (lower === undefined || upper === undefined || lower === null || upper === null) {
          return;
        }
        const x = pt.x;
        const yLow = yScale.getPixelForValue(lower);
        const yHigh = yScale.getPixelForValue(upper);
        ctx.beginPath();
        ctx.moveTo(x, yLow);
        ctx.lineTo(x, yHigh);
        // Cap at the bottom
        ctx.moveTo(x - cap, yLow);
        ctx.lineTo(x + cap, yLow);
        // Cap at the top
        ctx.moveTo(x - cap, yHigh);
        ctx.lineTo(x + cap, yHigh);
        ctx.stroke();
      });
      ctx.restore();
    }
  },
};

const changePointLinesPlugin = {
  id: "changePointLines",
  afterDatasetsDraw(chart: any) {
    const options = chart.options?.plugins?.changePointLines;
    const points = options?.points as
      | { run_id: number; magnitude_ns: number; p_value: number }[]
      | undefined;
    const runIdToIndex = options?.runIdToIndex as Record<string, number> | undefined;
    const xScale = chart.scales?.x;
    const yScale = chart.scales?.y;
    if (!points || points.length === 0 || !runIdToIndex || !xScale || !yScale) {
      return;
    }

    const { ctx } = chart;
    for (const cp of points) {
      const idx = runIdToIndex[String(cp.run_id)];
      if (idx === undefined) {
        continue;
      }

      const x = xScale.getPixelForValue(idx);
      const color = cp.magnitude_ns >= 0 ? "#cf222e" : "#0969da";
      const absMagnitude = Math.abs(cp.magnitude_ns);
      const magnitudeLabel = `${cp.magnitude_ns >= 0 ? "+" : "-"}${formatNs(Math.round(absMagnitude))}`;

      ctx.save();
      ctx.strokeStyle = color;
      ctx.lineWidth = 1;
      ctx.setLineDash([4, 4]);
      ctx.beginPath();
      ctx.moveTo(x, yScale.top);
      ctx.lineTo(x, yScale.bottom);
      ctx.stroke();

      ctx.setLineDash([]);
      ctx.fillStyle = color;
      ctx.font = "10px var(--font-mono)";
      ctx.textAlign = "left";
      ctx.fillText(magnitudeLabel, x + 4, yScale.top + 10);
      ctx.restore();
    }
  },
};

const globalShiftLinesPlugin = {
  id: "globalShiftLines",
  afterDatasetsDraw(chart: any) {
    const options = chart.options?.plugins?.globalShiftLines;
    const points = options?.points as
      | {
          run_id: number;
          positive_share: number;
          geo_increase_pct: number;
          compared_benchmarks: number;
        }[]
      | undefined;
    const runIdToIndex = options?.runIdToIndex as Record<string, number> | undefined;
    const xScale = chart.scales?.x;
    const yScale = chart.scales?.y;
    if (!points || points.length === 0 || !runIdToIndex || !xScale || !yScale) {
      return;
    }

    const { ctx } = chart;
    for (const shift of points) {
      const idx = runIdToIndex[String(shift.run_id)];
      if (idx === undefined) {
        continue;
      }

      const x = xScale.getPixelForValue(idx);
      const color = "#b45309";
      const label = `global +${shift.geo_increase_pct.toFixed(1)}%`;

      ctx.save();
      ctx.strokeStyle = color;
      ctx.lineWidth = 2;
      ctx.setLineDash([8, 4]);
      ctx.beginPath();
      ctx.moveTo(x, yScale.top);
      ctx.lineTo(x, yScale.bottom);
      ctx.stroke();

      ctx.setLineDash([]);
      ctx.fillStyle = color;
      ctx.font = "10px var(--font-mono)";
      ctx.textAlign = "left";
      ctx.fillText(label, x + 4, yScale.top + 22);
      ctx.restore();
    }
  },
};

Chart.register(
  Title,
  Tooltip,
  Legend,
  Colors,
  LineController,
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  Filler,
  errorBarPlugin,
  baselineBandPlugin,
  changePointLinesPlugin,
  globalShiftLinesPlugin,
);

// Branch overlay color
const BRANCH_COLOR = "#7c3aed"; // purple-600

interface Props {
  data: TrendPoint[];
  changePoints?: { run_id: number; magnitude_ns: number; p_value: number }[];
  globalShifts?: {
    run_id: number;
    positive_share: number;
    geo_increase_pct: number;
    compared_benchmarks: number;
  }[];
  overlayData?: TrendPoint[];
  overlayBranch?: string;
  range?: number;
  valueMode?: "absolute" | "index";
  currentRunId?: number;
  onPointClick?: (runId: number, resultId: number) => void;
  baselineCILowerNs?: number;
  baselineCIUpperNs?: number;
}

/**
 * Build a merged timeline of main + branch points.
 *
 * Main points are shown as the primary line. Branch points fork off from the
 * last main point that precedes the branch's first data point chronologically.
 * The fork point is included in the branch dataset so the line visually
 * connects to main.
 *
 * Returns: { labels, mainData, branchData, allPoints }
 * where mainData/branchData are arrays aligned to labels (null where absent).
 */
function buildMergedTimeline(mainPoints: TrendPoint[], branchPoints: TrendPoint[], limit: number) {
  // Both arrays come in newest-first from API; reverse to chronological
  const main = mainPoints.slice(0, limit).reverse();
  const branch = branchPoints.slice(0, limit).reverse();

  if (branch.length === 0) {
    // No branch data — return main only
    return { labels: buildLabels(main), mainData: main, branchData: null, allPoints: main };
  }

  // Build a unified timeline: all main points, then branch points interleaved by date
  // But for simplicity with Chart.js category axis, we append branch points after main
  // and show the fork visually.

  // The x-axis labels come from main points. Branch points get appended at the end
  // (after the last main point they're chronologically near).
  // But actually, we need to merge by date for correct x positioning.

  // Strategy: create a combined sorted array of all unique time positions.
  // Each position has either a main value, a branch value, or both.

  interface TimeSlot {
    date: string;
    label: string[];
    mainPoint: TrendPoint | null;
    branchPoint: TrendPoint | null;
  }

  const slots: TimeSlot[] = [];
  const seen = new Set<string>();

  // Add all main points
  for (const p of main) {
    const key = `${p.run_id}-main`;
    if (!seen.has(key)) {
      seen.add(key);
      slots.push({
        date: p.run_date,
        label: buildLabel(p),
        mainPoint: p,
        branchPoint: null,
      });
    }
  }

  // Add branch points (they may overlap with main dates but are different runs)
  for (const p of branch) {
    slots.push({
      date: p.run_date,
      label: buildLabel(p),
      mainPoint: null,
      branchPoint: p,
    });
  }

  // Sort by date
  slots.sort((a, b) => new Date(a.date).getTime() - new Date(b.date).getTime());

  // Build the branch fork data: null for all positions before the fork,
  // then the fork point's main value (to connect), then branch values
  const labels = slots.map((s) => s.label);
  const mainData = slots.map((s) => s.mainPoint);

  // Find the fork point in the merged timeline: last main slot before first branch slot
  let forkSlotIndex = -1;
  const firstBranchSlotIndex = slots.findIndex((s) => s.branchPoint !== null);
  if (firstBranchSlotIndex > 0) {
    for (let i = firstBranchSlotIndex - 1; i >= 0; i--) {
      if (slots[i]!.mainPoint !== null) {
        forkSlotIndex = i;
        break;
      }
    }
  }

  // Build branch dataset: null everywhere except fork point + branch points
  const branchData: (TrendPoint | null)[] = slots.map((s, i) => {
    if (i === forkSlotIndex && slots[i]!.mainPoint) {
      // Fork point: use the main point's value so the line connects
      return slots[i]!.mainPoint;
    }
    return s.branchPoint;
  });

  const allPoints = slots.map((s) => s.branchPoint ?? s.mainPoint);

  return { labels, mainData, branchData, allPoints };
}

function buildLabel(p: TrendPoint): string[] {
  const date = new Date(p.run_date).toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
  });
  return [date, p.commit_hash.slice(0, 7)];
}

function buildLabels(points: TrendPoint[]): string[][] {
  return points.map(buildLabel);
}

const TrendChart: Component<Props> = (props) => {
  const hasOverlay = () => !!props.overlayData && props.overlayData.length > 0;
  const isIndexMode = () => props.valueMode === "index";

  const toIndex = (value: number | null | undefined, anchorNs: number | null) => {
    if (value === null || value === undefined || anchorNs === null || anchorNs <= 0) {
      return null;
    }
    return (value / anchorNs) * 100;
  };

  const latestGlobalShiftRunId = () => {
    const shifts = props.globalShifts || [];
    if (shifts.length === 0) return undefined;
    return shifts.reduce((latest, curr) => (curr.run_id > latest.run_id ? curr : latest)).run_id;
  };

  const getAnchorNs = (points: (TrendPoint | null)[]) => {
    const valid = points.filter((p): p is TrendPoint => p !== null);
    if (valid.length === 0) return null;
    const shiftRunId = latestGlobalShiftRunId();
    if (shiftRunId !== undefined) {
      const shiftPoint = valid.find((p) => p.run_id === shiftRunId);
      if (shiftPoint && shiftPoint.avg_ns > 0) return shiftPoint.avg_ns;
    }
    const first = valid[0]!;
    return first.avg_ns > 0 ? first.avg_ns : null;
  };

  const showData = () => {
    const limit = props.range || 100;
    return (props.data || []).slice(0, limit).reverse();
  };

  // Cache the merged timeline so it's computed once per render
  let cachedMergedKey = "";
  let cachedMerged: ReturnType<typeof buildMergedTimeline> | null = null;
  const getMergedTimeline = () => {
    const limit = props.range || 100;
    const key = `${props.data?.length}-${props.overlayData?.length}-${limit}`;
    if (key !== cachedMergedKey || !cachedMerged) {
      cachedMergedKey = key;
      cachedMerged = buildMergedTimeline(props.data || [], props.overlayData || [], limit);
    }
    return cachedMerged;
  };

  const getChangePointRunIndexMap = (): Record<string, number> => {
    const map: Record<string, number> = {};
    if (hasOverlay()) {
      const { mainData } = getMergedTimeline();
      mainData.forEach((point, index) => {
        if (point) {
          map[String(point.run_id)] = index;
        }
      });
      return map;
    }

    const data = showData();
    data.forEach((point, index) => {
      map[String(point.run_id)] = index;
    });
    return map;
  };

  const chartData = (): any => {
    const currentRunId = props.currentRunId;

    if (hasOverlay()) {
      // Merged timeline mode
      const { labels, mainData, branchData, allPoints } = getMergedTimeline();
      const anchorNs = getAnchorNs(mainData);

      // Main series values (null where no main point)
      const mainValues = mainData.map((d) =>
        d ? (isIndexMode() ? toIndex(d.avg_ns, anchorNs) : d.avg_ns) : null,
      );
      const mainCiLower = mainData.map((d) =>
        d
          ? isIndexMode()
            ? toIndex(d.ci_lower_ns ?? d.avg_ns, anchorNs)
            : (d.ci_lower_ns ?? d.avg_ns)
          : null,
      );
      const mainCiUpper = mainData.map((d) =>
        d
          ? isIndexMode()
            ? toIndex(d.ci_upper_ns ?? d.avg_ns, anchorNs)
            : (d.ci_upper_ns ?? d.avg_ns)
          : null,
      );
      const mainSdLower = mainData.map((d) =>
        d
          ? isIndexMode()
            ? toIndex(Math.max(d.avg_ns - d.std_dev_ns, 0), anchorNs)
            : Math.max(d.avg_ns - d.std_dev_ns, 0)
          : null,
      );
      const mainSdUpper = mainData.map((d) =>
        d
          ? isIndexMode()
            ? toIndex(d.avg_ns + d.std_dev_ns, anchorNs)
            : d.avg_ns + d.std_dev_ns
          : null,
      );

      // Main point styling
      const mainBgColors = mainData.map((d) => {
        if (!d) return "transparent";
        return "#ffffff";
      });
      const mainBorderColors = mainData.map((d) => {
        if (!d) return "transparent";
        return "#000000";
      });
      const mainRadii = mainData.map((d) => {
        if (!d) return 0;
        return d.run_id === currentRunId ? 5 : 3;
      });

      // Branch series values (null where no branch point)
      const branchValues = branchData!.map((d) =>
        d ? (isIndexMode() ? toIndex(d.avg_ns, anchorNs) : d.avg_ns) : null,
      );
      const branchCiLower = branchData!.map((d) =>
        d
          ? isIndexMode()
            ? toIndex(d.ci_lower_ns ?? d.avg_ns, anchorNs)
            : (d.ci_lower_ns ?? d.avg_ns)
          : null,
      );
      const branchCiUpper = branchData!.map((d) =>
        d
          ? isIndexMode()
            ? toIndex(d.ci_upper_ns ?? d.avg_ns, anchorNs)
            : (d.ci_upper_ns ?? d.avg_ns)
          : null,
      );

      // Branch point styling: fork point looks like main, actual branch points are purple
      const branchBgColors = branchData!.map((d, i) => {
        if (!d) return "transparent";
        // Fork point (main data reused) — don't show as branch-styled
        if (mainData[i] !== null && d === mainData[i]) return "#ffffff";
        if (d.run_id === currentRunId) return BRANCH_COLOR;
        return BRANCH_COLOR;
      });
      const branchBorderColors = branchData!.map((d, i) => {
        if (!d) return "transparent";
        if (mainData[i] !== null && d === mainData[i]) return "#000000";
        return BRANCH_COLOR;
      });
      const branchRadii = branchData!.map((d, i) => {
        if (!d) return 0;
        // Fork point: small
        if (mainData[i] !== null && d === mainData[i]) return 0;
        if (d.run_id === currentRunId) return 6;
        return 5;
      });

      return {
        labels,
        datasets: [
          {
            label: "SD Lower",
            data: mainSdLower,
            borderColor: "transparent",
            pointRadius: 0,
            pointHoverRadius: 0,
            fill: false,
            spanGaps: true,
          },
          {
            label: "SD Upper",
            data: mainSdUpper,
            borderColor: "transparent",
            backgroundColor: "rgba(0, 0, 0, 0.05)",
            pointRadius: 0,
            pointHoverRadius: 0,
            fill: "-1",
            spanGaps: true,
          },
          {
            label: "Average",
            data: mainValues,
            borderColor: "#000000",
            backgroundColor: "#ffffff",
            borderWidth: 1.5,
            tension: 0,
            pointRadius: mainRadii,
            pointHoverRadius: 6,
            pointBorderColor: mainBorderColors,
            pointBorderWidth: 1.5,
            pointBackgroundColor: mainBgColors,
            fill: false,
            spanGaps: true,
            ciLower: mainCiLower,
            ciUpper: mainCiUpper,
            // Store allPoints for tooltip/click lookups
            _allPoints: allPoints,
            _mainData: mainData,
          },
          {
            label: "Branch",
            data: branchValues,
            borderColor: BRANCH_COLOR,
            backgroundColor: BRANCH_COLOR,
            borderWidth: 2,
            borderDash: [6, 3],
            tension: 0,
            pointRadius: branchRadii,
            pointHoverRadius: 7,
            pointBorderColor: branchBorderColors,
            pointBorderWidth: 2,
            pointBackgroundColor: branchBgColors,
            fill: false,
            spanGaps: true,
            ciLower: branchCiLower,
            ciUpper: branchCiUpper,
            _branchData: branchData,
          },
        ],
      };
    }

    // Standard mode (no overlay) — original behavior
    const data = showData();
    const anchorNs = getAnchorNs(data);
    const ciLower = data.map((d) =>
      isIndexMode() ? toIndex(d.ci_lower_ns ?? d.avg_ns, anchorNs) : (d.ci_lower_ns ?? d.avg_ns),
    );
    const ciUpper = data.map((d) =>
      isIndexMode() ? toIndex(d.ci_upper_ns ?? d.avg_ns, anchorNs) : (d.ci_upper_ns ?? d.avg_ns),
    );
    const sdLower = data.map((d) =>
      isIndexMode()
        ? toIndex(Math.max(d.avg_ns - d.std_dev_ns, 0), anchorNs)
        : Math.max(d.avg_ns - d.std_dev_ns, 0),
    );
    const sdUpper = data.map((d) =>
      isIndexMode() ? toIndex(d.avg_ns + d.std_dev_ns, anchorNs) : d.avg_ns + d.std_dev_ns,
    );

    const pointBgColors = data.map((d) => {
      if (d.run_id === currentRunId) return "#000000";
      return "#ffffff";
    });
    const pointBorderColors = data.map(() => "#000000");
    const pointRadii = data.map((d) => {
      if (d.run_id === currentRunId) return 5;
      return 3;
    });

    return {
      labels: buildLabels(data),
      datasets: [
        {
          label: "SD Lower",
          data: sdLower,
          borderColor: "transparent",
          pointRadius: 0,
          pointHoverRadius: 0,
          fill: false,
        },
        {
          label: "SD Upper",
          data: sdUpper,
          borderColor: "transparent",
          backgroundColor: "rgba(0, 0, 0, 0.05)",
          pointRadius: 0,
          pointHoverRadius: 0,
          fill: "-1",
        },
        {
          label: "Average",
          data: data.map((d) => (isIndexMode() ? toIndex(d.avg_ns, anchorNs) : d.avg_ns)),
          borderColor: "#000000",
          backgroundColor: "#ffffff",
          borderWidth: 1.5,
          tension: 0,
          pointRadius: pointRadii,
          pointHoverRadius: 6,
          pointBorderColor: pointBorderColors,
          pointBorderWidth: 1.5,
          pointBackgroundColor: pointBgColors,
          fill: false,
          ciLower,
          ciUpper,
        },
      ],
    };
  };

  // Find the trend point for a given chart index (works for both modes)
  const getPointAtIndex = (index: number): TrendPoint | null => {
    if (hasOverlay()) {
      const { mainData, branchData } = getMergedTimeline();
      // Prefer branch point if it exists at this index, else main
      const bp = branchData?.[index];
      const mp = mainData[index];
      return bp ?? mp ?? null;
    }
    return showData()[index] ?? null;
  };

  const currentAnchorNs = () => {
    if (hasOverlay()) {
      const { mainData } = getMergedTimeline();
      return getAnchorNs(mainData);
    }
    return getAnchorNs(showData());
  };

  const chartOptions = (): any => ({
    responsive: true,
    maintainAspectRatio: false,
    animation: false,
    interaction: {
      mode: "index",
      intersect: false,
    },
    onClick: (_event: any, elements: any[]) => {
      if (!elements || elements.length === 0) return;
      const index = elements[0].index;
      const d = getPointAtIndex(index);
      if (d && props.onPointClick) {
        props.onPointClick(d.run_id, d.result_id);
      }
    },
    plugins: {
      legend: hasOverlay()
        ? {
            display: true,
            position: "top" as const,
            align: "end" as const,
            labels: {
              usePointStyle: true,
              pointStyle: "circle",
              boxWidth: 8,
              boxHeight: 8,
              font: {
                family: "var(--font-ui)",
                size: 11,
              },
              color: "#666666",
              generateLabels: (chart: any) => {
                const datasets = chart.data.datasets;
                const items: any[] = [];
                const averageDataset = datasets.find((d: any) => d.label === "Average");
                if (averageDataset) {
                  items.push({
                    text: "main",
                    fillStyle: "#000000",
                    strokeStyle: "#000000",
                    lineWidth: 1.5,
                    pointStyle: "circle",
                    hidden: false,
                  });
                }
                const branchDs = datasets.find((d: any) => d.label === "Branch");
                if (branchDs) {
                  items.push({
                    text: props.overlayBranch || "branch",
                    fillStyle: BRANCH_COLOR,
                    strokeStyle: BRANCH_COLOR,
                    lineWidth: 2,
                    pointStyle: "circle",
                    lineDash: [6, 3],
                    hidden: false,
                  });
                }
                return items;
              },
            },
          }
        : { display: false },
      baselineBand: {
        lower: isIndexMode()
          ? toIndex(props.baselineCILowerNs, currentAnchorNs())
          : props.baselineCILowerNs,
        upper: isIndexMode()
          ? toIndex(props.baselineCIUpperNs, currentAnchorNs())
          : props.baselineCIUpperNs,
      },
      changePointLines: {
        points: props.changePoints || [],
        runIdToIndex: getChangePointRunIndexMap(),
      },
      globalShiftLines: {
        points: props.globalShifts || [],
        runIdToIndex: getChangePointRunIndexMap(),
      },
      tooltip: {
        backgroundColor: "#ffffff",
        titleColor: "#111111",
        bodyColor: "#666666",
        borderColor: "#e5e5e5",
        borderWidth: 1,
        padding: 10,
        displayColors: false,
        titleFont: {
          family: "var(--font-mono)",
          size: 12,
        },
        bodyFont: {
          family: "var(--font-ui)",
          size: 12,
        },
        filter: function (context: any) {
          const label = context.dataset?.label;
          return label === "Average" || label === "Branch";
        },
        callbacks: {
          title: function (context: any[]) {
            const d = getPointAtIndex(context[0].dataIndex);
            if (!d) return "";
            const date = new Date(d.run_date).toLocaleString();
            const branchLabel =
              d.branch && d.branch !== "" && d.branch !== "main" ? ` [${d.branch}]` : "";
            const lines = [`${d.commit_hash.slice(0, 7)}${branchLabel} (${date})`];
            if (d.commit_message) {
              const firstLine = d.commit_message.split("\n")[0] ?? "";
              const msg = firstLine.length > 60 ? firstLine.slice(0, 59) + "…" : firstLine;
              if (msg) lines.push(msg);
            }
            return lines;
          },
          label: function (context: any) {
            const idx = context.dataIndex;
            const isBranch = context.dataset?.label === "Branch";

            let d: TrendPoint | null = null;
            if (hasOverlay()) {
              const { mainData, branchData } = getMergedTimeline();
              d = isBranch ? (branchData?.[idx] ?? null) : (mainData[idx] ?? null);
            } else {
              d = showData()[idx] ?? null;
            }

            if (!d) return "";
            const ciLower = d.ci_lower_ns ?? d.avg_ns;
            const ciUpper = d.ci_upper_ns ?? d.avg_ns;
            const lines = [
              `Average: ${formatNs(d.avg_ns)}`,
              `P50: ${formatNs(d.median_ns)}`,
              `95% CI: ${formatNs(ciLower)} - ${formatNs(ciUpper)}`,
              `Range: ${formatNs(d.min_ns)} - ${formatNs(d.max_ns)}`,
              `Samples: ${d.sample_count}`,
            ];
            if (isIndexMode()) {
              const anchorNs = currentAnchorNs();
              if (anchorNs && anchorNs > 0) {
                lines.unshift(`Index: ${((d.avg_ns / anchorNs) * 100).toFixed(2)} (100 = anchor)`);
              }
            }
            if (isBranch && d.branch) {
              lines.push(`Branch: ${d.branch}`);
            }
            return lines;
          },
        },
      },
    },
    scales: {
      y: {
        beginAtZero: true,
        grid: {
          color: "#f0f0f0",
          borderDash: [4, 4],
          drawBorder: false,
        },
        border: { display: false },
        ticks: {
          font: {
            family: "var(--font-mono)",
            size: 11,
          },
          color: "#666666",
          callback: function (value: any) {
            if (isIndexMode()) {
              return `${Number(value).toFixed(1)}`;
            }
            return formatNs(value);
          },
        },
      },
      x: {
        display: true,
        grid: {
          display: false,
          drawBorder: false,
        },
        border: {
          display: true,
          color: "#000000",
        },
        ticks: {
          font: {
            family: "var(--font-mono)",
            size: 10,
          },
          color: "#666666",
          maxRotation: 45,
          minRotation: 0,
          autoSkip: true,
          maxTicksLimit: 10,
        },
      },
    },
  });

  const hasChangePoints = () => (props.changePoints?.length ?? 0) > 0;
  const hasGlobalShifts = () => (props.globalShifts?.length ?? 0) > 0;

  return (
    <div class="relative w-full h-full flex flex-col">
      <div class="flex-1 min-h-0">
        <Line data={chartData()} options={chartOptions()} width={500} height={300} />
      </div>
      <div class="shrink-0 mt-2 flex flex-wrap gap-x-4 gap-y-1 text-[10px] text-text-muted font-mono uppercase tracking-wider">
        <Show when={hasOverlay()}>
          <span class="flex items-center gap-1">
            <span class="inline-block w-3 h-[2px] bg-black"></span>
            main
          </span>
          <span class="flex items-center gap-1">
            <span
              class="inline-block w-3 h-0 border-t-2 border-dashed"
              style={`border-color: ${BRANCH_COLOR};`}
            ></span>
            {props.overlayBranch}
          </span>
        </Show>
        <Show when={hasChangePoints()}>
          <span class="flex items-center gap-1">
            <span
              class="inline-block w-3 h-0 border-t border-dashed"
              style="border-color: #cf222e;"
            ></span>
            Regression shift
          </span>
          <span class="flex items-center gap-1">
            <span
              class="inline-block w-3 h-0 border-t border-dashed"
              style="border-color: #0969da;"
            ></span>
            Improvement shift
          </span>
        </Show>
        <Show when={hasGlobalShifts()}>
          <span class="flex items-center gap-1">
            <span
              class="inline-block w-3 h-0 border-t-2 border-dashed"
              style="border-color: #b45309;"
            ></span>
            Global shift
          </span>
          <Show when={isIndexMode()}>
            <span>Index mode (100 = epoch anchor)</span>
          </Show>
        </Show>
      </div>
    </div>
  );
};

export default TrendChart;
