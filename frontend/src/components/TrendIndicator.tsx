import { Show } from "solid-js";
import type { Component } from "solid-js";
import { useNavigate } from "@solidjs/router";
import type { TrendPoint } from "../services/api";

interface TrendIndicatorProps {
  trendData: TrendPoint[] | undefined;
  benchmarkName: string;
  fromCompare: boolean;
  compareBaseRunId?: string;
}

const TrendIndicator: Component<TrendIndicatorProps> = (props) => {
  const navigate = useNavigate();

  return (
    <Show when={props.trendData && props.trendData.length > 1} fallback={<span>No history</span>}>
        {(() => {
            const curr = props.trendData![0]!;
            const prev = props.trendData![1]!;
            const diff = curr.avg_ns - prev.avg_ns;
            const pct = (diff/prev.avg_ns)*100;
            const color = diff > 0 ? 'text-danger' : (diff < 0 ? 'text-success' : 'text-text-muted');
            
            const prevRunId = props.fromCompare && props.compareBaseRunId
                ? props.compareBaseRunId
                : prev.run_id;
            
            const prevUrl = props.fromCompare && props.compareBaseRunId
                ? `/benchmarks/${prevRunId}?name=${encodeURIComponent(props.benchmarkName)}`
                : `/benchmarks/${prevRunId}?bench_id=${prev.result_id}`;

            return (
                <>
                    <span class={`${color} font-semibold`}>{diff>0?'+':''}{pct.toFixed(1)}%</span>
                    {' vs '}
                    <a
                        href={prevUrl}
                        class="text-accent hover:underline cursor-pointer"
                        onClick={(e) => { e.preventDefault(); navigate(prevUrl); }}
                    >prev</a>
                </>
            );
        })()}
    </Show>
  );
};

export default TrendIndicator;
