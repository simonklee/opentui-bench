import { createResource } from "solid-js";
import type { Component } from "solid-js";
import { A, useLocation, useNavigate } from "@solidjs/router";
import { api } from "../services/api";
import {
  lastViewedRunId,
  isSidebarExpanded,
  setIsSidebarExpanded,
} from "../store";
import { toggleHelp } from "../shortcuts";
import {
  LayoutDashboard,
  List,
  GitCompare,
  PanelLeftClose,
  PanelLeftOpen,
  HelpCircle,
  Activity,
} from "lucide-solid";

const Sidebar: Component = () => {
  const location = useLocation();
  const navigate = useNavigate();
  const [runs] = createResource(() => api.getRuns(1));

  const navItemClass =
    "nav-item flex items-center p-3 text-[13px] font-medium transition-all duration-150 cursor-pointer text-text-muted hover:text-black group border-r-2 border-transparent hover:bg-bg-hover";
  const activeClass = "!text-black !font-bold bg-bg-hover !border-black";

  // Transition class for the label text
  const labelClass = () =>
    `overflow-hidden whitespace-nowrap transition-all duration-300 ease-in-out font-ui tracking-wide ${isSidebarExpanded() ? "max-w-[150px] opacity-100 ml-3" : "max-w-0 opacity-0"}`;

  const handleBenchmarksClick = () => {
    const id = lastViewedRunId() || (runs() && runs()![0]?.id);
    if (id) {
      navigate(`/benchmarks/${id}`);
    }
  };

  return (
    <nav class="h-full bg-white border-r border-border flex flex-col z-50 w-full transition-all duration-300">
      <div
        class={`h-[56px] flex items-center border-b border-border ${isSidebarExpanded() ? "justify-between px-5" : "justify-center"}`}
      >
        <div
          class={`font-mono font-bold text-black text-[14px] flex items-center cursor-pointer overflow-hidden whitespace-nowrap transition-all duration-300 ${isSidebarExpanded() ? "max-w-[200px] opacity-100" : "max-w-0 opacity-0"}`}
          onClick={() => navigate("/")}
        >
          <Activity size={20} class="flex-shrink-0 mr-2" />
          <span class="tracking-widest text-[12px]">OpenTUI Bench</span>
        </div>

        <button
          onClick={() => setIsSidebarExpanded(!isSidebarExpanded())}
          class="text-text-muted hover:text-black p-1 transition-colors"
          title={isSidebarExpanded() ? "Collapse sidebar" : "Expand sidebar"}
        >
          {isSidebarExpanded() ? (
            <PanelLeftClose size={18} />
          ) : (
            <PanelLeftOpen size={18} />
          )}
        </button>
      </div>

      <div class="py-4 flex flex-col gap-1 flex-1">
        <div
          class={`${navItemClass} ${isSidebarExpanded() ? "" : "justify-center"} ${location.pathname === "/runs" || location.pathname === "/" ? activeClass + " active" : ""}`}
          onClick={() => navigate("/runs")}
          title={!isSidebarExpanded() ? "Runs" : ""}
        >
          <List
            size={18}
            strokeWidth={2}
            class="opacity-70 group-[.active]:opacity-100 flex-shrink-0"
          />
          <span class={labelClass()}>RUNS</span>
        </div>
        <div
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
        </div>
        <A
          href="/compare"
          class={`${navItemClass} ${isSidebarExpanded() ? "" : "justify-center"}`}
          activeClass={activeClass}
          title={!isSidebarExpanded() ? "Compare" : ""}
        >
          <GitCompare
            size={18}
            strokeWidth={2}
            class="opacity-70 group-[.active]:opacity-100 flex-shrink-0"
          />
          <span class={labelClass()}>COMPARE</span>
        </A>
      </div>

      <div
        class={`p-4 border-t border-border text-[10px] uppercase tracking-wider text-text-muted flex items-center ${isSidebarExpanded() ? "justify-between" : "justify-center"}`}
      >
        <div
          class={`overflow-hidden whitespace-nowrap transition-all duration-300 flex items-center ${isSidebarExpanded() ? "max-w-[200px] opacity-100" : "max-w-0 opacity-0"}`}
        >
          <span class="font-mono">v0.2.1</span>
          <span
            class="cursor-pointer hover:text-black hover:underline ml-4"
            onClick={toggleHelp}
          >
            Shortcuts
          </span>
        </div>
        <div
          class={`transition-all duration-300 ${isSidebarExpanded() ? "opacity-0 w-0 overflow-hidden" : "opacity-100 w-auto"}`}
        >
          <button
            onClick={toggleHelp}
            title="Shortcuts"
            class="hover:text-black p-1"
          >
            <HelpCircle size={16} />
          </button>
        </div>
      </div>
    </nav>
  );
};

export default Sidebar;
