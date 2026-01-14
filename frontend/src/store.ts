import { createSignal } from "solid-js";

export const [lastViewedRunId, setLastViewedRunId] = createSignal<number | null>(null);
export const [isSidebarExpanded, setIsSidebarExpanded] = createSignal<boolean>(window.innerWidth >= 1024);
