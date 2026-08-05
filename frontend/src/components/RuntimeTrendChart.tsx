import type { Component } from "solid-js";
import {
  Chart,
  CategoryScale,
  LinearScale,
  LineController,
  LineElement,
  PointElement,
  Legend,
  Tooltip,
} from "chart.js";
import { Line } from "solid-chartjs";
import type { RuntimeTrendResponse } from "../services/api";
import { formatNs } from "../utils/format";

Chart.register(
  CategoryScale,
  LinearScale,
  LineController,
  LineElement,
  PointElement,
  Legend,
  Tooltip,
);

const RuntimeTrendChart: Component<{
  data: RuntimeTrendResponse;
  mode: "absolute" | "relative";
  range: number;
}> = (props) => {
  const pairs = () => props.data.pairs.slice(0, props.range).reverse();
  const chartData = () => {
    const relative = props.mode === "relative";
    return {
      labels: pairs().map((pair) => [
        pair.commit_hash.slice(0, 7),
        new Date(pair.run_date).toLocaleDateString(),
      ]),
      datasets: relative
        ? [
            {
              label: `${props.data.baseline_runtime} baseline`,
              data: pairs().map((pair) => (pair.baseline_p50_ns === null ? null : 100)),
              borderColor: "#111111",
              borderDash: [5, 4],
              pointRadius: 0,
              spanGaps: false,
            },
            {
              label: `${props.data.compared_runtime} / ${props.data.baseline_runtime}`,
              data: pairs().map((pair) =>
                pair.baseline_p50_ns && pair.compared_p50_ns
                  ? (pair.compared_p50_ns / pair.baseline_p50_ns) * 100
                  : null,
              ),
              borderColor: "#7c3aed",
              backgroundColor: "#7c3aed",
              pointRadius: 3,
              spanGaps: false,
            },
          ]
        : [
            {
              label: `${props.data.baseline_runtime} ${props.data.baseline_runtime_version || ""}`,
              data: pairs().map((pair) => pair.baseline_p50_ns),
              borderColor: "#111111",
              backgroundColor: "#111111",
              pointRadius: 3,
              spanGaps: false,
            },
            {
              label: `${props.data.compared_runtime} ${props.data.compared_runtime_version || ""}`,
              data: pairs().map((pair) => pair.compared_p50_ns),
              borderColor: "#7c3aed",
              backgroundColor: "#7c3aed",
              pointRadius: 3,
              spanGaps: false,
            },
          ],
    };
  };
  const options = () => ({
    responsive: true,
    maintainAspectRatio: false,
    animation: false as const,
    interaction: { mode: "index" as const, intersect: false },
    scales: {
      y: {
        ticks: {
          callback: (value: string | number) =>
            props.mode === "relative" ? `${value}%` : formatNs(Number(value)),
        },
      },
    },
    plugins: {
      legend: { display: true, labels: { usePointStyle: true, boxWidth: 8 } },
      tooltip: {
        callbacks: {
          label: (context: any) =>
            `${context.dataset.label}: ${props.mode === "relative" ? `${context.parsed.y.toFixed(1)}%` : formatNs(context.parsed.y)}`,
        },
      },
    },
  });
  return <Line data={chartData()} options={options()} />;
};

export default RuntimeTrendChart;
