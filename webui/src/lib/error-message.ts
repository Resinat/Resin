import { ApiError } from "./api-client";

type TranslateFn = (text: string, options?: Record<string, unknown>) => string;

export function formatApiErrorMessage(error: unknown, t: TranslateFn): string {
  if (error instanceof ApiError) {
    const message = error.message ? t(error.message) : t("未知错误");
    return error.code ? `${error.code}: ${message}` : message;
  }
  if (error instanceof Error) {
    return error.message ? t(error.message) : t("未知错误");
  }
  return t("未知错误");
}
