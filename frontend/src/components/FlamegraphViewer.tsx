import { createResource, Show } from "solid-js";
import type { Component } from "solid-js";

interface Props {
  runId: number;
  resultId: number;
  view: "flamegraph" | "callgraph";
}

const FlamegraphViewer: Component<Props> = (props) => {
  const [svg] = createResource(
    () => ({ runId: props.runId, resultId: props.resultId, view: props.view }),
    async ({ runId, resultId, view }) => {
      const res = await fetch(`/api/runs/${runId}/results/${resultId}/${view}`);
      const text = await res.text();
      if (!res.ok) {
        throw new Error(text || `flamegraph generation failed (${res.status})`);
      }
      const contentType = res.headers.get("content-type") ?? "";
      if (!contentType.includes("svg") && !text.includes("<svg")) {
        throw new Error(text || "renderer did not return an SVG");
      }
      return text;
    },
  );

  return (
    <div class="w-full h-full bg-bg-panel rounded overflow-hidden relative min-h-[500px]">
      <Show when={svg.loading}>
        <div class="flex h-full items-center justify-center text-xs font-mono text-text-muted">
          Generating flamegraph…
        </div>
      </Show>
      <Show when={svg.error}>
        <div class="flex h-full items-center justify-center p-4 text-xs font-mono text-danger">
          {String(svg.error)}
        </div>
      </Show>
      <Show when={svg()}>
        <iframe title="Flamegraph" srcdoc={svg()} class="w-full h-full border-none" />
      </Show>
    </div>
  );
};

export default FlamegraphViewer;
