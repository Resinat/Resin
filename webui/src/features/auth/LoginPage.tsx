import { zodResolver } from "@hookform/resolvers/zod";
import { Eye, EyeOff, Info, ShieldCheck } from "lucide-react";
import { useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { useLocation, useNavigate } from "react-router-dom";
import { z } from "zod";
import { Card } from "../../components/ui/Card";
import { Input } from "../../components/ui/Input";
import { Button } from "../../components/ui/Button";
import { LanguageSwitcher } from "../../components/LanguageSwitcher";
import { useAuthStore } from "./auth-store";
import { apiRequest, ApiError } from "../../lib/api-client";
import { useI18n } from "../../i18n";

const formSchema = z.object({
  token: z.string().trim().min(1, "请输入 Admin Token"),
});

type LoginFormInput = z.infer<typeof formSchema>;

export function LoginPage() {
  const { t } = useI18n();
  const navigate = useNavigate();
  const location = useLocation();
  const setToken = useAuthStore((state) => state.setToken);
  const storedToken = useAuthStore((state) => state.token);
  const [submitError, setSubmitError] = useState("");
  const [isPasswordVisible, setIsPasswordVisible] = useState(false);

  const {
    register,
    handleSubmit,
    formState: { errors, isSubmitting },
  } = useForm<LoginFormInput>({
    resolver: zodResolver(formSchema),
    defaultValues: { token: "" },
  });

  useEffect(() => {
    const params = new URLSearchParams(location.search);
    const next = params.get("next") || "/platforms";

    if (storedToken) {
      navigate(next, { replace: true });
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
        if (response.ok) {
          navigate(next, { replace: true });
        }
      } catch {
        // Keep login page for secured deployments or temporary network errors.
      }
    };
    void checkAuthMode();

    return () => {
      active = false;
      controller.abort();
    };
  }, [location.search, navigate, storedToken]);

  const onSubmit = handleSubmit(async (values) => {
    setSubmitError("");

    try {
      await apiRequest("/api/v1/system/info", {
        auth: true,
        token: values.token,
      });
    } catch (error) {
      if (error instanceof ApiError) {
        setSubmitError(t("登录失败：{{message}}", { message: error.message }));
      } else {
        setSubmitError(t("登录失败：无法连接 API。请确认 Resin 在 2260 端口运行，并使用 `npm run dev`（含 /api 代理）启动前端。"));
      }
      return;
    }

    setToken(values.token);

    const params = new URLSearchParams(location.search);
    const next = params.get("next") || "/platforms";
    navigate(next, { replace: true });
  });

  return (
    <main className="login-layout">
      <Card className="login-card">
        <div className="login-header">
          <div className="brand-logo" aria-hidden="true">
            <ShieldCheck size={18} />
          </div>
          <div className="login-heading-copy">
            <h1 className="login-title">{t("管理员登录")}</h1>
          </div>
          <LanguageSwitcher className="login-locale" />
        </div>

        <form className="login-form" onSubmit={onSubmit}>
          <label className="field-label field-label-with-info login-token-label" htmlFor="token">
            <span>{t("管理员令牌")}</span>
            <span
              className="subscription-info-icon"
              title={t("管理员令牌通过 `RESIN_ADMIN_TOKEN` 环境变量配置")}
              aria-label={t("管理员令牌通过 `RESIN_ADMIN_TOKEN` 环境变量配置")}
              tabIndex={0}
            >
              <Info size={14} />
            </span>
          </label>
          <div className="login-input-wrap">
            <Input
              id="token"
              className="login-token-input"
              autoComplete="off"
              type={isPasswordVisible ? "text" : "password"}
              invalid={Boolean(errors.token)}
              {...register("token")}
            />
            <Button
              variant="ghost"
              size="sm"
              className="password-visibility-toggle"
              aria-label={isPasswordVisible ? t("隐藏管理员令牌") : t("显示管理员令牌")}
              title={isPasswordVisible ? t("隐藏管理员令牌") : t("显示管理员令牌")}
              onClick={() => setIsPasswordVisible((visible) => !visible)}
            >
              {isPasswordVisible ? <EyeOff size={16} /> : <Eye size={16} />}
            </Button>
          </div>

          {errors.token?.message ? <p className="field-error">{t(errors.token.message)}</p> : null}
          {submitError ? <p className="field-error">{submitError}</p> : null}

          <Button type="submit" className="w-full login-submit" disabled={isSubmitting}>
            {isSubmitting ? t("校验中...") : t("进入控制台")}
          </Button>
        </form>
      </Card>
    </main>
  );
}
