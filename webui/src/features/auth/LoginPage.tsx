import { zodResolver } from "@hookform/resolvers/zod";
import { LockKeyhole, ShieldCheck } from "lucide-react";
import { useState } from "react";
import { useForm } from "react-hook-form";
import { useLocation, useNavigate } from "react-router-dom";
import { z } from "zod";
import { Card } from "../../components/ui/Card";
import { Input } from "../../components/ui/Input";
import { Button } from "../../components/ui/Button";
import { useAuthStore } from "./auth-store";
import { apiRequest, ApiError } from "../../lib/api-client";

const formSchema = z.object({
  token: z.string().trim().min(1, "请输入 Admin Token"),
});

type LoginFormInput = z.infer<typeof formSchema>;

export function LoginPage() {
  const navigate = useNavigate();
  const location = useLocation();
  const setToken = useAuthStore((state) => state.setToken);
  const [submitError, setSubmitError] = useState("");

  const {
    register,
    handleSubmit,
    formState: { errors, isSubmitting },
  } = useForm<LoginFormInput>({
    resolver: zodResolver(formSchema),
    defaultValues: { token: "" },
  });

  const onSubmit = handleSubmit(async (values) => {
    setSubmitError("");

    try {
      await apiRequest("/api/v1/system/info", {
        auth: true,
        token: values.token,
      });
    } catch (error) {
      if (error instanceof ApiError) {
        setSubmitError(`登录失败：${error.message}`);
      } else {
        setSubmitError(
          "登录失败：无法连接 API。请确认 Resin 在 2620 端口运行，并使用 `npm run dev`（含 /api 代理）启动前端。",
        );
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
          <div>
            <h1 className="login-title">管理员登录</h1>
          </div>
        </div>

        <p className="login-description">输入后端 `RESIN_ADMIN_TOKEN` 进入控制台。</p>

        <form className="login-form" onSubmit={onSubmit}>
          <label className="field-label" htmlFor="token">
            Admin Token
          </label>
          <div className="input-with-icon">
            <LockKeyhole size={16} />
            <Input
              id="token"
              placeholder="粘贴 Bearer Token（仅本地保存）"
              autoComplete="off"
              invalid={Boolean(errors.token)}
              {...register("token")}
            />
          </div>

          {errors.token?.message ? <p className="field-error">{errors.token.message}</p> : null}
          {submitError ? <p className="field-error">{submitError}</p> : null}

          <Button type="submit" className="w-full" disabled={isSubmitting}>
            {isSubmitting ? "校验中..." : "进入控制台"}
          </Button>
        </form>
      </Card>
    </main>
  );
}
