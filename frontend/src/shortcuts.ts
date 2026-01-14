import { createSignal } from "solid-js";

// Global shortcut state
export const [isHelpOpen, setIsHelpOpen] = createSignal(false);

export const toggleHelp = () => setIsHelpOpen(!isHelpOpen());

// Copy trigger - increments to signal a copy request
export const [copyTrigger, setCopyTrigger] = createSignal(0);

export const triggerCopy = () => setCopyTrigger(copyTrigger() + 1);
