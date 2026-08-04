import { For, Show } from "solid-js";
import type { Component } from "solid-js";
import type { RunIdentity as RunIdentityData } from "../services/api";

const RunIdentity: Component<{ identity: RunIdentityData; compact?: boolean }> = (props) => {
  const fields = () =>
    [
      ["Suite", props.identity.benchmark_suite],
      ["Bun", props.identity.bun_version],
      ["Zig", props.identity.zig_version],
      ["Protocol", String(props.identity.protocol_version)],
      ["Manifest", props.identity.manifest_hash],
    ] as const;

  return (
    <Show when={props.identity.benchmark_kind === "js"}>
      <div
        class={`flex flex-wrap gap-x-3 gap-y-1 font-mono text-text-muted ${props.compact ? "text-[9px]" : "text-[10px]"}`}
      >
        <For each={fields()}>
          {([label, value]) => (
            <span class={label === "Manifest" ? "break-all" : "whitespace-nowrap"}>
              <strong class="text-text-main font-medium">{label}:</strong> {value || "-"}
            </span>
          )}
        </For>
      </div>
    </Show>
  );
};

export default RunIdentity;
