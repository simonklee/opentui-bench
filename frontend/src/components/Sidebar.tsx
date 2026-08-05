import { createResource, Show } from "solid-js";
import type { Component } from "solid-js";
import { useLocation, useNavigate } from "@solidjs/router";
import { api } from "../services/api";
import {
  benchmarkKind,
  lastViewedRunId,
  isSidebarExpanded,
  jsRuntimeFilter,
  setIsSidebarExpanded,
} from "../store";
import BenchmarkKindSelector from "./BenchmarkKindSelector";
import JSRuntimeSelector from "./JSRuntimeSelector";
import { toggleHelp } from "../shortcuts";
import {
  LayoutDashboard,
  List,
  GitCompare,
  GitBranch,
  PanelLeftClose,
  PanelLeftOpen,
  HelpCircle,
  Activity,
} from "lucide-solid";

const Sidebar: Component = () => {
  const location = useLocation();
  const navigate = useNavigate();
  const [loadedRuns] = createResource(
    () => [benchmarkKind(), jsRuntimeFilter()] as const,
    async ([kind, runtime]) => ({
      kind,
      runs: await api.getRuns(1, kind, undefined, runtime),
    }),
  );

  const navItemClass =
    "nav-item flex w-full items-center border-0 bg-transparent p-3 text-left text-[13px] font-medium transition-all duration-150 cursor-pointer text-text-muted hover:text-black group border-r-2 border-transparent hover:bg-bg-hover";
  const activeClass = "!text-black !font-bold bg-bg-hover !border-black";

  // Transition class for the label text
  const labelClass = () =>
    `overflow-hidden whitespace-nowrap transition-all duration-300 ease-in-out font-ui tracking-wide ${isSidebarExpanded() ? "max-w-[150px] opacity-100 ml-3" : "max-w-0 opacity-0"}`;

  const handleBenchmarksClick = () => {
    const kind = benchmarkKind();
    const loaded = loadedRuns();
    const latestRunId = !loadedRuns.loading && loaded?.kind === kind ? loaded.runs[0]?.id : null;
    const id = lastViewedRunId() || latestRunId;
    if (id) {
      navigate(`/benchmarks/${id}?benchmark_kind=${kind}&js_runtime=${jsRuntimeFilter()}`);
    }
  };

  const handleCompareClick = () => {
    const currentRunId = lastViewedRunId();
    if (currentRunId) {
      // Pre-select the current run as "current" in compare
      navigate(
        `/compare?benchmark_kind=${benchmarkKind()}&js_runtime=${jsRuntimeFilter()}&curr=${currentRunId}`,
      );
    } else {
      navigate(`/compare?benchmark_kind=${benchmarkKind()}&js_runtime=${jsRuntimeFilter()}`);
    }
  };

  return (
    <nav class="h-full bg-white border-r border-border flex flex-col z-50 w-full transition-all duration-300">
      <div
        class={`h-[56px] flex items-center border-b border-border ${isSidebarExpanded() ? "justify-between px-5" : "justify-center"}`}
      >
        <button
          type="button"
          class={`border-0 bg-transparent p-0 text-left font-mono font-bold text-black text-[14px] flex items-center cursor-pointer overflow-hidden whitespace-nowrap transition-all duration-300 ${isSidebarExpanded() ? "max-w-[200px] opacity-100" : "max-w-0 opacity-0"}`}
          onClick={() =>
            navigate(
              benchmarkKind() === "zig"
                ? "/"
                : `/runs?benchmark_kind=${benchmarkKind()}&js_runtime=${jsRuntimeFilter()}`,
            )
          }
        >
          <Activity size={20} class="flex-shrink-0 mr-2" />
          <span class="tracking-widest text-[12px]">OpenTUI Bench</span>
        </button>

        <button
          onClick={() => setIsSidebarExpanded(!isSidebarExpanded())}
          class="text-text-muted hover:text-black p-1 transition-colors"
          title={isSidebarExpanded() ? "Collapse sidebar" : "Expand sidebar"}
        >
          {isSidebarExpanded() ? <PanelLeftClose size={18} /> : <PanelLeftOpen size={18} />}
        </button>
      </div>

      <BenchmarkKindSelector />
      <Show when={benchmarkKind() === "js"}>
        <JSRuntimeSelector />
      </Show>

      <div class="py-4 flex flex-col gap-1 flex-1">
        <Show when={benchmarkKind() === "zig"}>
          <button
            type="button"
            class={`${navItemClass} ${isSidebarExpanded() ? "" : "justify-center"} ${location.pathname === "/" ? activeClass + " active" : ""}`}
            onClick={() => navigate("/")}
            title={!isSidebarExpanded() ? "Regressions" : ""}
          >
            <Activity
              size={18}
              strokeWidth={2}
              class="opacity-70 group-[.active]:opacity-100 flex-shrink-0"
            />
            <span class={labelClass()}>REGRESSIONS</span>
          </button>
        </Show>
        <button
          type="button"
          class={`${navItemClass} ${isSidebarExpanded() ? "" : "justify-center"} ${location.pathname === "/runs" ? activeClass + " active" : ""}`}
          onClick={() =>
            navigate(`/runs?benchmark_kind=${benchmarkKind()}&js_runtime=${jsRuntimeFilter()}`)
          }
          title={!isSidebarExpanded() ? "Runs" : ""}
        >
          <List
            size={18}
            strokeWidth={2}
            class="opacity-70 group-[.active]:opacity-100 flex-shrink-0"
          />
          <span class={labelClass()}>RUNS</span>
        </button>
        <button
          type="button"
          class={`${navItemClass} ${isSidebarExpanded() ? "" : "justify-center"} ${location.pathname.startsWith("/benchmarks") ? activeClass + " active" : ""}`}
          onClick={handleBenchmarksClick}
          title={!isSidebarExpanded() ? "Benchmarks" : ""}
        >
          <LayoutDashboard
            size={18}
            strokeWidth={2}
            class="opacity-70 group-[.active]:opacity-100 flex-shrink-0"
          />
          <span class={labelClass()}>BENCHMARKS</span>
        </button>
        <button
          type="button"
          class={`${navItemClass} ${isSidebarExpanded() ? "" : "justify-center"} ${location.pathname === "/compare" ? activeClass + " active" : ""}`}
          onClick={handleCompareClick}
          title={!isSidebarExpanded() ? "Compare" : ""}
        >
          <GitCompare
            size={18}
            strokeWidth={2}
            class="opacity-70 group-[.active]:opacity-100 flex-shrink-0"
          />
          <span class={labelClass()}>COMPARE</span>
        </button>
      </div>

      <div
        class={`p-4 border-t border-border text-[10px] uppercase tracking-wider text-text-muted flex items-center ${isSidebarExpanded() ? "justify-between" : "justify-center"}`}
      >
        <div
          class={`overflow-hidden whitespace-nowrap transition-all duration-300 flex items-center gap-4 ${isSidebarExpanded() ? "max-w-[250px] opacity-100" : "max-w-0 opacity-0"}`}
        >
          <a
            href="https://github.com/simonklee/opentui-bench"
            target="_blank"
            rel="noopener noreferrer"
            class="hover:text-black transition-colors flex items-center gap-1"
            title="opentui-bench on GitHub"
          >
            <GitBranch size={12} />
            <span>Source</span>
          </a>
          <form
            method="post"
            action="/api/database/download"
            onSubmit={(event) => {
              if (!confirm("Download the full SQLite database (currently over 1 GB)?")) {
                event.preventDefault();
              }
            }}
          >
            <button
              type="submit"
              class="cursor-pointer border-0 bg-transparent p-0 hover:text-black hover:underline"
              title="Download SQLite database"
            >
              Export
            </button>
          </form>
          <button
            type="button"
            class="cursor-pointer border-0 bg-transparent p-0 hover:text-black hover:underline"
            onClick={toggleHelp}
          >
            Shortcuts
          </button>
        </div>
        <div
          class={`transition-all duration-300 ${isSidebarExpanded() ? "opacity-0 w-0 overflow-hidden" : "opacity-100 w-auto"}`}
        >
          <button onClick={toggleHelp} title="Shortcuts" class="hover:text-black p-1">
            <HelpCircle size={16} />
          </button>
        </div>
      </div>
    </nav>
  );
};

export default Sidebar;
