import type { Component } from "solid-js";
import { useLocation, useNavigate } from "@solidjs/router";
import type { BenchmarkKind } from "../services/api";
import { benchmarkKind, isSidebarExpanded, jsRuntimeFilter, setBenchmarkKind } from "../store";

const BenchmarkKindSelector: Component = () => {
  const location = useLocation();
  const navigate = useNavigate();

  const select = (kind: BenchmarkKind) => {
    if (kind === benchmarkKind()) return;
    setBenchmarkKind(kind);
    const params = new URLSearchParams(location.search);
    params.set("benchmark_kind", kind);
    if (kind === "js") params.set("js_runtime", jsRuntimeFilter());
    if (kind === "js" && location.pathname === "/") {
      navigate(`/runs?${params}`);
    } else if (location.pathname.startsWith("/benchmarks/")) {
      navigate(`/runs?${params}`);
    } else {
      navigate(`${location.pathname}?${params}`);
    }
  };

  const handleCollapsedClick = () => select(benchmarkKind() === "zig" ? "js" : "zig");

  return (
    <div
      class={`border-b border-border p-2 ${isSidebarExpanded() ? "mx-3" : "mx-1"}`}
      aria-label="Benchmark language"
    >
      <div
        class={`grid border border-border ${isSidebarExpanded() ? "grid-cols-2" : "grid-cols-1"}`}
      >
        {(["zig", "js"] as const).map((kind) => (
          <button
            type="button"
            class={`px-2 py-1.5 text-[10px] font-mono font-bold uppercase tracking-wider transition-colors ${
              benchmarkKind() === kind
                ? "bg-black text-white"
                : "bg-white text-text-muted hover:text-black"
            } ${!isSidebarExpanded() && benchmarkKind() !== kind ? "hidden" : ""}`}
            title={kind === "zig" ? "Zig benchmarks" : "JavaScript benchmarks"}
            onClick={() => (isSidebarExpanded() ? select(kind) : handleCollapsedClick())}
          >
            {kind === "zig" ? "Zig" : isSidebarExpanded() ? "JavaScript" : "JS"}
          </button>
        ))}
      </div>
    </div>
  );
};

export default BenchmarkKindSelector;
