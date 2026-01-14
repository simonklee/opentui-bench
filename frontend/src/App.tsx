import { onCleanup, onMount } from "solid-js";
import type { Component } from "solid-js";
import { Router, Route, useNavigate, useLocation } from "@solidjs/router";
import Sidebar from "./components/Sidebar";
import RunsList from "./pages/RunsList";
import BenchmarkDetail from "./pages/BenchmarkDetail";
import Compare from "./pages/Compare";
import HelpModal from "./components/HelpModal";
import { isHelpOpen, toggleHelp, triggerCopy } from "./shortcuts";
import { api } from "./services/api";
import { lastViewedRunId, isSidebarExpanded } from "./store";

const Layout: Component<{ children?: any }> = (props) => {
  const navigate = useNavigate();
  const location = useLocation();

  const handleKeyDown = async (e: KeyboardEvent) => {
    if (e.target instanceof HTMLInputElement || e.target instanceof HTMLSelectElement) return;

    if (e.key === '?') {
        toggleHelp();
        return;
    }

    if (e.key === 'Escape') {
        if (isHelpOpen()) {
            toggleHelp();
            return;
        }
        if (location.pathname.startsWith('/benchmarks/')) {
            const search = new URLSearchParams(location.search);
            // If we came from compare, go back there
            if (search.get('from') === 'compare') {
                const base = search.get('compare_base');
                const curr = search.get('compare_curr');
                const params = new URLSearchParams();
                if (base) params.set('base', base);
                if (curr) params.set('curr', curr);
                navigate(`/compare?${params.toString()}`);
                return;
            }
            if (location.search.includes('bench_id=')) {
                search.delete('bench_id');
                const newSearch = search.toString();
                navigate(`${location.pathname}${newSearch ? `?${newSearch}` : ''}`);
                return;
            }
            navigate('/runs');
            return;
        }
    }

if (e.key === '/') {
         e.preventDefault();
         const searchInput = document.querySelector('input[type="text"]') as HTMLInputElement;
         if (searchInput) searchInput.focus();
         return;
     }

     // Copy shortcut
     if (e.key === 'y') {
         triggerCopy();
         return;
     }

    // View Switching
    if (e.key === '1') navigate('/runs');
    if (e.key === '2') {
        // Navigate to last viewed or fetch latest
        let id = lastViewedRunId();
        if (!id) {
            const runs = await api.getRuns(1);
            if (runs && runs.length > 0 && runs[0]) id = runs[0].id;
        }
        if (id) navigate(`/benchmarks/${id}`);
    }
    if (e.key === '3') navigate('/compare');
  };

  onMount(() => window.addEventListener('keydown', handleKeyDown));
  onCleanup(() => window.removeEventListener('keydown', handleKeyDown));

  return (
    <div 
        class="grid h-screen w-screen overflow-hidden transition-all duration-300 ease-in-out"
        classList={{
            "grid-cols-[240px_1fr]": isSidebarExpanded(),
            "grid-cols-[60px_1fr]": !isSidebarExpanded()
        }}
    >
      <Sidebar />
      <main class="bg-bg-dark relative flex flex-col overflow-hidden">
        {props.children}
      </main>
      <HelpModal isOpen={isHelpOpen()} onClose={toggleHelp} />
    </div>
  );
};

const baseUrl = import.meta.env.BASE_URL ?? "/";
const routerBase = baseUrl.replace(/\/$/, "") || "/";

const App: Component = () => {
  return (
    <Router root={Layout} base={routerBase}>
      <Route path="/" component={RunsList} />
      <Route path="/runs" component={RunsList} />
      <Route path="/benchmarks/:id" component={BenchmarkDetail} />
      <Route path="/compare" component={Compare} />
    </Router>
  );
};

export default App;
