import { createResource, For, Show } from "solid-js";
import type { Component } from "solid-js";
import { useNavigate } from "@solidjs/router";
import { api } from "../services/api";

const Investigations: Component = () => {
  const navigate = useNavigate();
  const [items] = createResource(() => api.getInvestigations(50));

  return (
    <div class="flex h-full flex-col overflow-auto p-6">
      <h1 class="mb-4 text-[16px] font-bold tracking-wide">Investigations</h1>
      <Show when={items.error}>
        <div class="text-sm text-danger">{String(items.error)}</div>
      </Show>
      <table class="w-full bg-white">
        <thead class="text-left text-[11px] uppercase tracking-wider text-text-muted">
          <tr>
            <th class="py-2 px-3">ID</th>
            <th class="py-2 px-3">Benchmark</th>
            <th class="py-2 px-3">Revisions</th>
            <th class="py-2 px-3">Status</th>
          </tr>
        </thead>
        <tbody>
          <For each={items() ?? []}>
            {(item) => (
              <tr
                class="cursor-pointer border-t border-border hover:bg-bg-hover"
                onClick={() => navigate(`/investigations/${item.id}`)}
              >
                <td class="py-2 px-3 font-mono">#{item.id}</td>
                <td class="py-2 px-3">
                  {item.category} / {item.name}
                </td>
                <td class="py-2 px-3 font-mono text-[12px]">
                  {item.baseline_commit.slice(0, 8)} → {item.target_commit.slice(0, 8)}
                </td>
                <td class="py-2 px-3">{item.status}</td>
              </tr>
            )}
          </For>
        </tbody>
      </table>
    </div>
  );
};

export default Investigations;
