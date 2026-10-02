import { t } from "./i18n.js";
import { markClean, confirmDiscard, lockForm } from "./unsaved.js";

let api;
let current;
let busy = false;
let feedback = "";

const oidcFields = ["issuer", "client_id", "token_auth_method", "name_claim", "groups_claim", "departments_claim", "tenant_claim", "tenant_value"];
const ldapFields = ["url", "bind_dn", "user_base_dn", "user_filter", "user_id_attribute", "name_attribute", "group_base_dn", "group_filter", "group_id_attribute", "group_name_attribute", "root_ca_pem"];
const form = () => document.querySelector("#identity-provider-form");

function setFeedback(message) {
  feedback = message;
  document.querySelector("#identity-provider-feedback").textContent = t(message);
}

export function renderIdentityProviders() {
  document.querySelector("#identity-provider-feedback").textContent = t(feedback);
  if (!current) return;
  form().elements.oidc_client_secret.placeholder = t(current.oidc.client_secret_configured ? "已配置，留空保留" : "填写 Client Secret");
  form().elements.ldap_bind_password.placeholder = t(current.ldap.bind_password_configured ? "已配置，留空保留" : "填写服务账号密码");
}

function fill(value) {
  current = value;
  const fields = form().elements;
  fields.oidc_enabled.checked = value.oidc.enabled;
  fields.ldap_enabled.checked = value.ldap.enabled;
  for (const name of oidcFields) fields[`oidc_${name}`].value = value.oidc.provider[name] || (name === "token_auth_method" ? "client_secret_post" : "");
  fields.oidc_scopes.value = value.oidc.provider.scopes?.join(" ") || "openid profile email";
  fields.oidc_name_claim.value ||= "name";
  for (const name of ldapFields) fields[`ldap_${name}`].value = value.ldap[name] || "";
  fields.ldap_user_filter.value ||= "(&(objectClass=person)(uid={{username}}))";
  fields.ldap_user_id_attribute.value ||= "entryUUID";
  fields.ldap_name_attribute.value ||= "cn";
  fields.ldap_group_name_attribute.value ||= "cn";
  fields.oidc_client_secret.value = "";
  fields.ldap_bind_password.value = "";
  document.querySelector("#identity-provider-callback").value = value.callback_url;
  for (const element of form().elements) element.disabled = !value.managed;
  if (!value.managed) setFeedback("当前使用独立外部认证模式。请通过 YAML 管理身份源；UI 配置需要内建认证与本地恢复管理员。");
  renderIdentityProviders();
  markClean(form());
}

function collect() {
  const data = new FormData(form());
  const value = { oidc: { enabled: data.has("oidc_enabled"), provider: { ...current.oidc.provider, protocol: "oidc" } }, ldap: { enabled: data.has("ldap_enabled") } };
  for (const name of oidcFields) value.oidc.provider[name] = String(data.get(`oidc_${name}`) || "").trim();
  value.oidc.provider.scopes = String(data.get("oidc_scopes") || "").trim().split(/\s+/).filter(Boolean);
  for (const name of ldapFields) value.ldap[name] = String(data.get(`ldap_${name}`) || "").trim();
  const oidcSecret = String(data.get("oidc_client_secret") || "");
  const ldapSecret = String(data.get("ldap_bind_password") || "");
  if (oidcSecret) value.oidc.client_secret = oidcSecret;
  if (ldapSecret) value.ldap.bind_password = ldapSecret;
  return value;
}

export async function refreshIdentityProviders() {
  if (!api || busy) return;
  if (!await confirmDiscard(form())) return;
  try {
    fill(await api("/identity-providers"));
  } catch (error) {
    setFeedback(error.message);
  }
}

async function submit(source = "") {
  if (busy || !current?.managed) return;
  const value = collect();
  if (source) value.source = source;
  busy = true;
  const unlock = lockForm(form());
  setFeedback(source ? "正在测试身份服务连接…" : "正在保存身份服务…");
  try {
    const result = await api(`/identity-providers${source ? "/probe" : ""}`, {
      method: source ? "POST" : "PUT",
      headers: { "If-Match": `"${current.revision}"` },
      body: JSON.stringify(value),
    });
    if (source) {
      setFeedback(source === "oidc" ? "OIDC 元数据与签名密钥可用。请保存后实际登录，验证客户端密钥和身份映射。" : "LDAP TLS、服务账号和搜索 Base DN 验证通过。请保存后实际登录，验证用户与组搜索。");
    } else {
      fill(result);
      setFeedback("身份服务已保存并立即生效。受影响的企业用户需重新登录。");
    }
  } catch (error) {
    setFeedback(error.message);
  } finally {
    busy = false;
    unlock();
  }
}

export function initIdentityProviders(apiCall) {
  api = apiCall;
  form().addEventListener("submit", (event) => { event.preventDefault(); submit(); });
  for (const button of form().querySelectorAll("[data-probe-identity]")) button.addEventListener("click", () => submit(button.dataset.probeIdentity));
}
