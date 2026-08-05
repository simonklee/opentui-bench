import type { Component } from "solid-js";
import { useLocation, useNavigate } from "@solidjs/router";
import type { JSRuntimeFilter } from "../services/api";
import { isSidebarExpanded, jsRuntimeFilter, setJSRuntimeFilter } from "../store";

const JSRuntimeSelector: Component = () => {
  const location = useLocation();
  const navigate = useNavigate();
  const select = (runtime: JSRuntimeFilter) => {
    setJSRuntimeFilter(runtime);
    const params = new URLSearchParams(location.search);
    params.set("benchmark_kind", "js");
    params.set("js_runtime", runtime);
    navigate(`${location.pathname}?${params}`);
  };

  return (
    <div class={`border-b border-border px-2 pb-2 ${isSidebarExpanded() ? "mx-3" : "mx-1"}`}>
      <div
        class={`grid border border-border ${isSidebarExpanded() ? "grid-cols-3" : "grid-cols-1"}`}
      >
        {(["bun", "node", "all"] as const).map((runtime) => (
          <button
            type="button"
            class={`px-1 py-1.5 text-[9px] font-mono font-bold uppercase tracking-wide transition-colors ${
              jsRuntimeFilter() === runtime
                ? "bg-black text-white"
                : "bg-white text-text-muted hover:text-black"
            } ${!isSidebarExpanded() && jsRuntimeFilter() !== runtime ? "hidden" : ""}`}
            title={`Show ${runtime === "all" ? "all JavaScript" : runtime} runs`}
            onClick={() => select(runtime)}
          >
            {isSidebarExpanded() ? runtime : runtime.slice(0, 1)}
          </button>
        ))}
      </div>
    </div>
  );
};

export default JSRuntimeSelector;
