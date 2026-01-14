import { splitProps } from "solid-js";
import type { Component, JSX } from "solid-js";

interface ButtonProps extends JSX.ButtonHTMLAttributes<HTMLButtonElement> {
    variant?: "default" | "primary" | "active";
    active?: boolean;
}

export const Button: Component<ButtonProps> = (props) => {
    const [local, others] = splitProps(props, ["variant", "active", "class", "children"]);

    const baseClass = "px-3 py-1.5 rounded-md text-[12px] font-medium border transition-colors flex items-center gap-1.5 cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed";
    
    const variantClasses = () => {
        if (local.variant === "primary") {
            return "bg-accent text-white border-accent hover:opacity-90";
        }
        if (local.variant === "active" || local.active) {
            return "bg-bg-panel border-accent text-accent";
        }
        return "bg-bg-dark border-border text-text-main hover:bg-bg-hover hover:border-text-muted";
    };

    return (
        <button 
            class={`${baseClass} ${variantClasses()} ${local.class || ""}`}
            {...others}
        >
            {local.children}
        </button>
    );
};
