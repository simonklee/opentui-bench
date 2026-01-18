import type { Component } from "solid-js";
import { Portal } from "solid-js/web";

interface HelpModalProps {
  isOpen: boolean;
  onClose: () => void;
}

const HelpModal: Component<HelpModalProps> = (props) => {
  return (
    <Portal>
      <div
        class={`fixed inset-0 bg-black/50 z-[3000] flex items-center justify-center font-ui ${props.isOpen ? "flex" : "hidden"}`}
        onClick={(e) => {
          if (e.target === e.currentTarget) props.onClose();
        }}
      >
        <div class="bg-bg-dark rounded-lg w-[400px] shadow-xl">
          <div class="p-4 border-b border-border font-semibold flex justify-between items-center text-text-main">
            <span>Shortcuts</span>
            <span
              class="cursor-pointer text-text-muted hover:text-text-main"
              onClick={props.onClose}
            >
              ✕
            </span>
          </div>
          <div class="p-6">
            <div class="flex justify-between mb-3 text-[13px] text-text-main">
              <span>Next/Prev Run</span>
              <div>
                <span class="bg-bg-panel border border-border px-1.5 py-0.5 rounded font-mono text-[11px]">
                  J
                </span>{" "}
                /{" "}
                <span class="bg-bg-panel border border-border px-1.5 py-0.5 rounded font-mono text-[11px]">
                  K
                </span>
              </div>
            </div>
            <div class="flex justify-between mb-3 text-[13px] text-text-main">
              <span>Switch View</span>
              <div>
                <span class="bg-bg-panel border border-border px-1.5 py-0.5 rounded font-mono text-[11px]">
                  1
                </span>{" "}
                -{" "}
                <span class="bg-bg-panel border border-border px-1.5 py-0.5 rounded font-mono text-[11px]">
                  3
                </span>
              </div>
            </div>
            <div class="flex justify-between mb-3 text-[13px] text-text-main">
              <span>Search</span>
              <span class="bg-bg-panel border border-border px-1.5 py-0.5 rounded font-mono text-[11px]">
                /
              </span>
            </div>
            <div class="flex justify-between mb-3 text-[13px] text-text-main">
              <span>Close / Exit Search</span>
              <span class="bg-bg-panel border border-border px-1.5 py-0.5 rounded font-mono text-[11px]">
                ESC
              </span>
            </div>
            <div class="flex justify-between mb-3 text-[13px] text-text-main">
              <span>Copy as Markdown</span>
              <span class="bg-bg-panel border border-border px-1.5 py-0.5 rounded font-mono text-[11px]">
                Y
              </span>
            </div>
            <div class="flex justify-between mb-3 text-[13px] text-text-main">
              <span>Help</span>
              <span class="bg-bg-panel border border-border px-1.5 py-0.5 rounded font-mono text-[11px]">
                ?
              </span>
            </div>
          </div>
        </div>
      </div>
    </Portal>
  );
};

export default HelpModal;
