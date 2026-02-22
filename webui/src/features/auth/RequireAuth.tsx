import { useEffect, useState, type ReactElement } from "react";
import { Navigate, useLocation } from "react-router-dom";
import { useAuthStore } from "./auth-store";

type RequireAuthProps = {
  children: ReactElement;
};

export function RequireAuth({ children }: RequireAuthProps) {
  const token = useAuthStore((state) => state.token);
  const location = useLocation();
  const [checked, setChecked] = useState(Boolean(token));
  const [anonymousAllowed, setAnonymousAllowed] = useState(false);

  useEffect(() => {
    if (token) {
      setAnonymousAllowed(false);
      setChecked(true);
      return;
    }

    let active = true;
    const controller = new AbortController();

    const checkAuthMode = async () => {
      try {
        const response = await fetch("/api/v1/system/info", {
          method: "GET",
          signal: controller.signal,
        });
        if (!active) {
          return;
        }
        // /api/v1/system/info returns 200 only when admin auth is disabled.
        setAnonymousAllowed(response.ok);
      } catch {
        if (!active) {
          return;
        }
        setAnonymousAllowed(false);
      } finally {
        if (active) {
          setChecked(true);
        }
      }
    };

    void checkAuthMode();

    return () => {
      active = false;
      controller.abort();
    };
  }, [token]);

  if (token || anonymousAllowed) {
    return children;
  }

  if (!checked) {
    return null;
  }

  const next = `${location.pathname}${location.search}`;
  return <Navigate to={`/login?next=${encodeURIComponent(next)}`} replace />;
}
