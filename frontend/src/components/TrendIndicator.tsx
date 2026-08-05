import { Show } from "solid-js";
import type { Component } from "solid-js";
import { useNavigate } from "@solidjs/router";
import type { TrendPoint } from "../services/api";

interface TrendIndicatorProps {
  trendData: TrendPoint[] | undefined;
  currentRunId: number;
  neutral?: boolean;
  fromCompare: boolean;
  compareBaseResultId?: string;
}

const TrendIndicator: Component<TrendIndicatorProps> = (props) => {
  const navigate = useNavigate();

  return (
    <Show when={props.trendData && props.trendData.length > 1} fallback={<span>No history</span>}>
      {(() => {
        // Find the current run in trend data (trend data is sorted most recent first)
        const currIndex = props.trendData!.findIndex((t) => t.run_id === props.currentRunId);

        if (currIndex < 0) {
          return <span class="text-text-muted text-[12px]">No previous</span>;
        }

        const curr = props.trendData![currIndex]!;
        const compareBase = props.fromCompare
          ? props.trendData!.find((point) => String(point.result_id) === props.compareBaseResultId)
          : undefined;
        const prev = compareBase ?? props.trendData![currIndex + 1];
        if (!prev || (props.fromCompare && !compareBase)) {
          return <span class="text-text-muted text-[12px]">No baseline</span>;
        }
        const currentValue = compareBase ? curr.median_ns : curr.avg_ns;
        const previousValue = compareBase ? prev.median_ns : prev.avg_ns;
        const diff = currentValue - previousValue;

        let pctStr = "0.0%";
        if (previousValue > 0) {
          const pct = (diff / previousValue) * 100;
          pctStr = pct.toFixed(1) + "%";
        }

        const color = props.neutral
          ? "text-text-main"
          : diff > 0
            ? "text-danger"
            : diff < 0
              ? "text-success"
              : "text-text-muted";

        const prevRunId = prev.run_id;
        const prevResultId = String(prev.result_id);
        const prevUrl = prevResultId
          ? `/benchmarks/${prevRunId}?bench_id=${prevResultId}`
          : `/benchmarks/${prevRunId}`;

        return (
          <div class="flex items-baseline font-mono text-[14px]">
            <span class={`${color} font-bold`}>
              {diff > 0 ? "+" : ""}
              {pctStr}
            </span>
            <span class="text-text-muted mx-1.5 text-[11px] font-ui uppercase tracking-wider font-medium">
              vs
            </span>
            <a
              href={prevUrl}
              class="text-black hover:underline cursor-pointer decoration-dotted underline-offset-2 text-[12px]"
              onClick={(e) => {
                e.preventDefault();
                navigate(prevUrl);
              }}
            >
              {compareBase ? "base" : "prev"}
            </a>
          </div>
        );
      })()}
    </Show>
  );
};

export default TrendIndicator;
