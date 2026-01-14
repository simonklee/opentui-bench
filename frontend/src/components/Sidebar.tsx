import { createResource, Show } from "solid-js";
import type { Component } from "solid-js";
import { A, useLocation, useNavigate } from "@solidjs/router";
import { api } from "../services/api";
import { lastViewedRunId, isSidebarExpanded, setIsSidebarExpanded } from "../store";
import { toggleHelp } from "../shortcuts";

const Sidebar: Component = () => {
  const location = useLocation();
  const navigate = useNavigate();
  const [runs] = createResource(() => api.getRuns(1));

  // Removed gap-3, handling spacing via margin on the text label for smooth transition
  const navItemClass = "nav-item flex items-center p-3 rounded-md text-[13px] font-medium transition-all duration-150 cursor-pointer text-text-muted hover:bg-black/5 hover:text-text-main group";
  const activeClass = "!bg-bg-dark !text-accent !font-semibold shadow-[var(--shadow-nav-active)]";

  // Transition class for the label text
  const labelClass = () => `overflow-hidden whitespace-nowrap transition-all duration-300 ease-in-out ${isSidebarExpanded() ? 'max-w-[150px] opacity-100 ml-3' : 'max-w-0 opacity-0'}`;

  const handleBenchmarksClick = () => {
      const id = lastViewedRunId() || (runs() && runs()![0]?.id);
      if (id) {
          navigate(`/benchmarks/${id}`);
      }
  };

  return (
    <nav class="h-full bg-bg-panel border-r border-border flex flex-col z-50 w-full transition-all duration-300">
        <div class={`h-[56px] flex items-center border-b border-border ${isSidebarExpanded() ? 'justify-between px-5' : 'justify-center'}`}>
            <div 
                class={`font-mono font-bold text-accent text-[14px] flex items-center cursor-pointer hover:opacity-80 overflow-hidden whitespace-nowrap transition-all duration-300 ${isSidebarExpanded() ? 'max-w-[200px] opacity-100' : 'max-w-0 opacity-0'}`} 
                onClick={() => navigate('/')}
            >
                <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" class="flex-shrink-0 mr-2"><path d="M12 2v20M2 12h20M4.93 4.93l14.14 14.14M4.93 19.07l14.14-14.14"/></svg>
                <span>OpenTUI <span class="text-text-muted font-normal">Bench</span></span>
            </div>
            
            <button 
                onClick={() => setIsSidebarExpanded(!isSidebarExpanded())}
                class="text-text-muted hover:text-text-main p-1 rounded hover:bg-black/5 transition-colors"
                title={isSidebarExpanded() ? "Collapse sidebar" : "Expand sidebar"}
            >
                {isSidebarExpanded() ? (
                    <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect width="18" height="18" x="3" y="3" rx="2" ry="2"/><line x1="9" x2="9" y1="3" y2="21"/><path d="m15 14-2-2 2-2"/></svg>
                ) : (
                    <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect width="18" height="18" x="3" y="3" rx="2" ry="2"/><line x1="9" x2="9" y1="3" y2="21"/><path d="m15 10 2 2-2 2"/></svg>
                )}
            </button>
        </div>
        
        <div class="py-4 px-2 lg:px-3 flex flex-col gap-1 flex-1">
            <div 
                class={`${navItemClass} ${isSidebarExpanded() ? '' : 'justify-center'} ${location.pathname === '/runs' || location.pathname === '/' ? activeClass + " active" : ""}`}
                onClick={() => navigate('/runs')}
                title={!isSidebarExpanded() ? "Runs" : ""}
            >
                <svg class="w-4 h-4 opacity-70 group-[.active]:opacity-100 flex-shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="8" y1="6" x2="21" y2="6"></line><line x1="8" y1="12" x2="21" y2="12"></line><line x1="8" y1="18" x2="21" y2="18"></line><line x1="3" y1="6" x2="3.01" y2="6"></line><line x1="3" y1="12" x2="3.01" y2="12"></line><line x1="3" y1="18" x2="3.01" y2="18"></line></svg>
                <span class={labelClass()}>Runs</span>
            </div>
            <div 
                class={`${navItemClass} ${isSidebarExpanded() ? '' : 'justify-center'} ${location.pathname.startsWith('/benchmarks') ? activeClass + " active" : ""}`} 
                onClick={handleBenchmarksClick}
                title={!isSidebarExpanded() ? "Benchmarks" : ""}
            >
                <svg class="w-4 h-4 opacity-70 group-[.active]:opacity-100 flex-shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="20" x2="18" y2="10"></line><line x1="12" y1="20" x2="12" y2="4"></line><line x1="6" y1="20" x2="6" y2="14"></line></svg>
                <span class={labelClass()}>Benchmarks</span>
            </div>
            <A 
                href="/compare" 
                class={`${navItemClass} ${isSidebarExpanded() ? '' : 'justify-center'}`} 
                activeClass={activeClass}
                title={!isSidebarExpanded() ? "Compare" : ""}
            >
                <svg class="w-4 h-4 opacity-70 group-[.active]:opacity-100 flex-shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M16 3h5v5"></path><path d="M4 20L21 3"></path><path d="M21 16v5h-5"></path><path d="M15 15l5 5"></path><path d="M4 4l5 5"></path></svg>
                <span class={labelClass()}>Compare</span>
            </A>
        </div>

        <div class={`p-4 border-t border-border text-[11px] text-text-muted flex items-center ${isSidebarExpanded() ? 'justify-between' : 'justify-center'}`}>
            <div class={`overflow-hidden whitespace-nowrap transition-all duration-300 flex items-center ${isSidebarExpanded() ? 'max-w-[200px] opacity-100' : 'max-w-0 opacity-0'}`}>
                <span>v0.2.1</span>
                <span class="cursor-pointer hover:text-accent ml-4" onClick={toggleHelp}>Shortcuts</span>
            </div>
            <div class={`transition-all duration-300 ${isSidebarExpanded() ? 'opacity-0 w-0 overflow-hidden' : 'opacity-100 w-auto'}`}>
                 <button onClick={toggleHelp} title="Shortcuts" class="hover:text-accent p-1">
                     <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><path d="M9.09 9a3 3 0 0 1 5.83 1c0 2-3 3-3 3"/><path d="M12 17h.01"/></svg>
                 </button>
            </div>
        </div>
    </nav>
  );
};

export default Sidebar;
