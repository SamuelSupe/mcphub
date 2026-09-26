import { t } from "./i18n.js";

const field = (id) => document.getElementById("credential-" + id);
let api;

export function initCredentials(request) {
  api = request;
  field("mode").addEventListener("change", updateCredentialFields);
  field("login").addEventListener("change", updateCredentialFields);
  field("check").addEventListener("click", async () => {
    field("check").disabled = true;
    field("status").textContent = t("正在验证 Vault 连接…");
    try {
      await api("/vault", { method: "POST" });
      field("status").textContent = t("Vault 连接正常");
    } catch {
      field("status").textContent = t("无法连接 Vault，请检查地址、证书和访问权限。");
    } finally { field("check").disabled = false; }
  });
}

export async function showCredentials(show) {
  document.getElementById("credentials-panel").hidden = !show;
  updateCredentialFields();
  if (!show) return;
  field("status").textContent = t("正在读取凭证服务状态…");
  try {
    const result = await api("/vault");
    field("status").textContent = result.configured
      ? t("已配置 Vault：{address}", { address: result.vault.address })
      : t("请先在服务配置中启用 Vault，再选择此认证方式。");
    field("check").hidden = !result.configured;
    field("callback").value = result.callback_url || "";
  } catch { field("status").textContent = t("无法读取凭证服务状态，请重试。"); }
}

export function fillCredentials(value) {
  field("mode").value = value?.mode || "shared";
  field("path").value = value?.path || "";
  field("field").value = value?.field || "token";
  field("header").value = value?.header || "Authorization";
  field("scheme").value = value?.scheme ?? "Bearer";
  field("discovery").value = value?.discovery_path || "";
  field("login").value = value && !value.oauth ? "token" : "oauth";
  field("issuer").value = value?.oauth?.issuer || "";
  field("client").value = value?.oauth?.client_id || "";
  field("secret").value = value?.oauth?.client_secret_path || "";
  field("scopes").value = (value?.oauth?.scopes || []).join(", ");
  updateCredentialFields();
}

function updateCredentialFields() {
  const visible = !document.getElementById("credentials-panel").hidden;
  const personal = field("mode").value === "personal";
  const oauth = personal && field("login").value === "oauth";
  field("shared").hidden = personal;
  field("personal").hidden = !personal;
  field("oauth").hidden = !oauth;
  field("mapping").hidden = oauth;
  for (const input of document.querySelectorAll("#credentials-panel input, #credentials-panel select")) {
    input.disabled = !visible || (input.closest("#credential-shared") && personal) || (input.closest("#credential-personal") && !personal) || (input.closest("#credential-oauth") && !oauth) || (input.closest("#credential-mapping") && oauth);
  }
  document.getElementById("stored-credential-note").hidden = visible;
  const grant = document.getElementById("field-client-grant");
  grant.disabled = visible && personal;
  if (grant.disabled) grant.checked = true;
}

export function collectCredentials() {
  const personal = field("mode").value === "personal";
  const oauth = personal && field("login").value === "oauth";
  const result = { mode: field("mode").value, field: field("field").value.trim() || "token", header: oauth ? "Authorization" : field("header").value.trim() || "Authorization", scheme: oauth ? "Bearer" : field("scheme").value.trim() };
  if (personal) {
    result.discovery_path = field("discovery").value.trim();
    if (oauth) result.oauth = {
      issuer: field("issuer").value.trim(), client_id: field("client").value.trim(),
      client_secret_path: field("secret").value.trim(),
      scopes: field("scopes").value.split(/[\s,]+/).filter(Boolean),
    };
  } else result.path = field("path").value.trim();
  if (!personal && !result.path) throw new Error(t("请填写 Vault 凭证路径。"));
  if (oauth && (!result.oauth.issuer || !result.oauth.client_id)) throw new Error(t("请填写上游 Issuer 和 Client ID。"));
  return result;
}
