import { useQuery } from "@tanstack/react-query";
import { Check, Copy, Info } from "lucide-react";
import { useMemo, useState } from "react";
import { Button } from "../../components/ui/Button";
import { Input } from "../../components/ui/Input";
import { useI18n } from "../../i18n";
import { getEnvConfig } from "../systemConfig/api";

const PROXY_TOKEN_STORAGE_KEY = "resin_proxy_token";
const TOKEN_PLACEHOLDER = "<token>";

type ProxyEndpoint = { scheme: string; host: string };

function loadStoredProxyToken(): string {
  if (typeof window === "undefined") {
    return "";
  }
  return window.localStorage.getItem(PROXY_TOKEN_STORAGE_KEY) ?? "";
}

function persistProxyToken(value: string): void {
  if (typeof window === "undefined") {
    return;
  }
  if (value) {
    window.localStorage.setItem(PROXY_TOKEN_STORAGE_KEY, value);
  } else {
    window.localStorage.removeItem(PROXY_TOKEN_STORAGE_KEY);
  }
}

function formatHostWithPort(hostname: string, port: number): string {
  const host = hostname.includes(":") && !hostname.startsWith("[") ? `[${hostname}]` : hostname;
  return port ? `${host}:${port}` : host;
}

function configuredApiEndpoint(): ProxyEndpoint | null {
  const apiBase = import.meta.env.VITE_API_BASE_URL?.trim();
  if (!apiBase || !/^https?:\/\//i.test(apiBase)) {
    return null;
  }
  try {
    const url = new URL(apiBase);
    return { scheme: url.protocol.replace(/:$/, ""), host: url.host };
  } catch {
    return null;
  }
}

function currentProxyEndpoint(fallbackPort: number): ProxyEndpoint {
  const configured = configuredApiEndpoint();
  if (configured) {
    return configured;
  }

  if (typeof window === "undefined") {
    return { scheme: "http", host: fallbackPort ? `127.0.0.1:${fallbackPort}` : "127.0.0.1:2260" };
  }

  const scheme = currentScheme();
  const expectedPort = fallbackPort || 2260;
  const currentPort = Number(window.location.port);
  if (import.meta.env.DEV && window.location.hostname && currentPort && currentPort !== expectedPort) {
    return { scheme, host: formatHostWithPort(window.location.hostname, expectedPort) };
  }
  if (window.location.host) {
    return { scheme, host: window.location.host };
  }
  return {
    scheme,
    host: window.location.hostname
      ? formatHostWithPort(window.location.hostname, expectedPort)
      : `127.0.0.1:${expectedPort}`,
  };
}

function currentScheme(): string {
  if (typeof window === "undefined" || !window.location.protocol) {
    return "http";
  }
  return window.location.protocol.replace(/:$/, "");
}

// Encode a URL segment/userinfo component, but keep the literal <token>
// placeholder readable so users can see where to paste the real token.
function encodeSegment(value: string): string {
  return value === TOKEN_PLACEHOLDER ? value : encodeURIComponent(value);
}

function shellQuote(value: string): string {
  return `'${value.replace(/'/g, `'\\''`)}'`;
}

type ParsedTarget = { protocol: string; rest: string };

function parseTarget(raw: string): ParsedTarget | null {
  const trimmed = raw.trim();
  if (!trimmed) {
    return null;
  }
  const withScheme = /^[a-zA-Z][\w+.-]*:\/\//.test(trimmed) ? trimmed : `https://${trimmed}`;
  let url: URL;
  try {
    url = new URL(withScheme);
  } catch {
    return null;
  }
  const protocol = url.protocol.replace(/:$/, "").toLowerCase();
  if (protocol !== "http" && protocol !== "https") {
    return null;
  }
  const path = url.pathname === "/" ? "" : url.pathname;
  return { protocol, rest: `${url.host}${path}${url.search}` };
}

type CopyFieldProps = {
  label: string;
  value: string;
  hint?: string;
  copyLabel: string;
  copiedLabel: string;
};

function CopyField({ label, value, hint, copyLabel, copiedLabel }: CopyFieldProps) {
  const [copied, setCopied] = useState(false);

  const handleCopy = async () => {
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(value);
      } else {
        const area = document.createElement("textarea");
        area.value = value;
        area.style.position = "fixed";
        area.style.opacity = "0";
        document.body.appendChild(area);
        area.select();
        document.execCommand("copy");
        document.body.removeChild(area);
      }
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      setCopied(false);
    }
  };

  return (
    <div className="platform-access-field">
      <div className="platform-access-field-head">
        <span className="platform-access-field-label">{label}</span>
        {hint ? <span className="platform-access-field-hint">{hint}</span> : null}
      </div>
      <div className="platform-access-field-body">
        <code className="platform-access-value" title={value}>
          {value}
        </code>
        <Button
          variant="secondary"
          size="sm"
          onClick={() => void handleCopy()}
          className="platform-access-copy-btn"
        >
          {copied ? <Check size={14} /> : <Copy size={14} />}
          {copied ? copiedLabel : copyLabel}
        </Button>
      </div>
    </div>
  );
}

type PlatformAccessPanelProps = {
  platformName: string;
};

export function PlatformAccessPanel({ platformName }: PlatformAccessPanelProps) {
  const { t } = useI18n();
  const [account, setAccount] = useState("");
  const [token, setToken] = useState(loadStoredProxyToken);
  const [target, setTarget] = useState("https://api.ipify.org");

  const envQuery = useQuery({
    queryKey: ["system-env-config"],
    queryFn: getEnvConfig,
    staleTime: 60_000,
  });

  const env = envQuery.data;
  const proxyTokenSet = env?.proxy_token_set ?? true;
  const isLegacy = env?.auth_version === "LEGACY_V0";
  const endpoint = currentProxyEndpoint(env?.resin_port ?? 2260);
  const host = endpoint.host;
  const scheme = endpoint.scheme;

  const handleTokenChange = (value: string) => {
    setToken(value);
    persistProxyToken(value.trim());
  };

  const urls = useMemo(() => {
    const platform = platformName.trim() || "Default";
    const acc = account.trim();
    const tokenRaw = proxyTokenSet ? token.trim() : "";
    const sep = isLegacy ? ":" : ".";

    // Raw identity/credential are used verbatim for the curl -U value.
    const identityRaw = acc ? `${platform}${sep}${acc}` : platform;
    // Encoded identity is reused for URL userinfo and reverse path segment.
    const identityEnc = acc
      ? `${encodeSegment(platform)}${sep}${encodeSegment(acc)}`
      : encodeSegment(platform);
    const reverseIdentityEnc = isLegacy
      ? `${encodeSegment(platform)}:${acc ? encodeSegment(acc) : ""}`
      : identityEnc;

    // forward token: literal placeholder only when auth is enabled but unset.
    const forwardToken = tokenRaw || (proxyTokenSet ? TOKEN_PLACEHOLDER : "");

    let forwardCredential: string;
    let userInfo: string;
    if (isLegacy) {
      forwardCredential = forwardToken ? `${forwardToken}:${identityRaw}` : identityRaw;
      userInfo = forwardToken ? `${encodeSegment(forwardToken)}:${identityEnc}` : identityEnc;
    } else {
      forwardCredential = forwardToken ? `${identityRaw}:${forwardToken}` : identityRaw;
      userInfo = forwardToken ? `${identityEnc}:${encodeSegment(forwardToken)}` : identityEnc;
    }

    const httpForward = `http://${userInfo}@${host}`;
    const socksForward = `socks5h://${userInfo}@${host}`;

    const reverseTokenSeg = proxyTokenSet ? encodeSegment(tokenRaw || TOKEN_PLACEHOLDER) : "";
    const parsed = parseTarget(target);
    const reverseUrl = parsed
      ? `${scheme}://${host}/${reverseTokenSeg}/${reverseIdentityEnc}/${parsed.protocol}/${parsed.rest}`
      : "";

    const curlForward = [
      "curl",
      "-x",
      shellQuote(`http://${host}`),
      "-U",
      shellQuote(forwardCredential),
      shellQuote("https://api.ipify.org"),
    ].join(" ");
    const curlReverse = reverseUrl ? `curl ${shellQuote(reverseUrl)}` : "";

    return { httpForward, socksForward, reverseUrl, curlForward, curlReverse };
  }, [platformName, account, token, isLegacy, proxyTokenSet, host, scheme, target]);

  const copyLabel = t("复制");
  const copiedLabel = t("已复制");
  const tokenMissing = proxyTokenSet && !token.trim();
  const tokenInputValue = proxyTokenSet ? token : "";

  return (
    <section className="platform-detail-tabpanel platform-access-section">
      <div className="platform-drawer-section-head">
        <h4>{t("接入方式")}</h4>
        <p>{t("填写账号与代理 token，一键复制正向/反向代理地址。")}</p>
      </div>

      <div className="platform-access-inputs">
        <div className="field-group">
          <label className="field-label" htmlFor="access-account">
            {t("业务账号（可选）")}
          </label>
          <Input
            id="access-account"
            placeholder={t("例如 user_tom，留空则只按平台路由")}
            value={account}
            onChange={(event) => setAccount(event.target.value)}
          />
        </div>

        <div className="field-group">
          <label className="field-label field-label-with-info" htmlFor="access-token">
            <span>{t("代理 token")}</span>
            <span
              className="subscription-info-icon"
              title={t("即后端 RESIN_PROXY_TOKEN。仅保存在浏览器本地，不会上传服务器。")}
              aria-label={t("即后端 RESIN_PROXY_TOKEN。仅保存在浏览器本地，不会上传服务器。")}
              tabIndex={0}
            >
              <Info size={13} />
            </span>
          </label>
          <Input
            id="access-token"
            type="password"
            placeholder={proxyTokenSet ? t("填写 RESIN_PROXY_TOKEN") : t("当前代理免认证，无需填写")}
            value={tokenInputValue}
            onChange={(event) => {
              if (proxyTokenSet) {
                handleTokenChange(event.target.value);
              }
            }}
            disabled={!proxyTokenSet}
            autoComplete="off"
          />
          {tokenMissing ? (
            <p className="muted" style={{ marginTop: 4, fontSize: 12 }}>
              {t("尚未填写 token，地址中将以 <token> 占位，请替换为实际值。")}
            </p>
          ) : null}
        </div>
      </div>

      <div className="platform-access-group">
        <h5>{t("正向代理")}</h5>
        <CopyField
          label={t("HTTP 正向代理")}
          value={urls.httpForward}
          copyLabel={copyLabel}
          copiedLabel={copiedLabel}
        />
        {isLegacy ? (
          <p className="muted" style={{ fontSize: 12 }}>
            {t("当前为 LEGACY_V0 鉴权，SOCKS5 正向代理未启用。")}
          </p>
        ) : (
          <CopyField
            label={t("SOCKS5 正向代理")}
            value={urls.socksForward}
            copyLabel={copyLabel}
            copiedLabel={copiedLabel}
          />
        )}
        <CopyField
          label={t("curl 示例")}
          value={urls.curlForward}
          copyLabel={copyLabel}
          copiedLabel={copiedLabel}
        />
      </div>

      <div className="platform-access-group">
        <h5>{t("反向代理")}</h5>
        <div className="field-group">
          <label className="field-label" htmlFor="access-target">
            {t("目标网址")}
          </label>
          <Input
            id="access-target"
            placeholder={t("例如 https://api.ipify.org")}
            value={target}
            onChange={(event) => setTarget(event.target.value)}
          />
        </div>
        {urls.reverseUrl ? (
          <>
            <CopyField
              label={t("反向代理地址")}
              value={urls.reverseUrl}
              copyLabel={copyLabel}
              copiedLabel={copiedLabel}
            />
            <CopyField
              label={t("curl 示例")}
              value={urls.curlReverse}
              copyLabel={copyLabel}
              copiedLabel={copiedLabel}
            />
          </>
        ) : (
          <p className="muted" style={{ fontSize: 12 }}>
            {t("请输入合法的 http/https 目标网址以生成反向代理地址。")}
          </p>
        )}
      </div>
    </section>
  );
}
