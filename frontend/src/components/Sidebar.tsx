import { createResource } from "solid-js";
import type { Component } from "solid-js";
import { A, useLocation, useNavigate } from "@solidjs/router";
import { api } from "../services/api";
import { lastViewedRunId, isSidebarExpanded, setIsSidebarExpanded } from "../store";
import { toggleHelp } from "../shortcuts";
import { 
    LayoutDashboard, 
    List, 
    GitCompare, 
    PanelLeftClose, 
    PanelLeftOpen, 
    HelpCircle,
    Activity
} from "lucide-solid";

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
                <Activity size={20} class="flex-shrink-0 mr-2" />
                <span>OpenTUI <span class="text-text-muted font-normal">Bench</span></span>
            </div>
            
            <button 
                onClick={() => setIsSidebarExpanded(!isSidebarExpanded())}
                class="text-text-muted hover:text-text-main p-1 rounded hover:bg-black/5 transition-colors"
                title={isSidebarExpanded() ? "Collapse sidebar" : "Expand sidebar"}
            >
                {isSidebarExpanded() ? (
                    <PanelLeftClose size={18} />
                ) : (
                    <PanelLeftOpen size={18} />
                )}
            </button>
        </div>
        
        <div class="py-4 px-2 lg:px-3 flex flex-col gap-1 flex-1">
            <div 
                class={`${navItemClass} ${isSidebarExpanded() ? '' : 'justify-center'} ${location.pathname === '/runs' || location.pathname === '/' ? activeClass + " active" : ""}`}
                onClick={() => navigate('/runs')}
                title={!isSidebarExpanded() ? "Runs" : ""}
            >
                <List size={16} class="opacity-70 group-[.active]:opacity-100 flex-shrink-0" />
                <span class={labelClass()}>Runs</span>
            </div>
            <div 
                class={`${navItemClass} ${isSidebarExpanded() ? '' : 'justify-center'} ${location.pathname.startsWith('/benchmarks') ? activeClass + " active" : ""}`} 
                onClick={handleBenchmarksClick}
                title={!isSidebarExpanded() ? "Benchmarks" : ""}
            >
                <LayoutDashboard size={16} class="opacity-70 group-[.active]:opacity-100 flex-shrink-0" />
                <span class={labelClass()}>Benchmarks</span>
            </div>
            <A 
                href="/compare" 
                class={`${navItemClass} ${isSidebarExpanded() ? '' : 'justify-center'}`} 
                activeClass={activeClass}
                title={!isSidebarExpanded() ? "Compare" : ""}
            >
                <GitCompare size={16} class="opacity-70 group-[.active]:opacity-100 flex-shrink-0" />
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
                     <HelpCircle size={16} />
                 </button>
            </div>
        </div>
    </nav>
  );
};

export default Sidebar;
