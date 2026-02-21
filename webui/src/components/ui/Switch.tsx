import type { ComponentProps } from "react";
import "./Switch.css";

type SwitchProps = Omit<ComponentProps<"input">, "type" | "className">;

export function Switch(props: SwitchProps) {
    return (
        <label className="switch">
            <input type="checkbox" className="switch-input" {...props} />
            <span className="switch-slider"></span>
        </label>
    );
}
