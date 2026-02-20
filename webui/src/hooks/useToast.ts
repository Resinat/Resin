import { useCallback, useRef, useState } from "react";

export interface ToastItem {
    id: number;
    tone: "success" | "error";
    text: string;
    /** true once the exit animation has started */
    exiting: boolean;
}

const SUCCESS_TTL = 3_000;
const ERROR_TTL = 5_000;
const EXIT_ANIMATION_MS = 340;

export function useToast() {
    const [toasts, setToasts] = useState<ToastItem[]>([]);
    const nextId = useRef(0);

    const dismissToast = useCallback((id: number) => {
        // mark as exiting so the component can play the fade-out animation
        setToasts((prev) => prev.map((t) => (t.id === id ? { ...t, exiting: true } : t)));
        // remove from DOM after animation
        setTimeout(() => {
            setToasts((prev) => prev.filter((t) => t.id !== id));
        }, EXIT_ANIMATION_MS);
    }, []);

    const showToast = useCallback(
        (tone: "success" | "error", text: string) => {
            const id = nextId.current++;
            setToasts((prev) => [...prev, { id, tone, text, exiting: false }]);

            const ttl = tone === "success" ? SUCCESS_TTL : ERROR_TTL;
            setTimeout(() => dismissToast(id), ttl);
        },
        [dismissToast],
    );

    return { toasts, showToast, dismissToast } as const;
}
