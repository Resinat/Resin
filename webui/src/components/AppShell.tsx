import {
  Boxes,
  Globe,
  LayoutDashboard,
  Logs,
  Network,
  Route,
  Settings,
  ShieldCheck,
  Split,
} from "lucide-react";
import { NavLink, Outlet, useNavigate } from "react-router-dom";
import { motion } from "framer-motion";
import { Button } from "./ui/Button";
import { cn } from "../lib/cn";
import { useAuthStore } from "../features/auth/auth-store";

type NavItem = {
  label: string;
  path: string;
  icon: typeof LayoutDashboard;
};

const navItems: NavItem[] = [
  { label: "Dashboard", path: "/dashboard", icon: LayoutDashboard },
  { label: "Platform 管理", path: "/platforms", icon: Route },
  { label: "订阅管理", path: "/subscriptions", icon: Split },
  { label: "节点池", path: "/nodes", icon: Network },
  { label: "Header 规则", path: "/rules", icon: ShieldCheck },
  { label: "请求日志", path: "/request-logs", icon: Logs },
  { label: "GeoIP", path: "/geoip", icon: Globe },
  { label: "系统配置", path: "/system-config", icon: Settings },
];

export function AppShell() {
  const clearToken = useAuthStore((state) => state.clearToken);
  const navigate = useNavigate();

  const logout = () => {
    clearToken();
    navigate("/login", { replace: true });
  };

  return (
    <div className="app-layout">
      <aside className="sidebar">
        <div className="brand">
          <div className="brand-logo" aria-hidden="true">
            <Boxes size={18} />
          </div>
          <div>
            <p className="brand-title">Resin Control</p>
            <p className="brand-subtitle">Modern Ops Console</p>
          </div>
        </div>

        <nav className="nav-list" aria-label="主导航">
          {navItems.map((item) => {
            const Icon = item.icon;
            return (
              <NavLink
                key={item.path}
                to={item.path}
                className={({ isActive }) => cn("nav-item", isActive && "nav-item-active")}
              >
                <Icon size={16} />
                <span>{item.label}</span>
              </NavLink>
            );
          })}
        </nav>

        <div className="sidebar-footer">
          <Button variant="secondary" className="w-full" onClick={logout}>
            退出登录
          </Button>
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
