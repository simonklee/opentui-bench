import { Show } from "solid-js";
import type { Component } from "solid-js";
import { useNavigate } from "@solidjs/router";
import type { TrendPoint } from "../services/api";

interface TrendIndicatorProps {
  trendData: TrendPoint[] | undefined;
  benchmarkName: string;
  currentRunId: number;
  fromCompare: boolean;
  compareBaseRunId?: string;
}

const TrendIndicator: Component<TrendIndicatorProps> = (props) => {
  const navigate = useNavigate();

  return (
    <Show when={props.trendData && props.trendData.length > 1} fallback={<span>No history</span>}>
      {(() => {
        // Find the current run in trend data (trend data is sorted most recent first)
        const currIndex = props.trendData!.findIndex((t) => t.run_id === props.currentRunId);

        // If current run not found or it's the last one (oldest), show no comparison
        if (currIndex < 0 || currIndex >= props.trendData!.length - 1) {
          return <span class="text-text-muted text-[12px]">No previous</span>;
        }

        const curr = props.trendData![currIndex]!;
        const prev = props.trendData![currIndex + 1]!; // Previous is next in array (older)
        const diff = curr.median_ns - prev.median_ns;

        let pctStr = "0.0%";
        if (prev.median_ns > 0) {
          const pct = (diff / prev.median_ns) * 100;
          pctStr = pct.toFixed(1) + "%";
        }

        const color = diff > 0 ? "text-danger" : diff < 0 ? "text-success" : "text-text-muted";

        const prevRunId =
          props.fromCompare && props.compareBaseRunId ? props.compareBaseRunId : prev.run_id;

        const prevUrl =
          props.fromCompare && props.compareBaseRunId
            ? `/benchmarks/${prevRunId}?name=${encodeURIComponent(props.benchmarkName)}`
            : `/benchmarks/${prevRunId}?bench_id=${prev.result_id}`;

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
              prev
            </a>
          </div>
        );
      })()}
    </Show>
  );
};

export default TrendIndicator;
