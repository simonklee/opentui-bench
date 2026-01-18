import { Show } from "solid-js";
import type { Component } from "solid-js";
import { formatDate } from "../utils/format";
import type { Run } from "../services/api";

interface StatsBarProps {
  runs: Run[] | undefined;
  loading: boolean;
}

const StatsBar: Component<StatsBarProps> = (props) => {
  return (
    <div class="flex-none px-6 py-3 border-b border-border bg-bg-panel flex gap-6 text-[12px] text-text-muted">
      <div class="flex gap-1">
        <span>Total Runs:</span>
        <strong class="text-text-main font-semibold">
          <Show when={props.runs} fallback="-">
            {props.runs?.length}
          </Show>
        </strong>
      </div>
      <div class="flex gap-1">
        <span>Latest:</span>
        <strong class="text-text-main font-semibold">
          <Show when={props.runs && props.runs[0]}>
            {(run) => (
              <a
                href={`https://github.com/anomalyco/opentui/commit/${run().commit_hash}`}
                target="_blank"
                class="font-mono text-accent hover:underline decoration-accent"
              >
                #{run().commit_hash.substring(0, 7)}
              </a>
            )}
          </Show>
          <Show when={!props.runs || props.runs.length === 0}>-</Show>
        </strong>
      </div>
      <div class="flex gap-1">
        <span>Date:</span>
        <strong class="text-text-main font-semibold">
          <Show when={props.runs && props.runs[0]}>{(run) => formatDate(run().run_date)}</Show>
          <Show when={!props.runs || props.runs.length === 0}>-</Show>
        </strong>
      </div>
    </div>
  );
};

export default StatsBar;
