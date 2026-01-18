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

    const avgIndex = datasets.findIndex((dataset: any) => dataset.label === "Average");
    if (avgIndex < 0) {
      return;
    }

    const avgDataset = datasets[avgIndex] as any;
    const ciLower = avgDataset.ciLower as number[] | undefined;
    const ciUpper = avgDataset.ciUpper as number[] | undefined;
    if (!ciLower || !ciUpper) {
      return;
    }

    const meta = chart.getDatasetMeta(avgIndex);
    const points = meta?.data || [];
    const stroke = typeof avgDataset.borderColor === "string" ? avgDataset.borderColor : "#000000";
    const cap = 3;

    const { ctx } = chart;
    ctx.save();
    ctx.strokeStyle = stroke;
    ctx.lineWidth = 1;
    points.forEach((pt: any, i: number) => {
      const lower = ciLower[i];
      const upper = ciUpper[i];
      if (lower === undefined || upper === undefined) {
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
);

interface Props {
  data: TrendPoint[];
  range?: number;
  currentRunId?: number;
  onPointClick?: (runId: number, resultId: number) => void;
  baselineCILowerNs?: number;
  baselineCIUpperNs?: number;
}

const TrendChart: Component<Props> = (props) => {
  const showData = () => {
    const limit = props.range || 100;
    return (props.data || []).slice(0, limit).reverse();
  };

  const chartData = (): any => {
    const data = showData();
    const ciLower = data.map((d) => d.ci_lower_ns ?? d.avg_ns);
    const ciUpper = data.map((d) => d.ci_upper_ns ?? d.avg_ns);
    const sdLower = data.map((d) => Math.max(d.avg_ns - d.std_dev_ns, 0));
    const sdUpper = data.map((d) => d.avg_ns + d.std_dev_ns);

    // Determine point colors based on regression status
    const currentRunId = props.currentRunId;
    const pointBgColors = data.map((d) => {
      if (d.regression_status === "regressed") return "#cf222e";
      if (d.regression_status === "baseline") return "#1a7f37";
      if (d.run_id === currentRunId) return "#000000";
      if (d.regression_status === "insufficient") return "#d1d5db";
      return "#ffffff";
    });
    const pointBorderColors = data.map((d) => {
      if (d.regression_status === "regressed") return "#cf222e";
      if (d.regression_status === "baseline") return "#1a7f37";
      if (d.regression_status === "insufficient") return "#9ca3af";
      return "#000000";
    });
    const pointRadii = data.map((d) => {
      if (d.regression_status === "regressed") return 6;
      if (d.regression_status === "baseline") return 5;
      if (d.run_id === currentRunId) return 5;
      if (d.regression_status === "insufficient") return 4;
      return 3;
    });

    return {
      labels: data.map((d) => {
        const date = new Date(d.run_date).toLocaleDateString(undefined, {
          month: "short",
          day: "numeric",
        });
        return [date, d.commit_hash.slice(0, 7)];
      }),
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
          data: data.map((d) => d.avg_ns),
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

  const chartOptions = (): any => ({
    responsive: true,
    maintainAspectRatio: false,
    animation: false,
    interaction: {
      mode: "index",
      intersect: false,
    },
    onClick: (event: any, elements: any[]) => {
      if (!elements || elements.length === 0) return;
      // The interaction mode is index, so elements[0] is one of the points
      // But we need to make sure we get the correct data index
      const index = elements[0].index;
      const d = showData()[index];
      if (d && props.onPointClick) {
        props.onPointClick(d.run_id, d.result_id);
      }
    },
    plugins: {
      legend: { display: false },
      baselineBand: {
        lower: props.baselineCILowerNs,
        upper: props.baselineCIUpperNs,
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
          return context.dataset?.label === "Average";
        },
        callbacks: {
          title: function (context: any[]) {
            const d = showData()[context[0].dataIndex];
            if (!d) return "";
            const date = new Date(d.run_date).toLocaleString();
            return `${d.commit_hash.slice(0, 7)} (${date})`;
          },
          label: function (context: any) {
            const d = showData()[context.dataIndex];
            if (!d) {
              return "";
            }
            const ciLower = d.ci_lower_ns ?? d.avg_ns;
            const ciUpper = d.ci_upper_ns ?? d.avg_ns;
            const lines = [
              `Avg: ${formatNs(d.avg_ns)}`,
              `95% CI: ${formatNs(ciLower)} - ${formatNs(ciUpper)}`,
              `Range: ${formatNs(d.min_ns)} - ${formatNs(d.max_ns)}`,
              `Samples: ${d.sample_count}`,
            ];
            // Add regression info if present
            if (d.regression_status === "regressed" && d.change_percent !== undefined) {
              lines.push(`Regression: +${d.change_percent.toFixed(1)}% vs baseline`);
            } else if (d.regression_status === "baseline") {
              lines.push(`Status: Baseline`);
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

  return (
    <div class="relative w-full h-full">
      <Line data={chartData()} options={chartOptions()} width={500} height={300} />
    </div>
  );
};

export default TrendChart;
