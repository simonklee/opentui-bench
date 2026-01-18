import { splitProps } from "solid-js";
import type { Component, JSX } from "solid-js";

interface ButtonProps extends JSX.ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: "default" | "primary" | "active";
  active?: boolean;
}

export const Button: Component<ButtonProps> = (props) => {
  const [local, others] = splitProps(props, ["variant", "active", "class", "children"]);

  const baseClass =
    "px-4 py-1.5 rounded-none text-[11px] uppercase tracking-wider font-semibold border transition-all flex items-center gap-2 cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed disabled:bg-transparent";

  const variantClasses = () => {
    if (local.variant === "primary") {
      return "bg-black text-white border-black hover:bg-neutral-800 hover:border-neutral-800";
    }
    if (local.variant === "active" || local.active) {
      return "bg-black text-white border-black";
    }
    // Default: outlined
    return "bg-transparent border-border-strong text-text-main hover:bg-black hover:text-white hover:border-black";
  };

  return (
    <button class={`${baseClass} ${variantClasses()} ${local.class || ""}`} {...others}>
      {local.children}
    </button>
  );
};
