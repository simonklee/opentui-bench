import { onCleanup, onMount } from "solid-js";
import type { Component, ParentComponent } from "solid-js";
import { Router, Route } from "@solidjs/router";
import Sidebar from "./components/Sidebar";
import Regressions from "./pages/Regressions";
import RunsList from "./pages/RunsList";
import BenchmarkDetail from "./pages/BenchmarkDetail";
import Compare from "./pages/Compare";
import HelpModal from "./components/HelpModal";
import { isHelpOpen, toggleHelp } from "./shortcuts";
import { isSidebarExpanded, setIsSidebarExpanded } from "./store";
import { useKeyboardShortcuts } from "./hooks/useKeyboardShortcuts";

const Layout: ParentComponent = (props) => {
  useKeyboardShortcuts();

  // Handle window resize to auto-collapse/expand
  const handleResize = () => {
    if (window.innerWidth < 1024) setIsSidebarExpanded(false);
    else setIsSidebarExpanded(true);
  };

  onMount(() => window.addEventListener('resize', handleResize));
  onCleanup(() => window.removeEventListener('resize', handleResize));

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
      <Route path="/" component={Regressions} />
      <Route path="/runs" component={RunsList} />
      <Route path="/benchmarks/:id" component={BenchmarkDetail} />
      <Route path="/compare" component={Compare} />
    </Router>
  );
};

export default App;
