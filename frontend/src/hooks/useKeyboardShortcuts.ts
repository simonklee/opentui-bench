import { onCleanup, onMount } from "solid-js";
import { useNavigate, useLocation } from "@solidjs/router";
import { isHelpOpen, toggleHelp, triggerCopy } from "../shortcuts";
import { benchmarkKind, jsRuntimeFilter, lastViewedRunId } from "../store";
import { api } from "../services/api";

export const useKeyboardShortcuts = () => {
  const navigate = useNavigate();
  const location = useLocation();

  const benchmarkSearch = () => {
    const params = new URLSearchParams({ benchmark_kind: benchmarkKind() });
    if (benchmarkKind() === "js") params.set("js_runtime", jsRuntimeFilter());
    return params.toString();
  };

  const handleKeyDown = async (e: KeyboardEvent) => {
    // Ignore if input is focused
    if (e.target instanceof HTMLInputElement || e.target instanceof HTMLSelectElement) return;

    if (e.key === "?") {
      toggleHelp();
      return;
    }

    if (e.key === "Escape") {
      if (isHelpOpen()) {
        toggleHelp();
        return;
      }
      if (location.pathname.startsWith("/benchmarks/")) {
        const search = new URLSearchParams(location.search);
        const kind = search.get("benchmark_kind") ?? benchmarkKind();
        // If we came from compare, go back there
        if (search.get("from") === "compare") {
          const base = search.get("compare_base");
          const curr = search.get("compare_curr");
          const params = new URLSearchParams();
          params.set("benchmark_kind", kind);
          if (base) params.set("base", base);
          if (curr) params.set("curr", curr);
          navigate(`/compare?${params.toString()}`);
          return;
        }
        if (search.get("from") === "regressions") {
          const params = new URLSearchParams({ benchmark_kind: kind });
          const branch = search.get("regression_branch");
          if (branch && branch !== "main") params.set("branch", branch);
          navigate(`/?${params.toString()}`);
          return;
        }
        if (location.search.includes("bench_id=")) {
          search.delete("bench_id");
          search.set("benchmark_kind", kind);
          const newSearch = search.toString();
          navigate(`${location.pathname}${newSearch ? `?${newSearch}` : ""}`);
          return;
        }
        const params = new URLSearchParams({ benchmark_kind: kind });
        if (kind === "js") params.set("js_runtime", jsRuntimeFilter());
        navigate(`/runs?${params.toString()}`);
        return;
      }
    }

    if (e.key === "/") {
      e.preventDefault();
      const searchInput = document.querySelector('input[type="text"]') as HTMLInputElement;
      if (searchInput) searchInput.focus();
      return;
    }

    // Copy shortcut
    if (e.key === "y") {
      triggerCopy();
      return;
    }

    // View Switching
    if (e.key === "1") navigate(`/runs?${benchmarkSearch()}`);
    if (e.key === "2") {
      // Navigate to last viewed or fetch latest
      let id = lastViewedRunId();
      if (!id) {
        try {
          const runs = await api.getRuns(1, benchmarkKind(), undefined, jsRuntimeFilter());
          if (runs && runs.length > 0 && runs[0]) id = runs[0].id;
        } catch (error) {
          console.error("Failed to fetch runs for shortcut", error);
        }
      }
      if (id) navigate(`/benchmarks/${id}?${benchmarkSearch()}`);
    }
    if (e.key === "3") navigate(`/compare?${benchmarkSearch()}`);
  };

  onMount(() => window.addEventListener("keydown", handleKeyDown));
  onCleanup(() => window.removeEventListener("keydown", handleKeyDown));
};
