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
            
            let pctStr = "0.0%";
            if (prev.avg_ns > 0) {
                const pct = (diff/prev.avg_ns)*100;
                pctStr = pct.toFixed(1) + "%";
            }
            
            const color = diff > 0 ? 'text-danger' : (diff < 0 ? 'text-success' : 'text-text-muted');
            
            const prevRunId = props.fromCompare && props.compareBaseRunId
                ? props.compareBaseRunId
                : prev.run_id;
            
            const prevUrl = props.fromCompare && props.compareBaseRunId
                ? `/benchmarks/${prevRunId}?name=${encodeURIComponent(props.benchmarkName)}`
                : `/benchmarks/${prevRunId}?bench_id=${prev.result_id}`;

            return (
                <div class="flex items-baseline font-mono text-[14px]">
                    <span class={`${color} font-bold`}>{diff>0?'+':''}{pctStr}</span>
                    <span class="text-text-muted mx-1.5 text-[11px] font-ui uppercase tracking-wider font-medium">vs</span>
                    <a
                        href={prevUrl}
                        class="text-black hover:underline cursor-pointer decoration-dotted underline-offset-2 text-[12px]"
                        onClick={(e) => { e.preventDefault(); navigate(prevUrl); }}
                    >prev</a>
                </div>
            );
        })()}
    </Show>
  );
};

export default TrendIndicator;
