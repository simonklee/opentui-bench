import { createSignal } from "solid-js";

export const [lastViewedRunId, setLastViewedRunId] = createSignal<number | null>(null);
export const [isSidebarExpanded, setIsSidebarExpanded] = createSignal<boolean>(window.innerWidth >= 1024);

// Handle window resize to auto-collapse/expand
window.addEventListener('resize', () => {
    if (window.innerWidth < 1024) setIsSidebarExpanded(false);
    else setIsSidebarExpanded(true);
});
