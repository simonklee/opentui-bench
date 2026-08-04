import { createSignal } from "solid-js";
import type { BenchmarkKind } from "./services/api";

const urlKind = new URLSearchParams(window.location.search).get("benchmark_kind");
const storedKind = window.localStorage.getItem("benchmark_kind");
const initialKind: BenchmarkKind =
  urlKind === "js" || urlKind === "zig" ? urlKind : storedKind === "js" ? "js" : "zig";

export const [lastViewedRunId, setLastViewedRunId] = createSignal<number | null>(null);
export const [benchmarkKind, setBenchmarkKindSignal] = createSignal<BenchmarkKind>(initialKind);
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
