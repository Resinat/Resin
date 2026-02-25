import { useQuery } from "@tanstack/react-query";
import {
  AlertTriangle,
  Database,
  LayoutDashboard,
  LogOut,
  Logs,
  Network,
  Regex,
  Rss,
  Server,
  Settings,
} from "lucide-react";
import { NavLink, Outlet, useNavigate } from "react-router-dom";
import { motion } from "framer-motion";
import { Button } from "./ui/Button";
import { cn } from "../lib/cn";
import { useAuthStore } from "../features/auth/auth-store";
import { getEnvConfig } from "../features/systemConfig/api";
import { useI18n } from "../i18n";
import { LanguageSwitcher } from "./LanguageSwitcher";

type NavItem = {
  label: string;
  path: string;
  icon: typeof LayoutDashboard;
};

const navItems: NavItem[] = [
  { label: "总览看板", path: "/dashboard", icon: LayoutDashboard },
  { label: "平台管理", path: "/platforms", icon: Server },
  { label: "订阅管理", path: "/subscriptions", icon: Rss },
  { label: "节点池", path: "/nodes", icon: Network },
  { label: "请求头规则", path: "/rules", icon: Regex },
  { label: "请求日志", path: "/request-logs", icon: Logs },
  { label: "资源", path: "/resources", icon: Database },
  { label: "系统配置", path: "/system-config", icon: Settings },
];

export function AppShell() {
  const { t } = useI18n();
  const clearToken = useAuthStore((state) => state.clearToken);
  const token = useAuthStore((state) => state.token);
  const navigate = useNavigate();
  const envConfigQuery = useQuery({
    queryKey: ["system-config-env", "shell"],
    queryFn: getEnvConfig,
    staleTime: 30_000,
  });
  const logoSrc = `${import.meta.env.BASE_URL}vite.svg`;

  const envConfig = envConfigQuery.data;
  const authWarnings: string[] = [];
  if (envConfig && !envConfig.admin_token_set) {
    authWarnings.push(t("RESIN_ADMIN_TOKEN 为空，控制面 API 免认证"));
  }
  if (envConfig && !envConfig.proxy_token_set) {
    authWarnings.push(t("RESIN_PROXY_TOKEN 为空，正/反向代理免认证"));
  }
  if (envConfig && envConfig.admin_token_set && envConfig.admin_token_weak) {
    authWarnings.push(t("RESIN_ADMIN_TOKEN 强度较弱，建议更换为更高熵随机令牌"));
  }
  if (envConfig && envConfig.proxy_token_set && envConfig.proxy_token_weak) {
    authWarnings.push(t("RESIN_PROXY_TOKEN 强度较弱，建议更换为更高熵随机令牌"));
  }
  const showAuthWarning = authWarnings.length > 0;

  const logout = () => {
    clearToken();
    navigate("/login", { replace: true });
  };

  return (
    <div className="app-layout">
      <aside className="sidebar">
        <div className="brand">
          <div className="brand-logo" aria-hidden="true">
            <img src={logoSrc} alt="Resin Logo" style={{ width: 20, height: 20 }} />
          </div>
          <div className="brand-copy">
            <p className="brand-title">Resin</p>
            <p className="brand-subtitle">{t("高性能粘性代理池 · 管理面板")}</p>
          </div>
        </div>

        <div className="sidebar-main">
          <nav className="nav-list" aria-label={t("主导航")}>
            {navItems.map((item) => {
              const Icon = item.icon;
              return (
                <NavLink
                  key={item.path}
                  to={item.path}
                  className={({ isActive }) => cn("nav-item", isActive && "nav-item-active")}
                >
                  <Icon size={16} />
                  <span>{t(item.label)}</span>
                </NavLink>
              );
            })}
          </nav>
        </div>

        <div className="sidebar-bottom">
          {showAuthWarning ? (
            <div className="callout callout-warning sidebar-warning" role="alert">
              <AlertTriangle size={16} />
              <div className="sidebar-warning-copy">
                <strong>{t("安全警告")}</strong>
                <div className="sidebar-warning-list">
                  {authWarnings.map((warning) => (
                    <span key={warning}>{warning}</span>
                  ))}
                </div>
              </div>
            </div>
          ) : null}

          {!token ? <p className="sidebar-hint">{t("当前为免认证访问模式")}</p> : null}

          <div className="sidebar-tools">
            {token ? (
              <Button
                variant="secondary"
                size="sm"
                className="sidebar-icon-btn"
                onClick={logout}
                aria-label={t("退出登录")}
                title={t("退出登录")}
              >
                <LogOut size={16} />
              </Button>
            ) : (
              <span className="sidebar-tool-spacer" aria-hidden="true" />
            )}
            <LanguageSwitcher className="sidebar-locale" compact />
          </div>
        </div>
      </aside>

      <main className="main">
        <motion.div
          key="content"
          initial={{ opacity: 0, y: 8 }}
          animate={{ opacity: 1, y: 0 }}
          transition={{ duration: 0.24, ease: "easeOut" }}
          className="content"
        >
          <Outlet />
        </motion.div>
      </main>
    </div>
  );
}
