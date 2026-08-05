import { createSignal } from "solid-js";
import type { BenchmarkKind, JSRuntimeFilter } from "./services/api";

export function benchmarkKindForLocation(pathname: string, search: string): BenchmarkKind | null {
  const urlKind = new URLSearchParams(search).get("benchmark_kind");
  if (urlKind === "js" || urlKind === "zig") return urlKind;
  return pathname === "/" || pathname === "/compare" ? "zig" : null;
}

const storedKind = window.localStorage.getItem("benchmark_kind");
const initialKind: BenchmarkKind =
  benchmarkKindForLocation(window.location.pathname, window.location.search) ??
  (storedKind === "js" ? "js" : "zig");

export const [lastViewedRunId, setLastViewedRunId] = createSignal<number | null>(null);
export const [benchmarkKind, setBenchmarkKindSignal] = createSignal<BenchmarkKind>(initialKind);
const urlRuntime = new URLSearchParams(window.location.search).get("js_runtime");
const storedRuntime = window.localStorage.getItem("js_runtime");
const isOldJavaScriptURL =
  new URLSearchParams(window.location.search).get("benchmark_kind") === "js" && !urlRuntime;
const initialRuntime: JSRuntimeFilter =
  urlRuntime === "node" || urlRuntime === "all" || urlRuntime === "bun"
    ? urlRuntime
    : isOldJavaScriptURL
      ? "bun"
      : storedRuntime === "node" || storedRuntime === "all"
        ? storedRuntime
        : "bun";
export const [jsRuntimeFilter, setJSRuntimeFilterSignal] =
  createSignal<JSRuntimeFilter>(initialRuntime);
export const setJSRuntimeFilter = (runtime: JSRuntimeFilter) => {
  window.localStorage.setItem("js_runtime", runtime);
  setLastViewedRunId(null);
  setJSRuntimeFilterSignal(runtime);
};
export const setBenchmarkKind = (kind: BenchmarkKind) => {
  window.localStorage.setItem("benchmark_kind", kind);
  setLastViewedRunId(null);
  setBenchmarkKindSignal(kind);
};
export const [isSidebarExpanded, setIsSidebarExpanded] = createSignal<boolean>(
  window.innerWidth >= 1024,
);
export const [globalFilter, setGlobalFilter] = createSignal("");
export const [globalCategory, setGlobalCategory] = createSignal("");
