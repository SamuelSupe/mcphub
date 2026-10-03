import { t } from "./i18n.js";
import { markClean, confirmDiscard, lockForm } from "./unsaved.js";
import { ldapTestCredentials, renderConnectionTest, confirmIdentityChange } from "./identity-connection-test.js";

let api;
let current;
let busy = false;
let feedback = "";
let baseline;
let tests = {};

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
  for (const source of ["oidc", "ldap"]) renderConnectionTest(document.querySelector(`#${source}-test-result`), tests[source]?.result);
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
  baseline = collect();
  tests = {};
  for (const source of ["oidc", "ldap"]) renderConnectionTest(document.querySelector(`#${source}-test-result`), null);
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

const sourceValue = (value, source) => JSON.stringify(value[source]);
const changedSources = (value) => ["oidc", "ldap"].filter((source) => sourceValue(value, source) !== sourceValue(baseline, source));
const verified = (value, source) => tests[source]?.result.status === "passed" && tests[source].draft === sourceValue(value, source) && Date.parse(tests[source].result.expires_at) > Date.now();

async function submit(source = "", fullTest = false) {
  if (busy || !current?.managed) return;
  const value = collect();
  if (source) value.source = source;
  if (fullTest && source === "ldap") {
    const credentials = await ldapTestCredentials();
    if (!credentials) return;
    Object.assign(value, credentials);
  }
  busy = true;
  let unlock = () => {};
  const changed = !source ? changedSources(value) : [];
  setFeedback(source ? "正在测试身份服务连接…" : "正在保存身份服务…");
  try {
    unlock = lockForm(form());
    if (!source) {
      if (changed.some((source) => value[source].enabled && !verified(value, source))) {
        setFeedback("请先验证发生变更的身份服务登录与映射，再保存。测试结果有效期为五分钟。");
        return;
      }
      if (changed.length) {
        const { identities = [] } = await api("/identities");
        const currentProviders = await api("/identity-providers");
        if (currentProviders.revision !== current.revision) throw new Error(t("配置已变化，请刷新后重试。"));
        const providers = identities.filter((p) => p.kind === "user" && p.provider !== "mcphub:local");
        const changes = changed.map((source) => ({ source, enabled: value[source].enabled, users: providers.filter((p) => p.provider === current.provider_ids[source]).length }));
        if (!await confirmIdentityChange(changes)) { setFeedback(""); return; }
      }
      if (changed.some((source) => value[source].enabled && !verified(value, source))) throw new Error(t("登录测试已过期，请重新测试。"));
    }
    const result = await api(`/identity-providers${source ? fullTest ? "/test" : "/probe" : ""}`, {
      method: source ? "POST" : "PUT",
      headers: { "If-Match": `"${current.revision}"` },
      body: JSON.stringify(value),
    });
    if (fullTest) {
      tests[source] = { draft: sourceValue(value, source), result };
      renderConnectionTest(document.querySelector(`#${source}-test-result`), result);
      setFeedback("测试使用草稿配置，不创建用户、不授予权限、不替换当前身份服务。");
      if (source === "oidc") pollTest(source, result.id);
    } else if (source) {
      setFeedback(source === "oidc" ? "OIDC 基础连接可用，仍需验证登录与身份映射。" : "LDAP 基础连接可用，仍需验证测试账号与组搜索。");
    } else {
      fill(result);
      setFeedback("身份服务已保存并立即生效。受影响的企业用户需重新登录。");
    }
  } catch (error) {
    setFeedback(error.message);
  } finally {
    delete value.password;
    busy = false;
    unlock();
  }
}

async function pollTest(source, id) {
  while (tests[source]?.result.id === id && tests[source].result.status !== "passed" && tests[source].result.status !== "failed") {
    await new Promise((resolve) => setTimeout(resolve, 2000));
    if (tests[source]?.result.id !== id) return;
    try {
      const result = await api(`/identity-providers/tests/${encodeURIComponent(tests[source].result.id)}`);
      if (tests[source]?.result.id !== id) return;
      tests[source].result = result;
      renderConnectionTest(document.querySelector(`#${source}-test-result`), result);
    } catch (error) {
      if (tests[source]?.result.id === id) setFeedback(error.message);
      return;
    }
  }
}

export function initIdentityProviders(apiCall) {
  api = apiCall;
  form().addEventListener("submit", (event) => { event.preventDefault(); submit(); });
  for (const button of form().querySelectorAll("[data-probe-identity]")) button.addEventListener("click", () => submit(button.dataset.probeIdentity));
  for (const button of form().querySelectorAll("[data-test-identity]")) button.addEventListener("click", () => submit(button.dataset.testIdentity, true));
  form().addEventListener("input", () => {
    const value = collect();
    for (const source of ["oidc", "ldap"]) {
      if (tests[source] && tests[source].draft !== sourceValue(value, source)) {
        delete tests[source];
        renderConnectionTest(document.querySelector(`#${source}-test-result`), null);
        setFeedback("草稿已变化，请重新验证登录与身份映射。");
      }
    }
  });
}
