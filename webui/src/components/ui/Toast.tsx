import { createPortal } from "react-dom";
import { X } from "lucide-react";
import type { ToastItem } from "../../hooks/useToast";
import { useI18n } from "../../i18n";

interface ToastContainerProps {
    toasts: ToastItem[];
    onDismiss: (id: number) => void;
}

export function ToastContainer({ toasts, onDismiss }: ToastContainerProps) {
    const { t } = useI18n();

    if (toasts.length === 0) {
        return null;
    }

    return createPortal(
        <div className="toast-container" aria-live="polite">
            {toasts.map((toast) => (
                <div
                    key={toast.id}
                    className={`toast-item toast-${toast.tone}${toast.exiting ? " toast-exit" : ""}`}
                >
                    <span className="toast-text">{t(toast.text)}</span>
                    <button
                        type="button"
                        className="toast-close"
                        aria-label={t("关闭")}
                        onClick={() => onDismiss(toast.id)}
                    >
                        <X size={14} />
                    </button>
                </div>
            ))}
        </div>,
        document.body,
    );
}
