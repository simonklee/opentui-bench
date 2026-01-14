import type { Component } from "solid-js";

interface Props {
    runId: number;
    resultId: number;
    view: 'flamegraph' | 'callgraph';
}

const FlamegraphViewer: Component<Props> = (props) => {
    return (
        <div class="w-full h-full bg-bg-panel rounded overflow-hidden relative min-h-[500px]">
            <iframe
                src={`/api/runs/${props.runId}/results/${props.resultId}/${props.view}`}
                class="w-full h-full border-none"
            />
        </div>
    );
};

export default FlamegraphViewer;
