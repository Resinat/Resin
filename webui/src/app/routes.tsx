import { Navigate, Route, Routes } from "react-router-dom";
import { AppShell } from "../components/AppShell";
import { LoginPage } from "../features/auth/LoginPage";
import { DashboardPage } from "../features/dashboard/DashboardPage";
import { GeoIPPage } from "../features/geoip/GeoIPPage";
import { NodesPage } from "../features/nodes/NodesPage";
import { RequireAuth } from "../features/auth/RequireAuth";
import { PlatformPage } from "../features/platforms/PlatformPage";
import { RequestLogsPage } from "../features/requestLogs/RequestLogsPage";
import { RulesPage } from "../features/rules/RulesPage";
import { SubscriptionPage } from "../features/subscriptions/SubscriptionPage";
import { SystemConfigPage } from "../features/systemConfig/SystemConfigPage";

export function AppRoutes() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />

      <Route
        element={
          <RequireAuth>
            <AppShell />
          </RequireAuth>
        }
      >
        <Route path="/" element={<Navigate to="/dashboard" replace />} />
        <Route path="/dashboard" element={<DashboardPage />} />
        <Route path="/platforms" element={<PlatformPage />} />
        <Route path="/subscriptions" element={<SubscriptionPage />} />
        <Route path="/nodes" element={<NodesPage />} />
        <Route path="/rules" element={<RulesPage />} />
        <Route path="/request-logs" element={<RequestLogsPage />} />
        <Route path="/resources" element={<GeoIPPage />} />
        <Route path="/system-config" element={<SystemConfigPage />} />
      </Route>

      <Route path="*" element={<Navigate to="/dashboard" replace />} />
    </Routes>
  );
}
