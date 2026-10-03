import { t, getLocale, translateDOM } from "./i18n.js";
import { element, showToolPolicies } from "./tool-policies.js";

let actions, services = [], selected = "", tab = "connection", generation = 0;
const byId = (id) => document.getElementById(id);
const stateLabel = (state) => t({ ready: "已连接", unavailable: "连接异常", disabled: "已停用", connecting: "正在连接" }[state] || state);

export function initServices(options) {
  actions = options;
  byId("service-search").oninput = renderServices;
  byId("service-kind").onchange = renderServices;
  byId("add-service").onclick = () => actions.edit(byId("new-service-kind").value, null);
  for (const button of document.querySelectorAll("[data-service-tab]")) button.onclick = () => { tab = button.dataset.serviceTab; renderServiceDetails(); };
}

export function updateServices(backends, groups) {
  services = [...backends.map((config) => ({ kind: "mcp", config })), ...groups.map((config) => ({ kind: "http", config }))];
  renderServices();
}

export function renderServices() {
  const query = byId("service-search").value.trim().toLowerCase(), kind = byId("service-kind").value;
  const list = byId("service-list"); list.replaceChildren();
  const visible = services.filter((s) => (!kind || s.kind === kind) && `${s.config.id} ${s.config.url || s.config.base_url}`.toLowerCase().includes(query));
	if (!visible.some((s) => s.config.id === selected)) selected = visible[0]?.config.id || "";
  byId("service-count").textContent = t("{count} 个服务", { count: visible.length });
  for (const service of visible) {
    const item = service.config, row = element("button", "", "service-row secondary"); row.type = "button";
    row.setAttribute("aria-pressed", String(selected === item.id));
    row.append(element("strong", item.id), element("small", `${service.kind.toUpperCase()} · ${stateLabel(item.runtime.state)}`), element("small", item.url || item.base_url));
    row.onclick = () => { selected = item.id; renderServices(); };
    list.append(row);
  }
  if (!visible.length) {
    list.append(element("h3", t(services.length ? "没有匹配的服务" : "接入第一个服务")), element("p", t(services.length ? "试试其他关键词，或清除筛选条件。" : "选择 MCP 或 HTTP 接入方式，添加服务并测试连接。"), "field-note"));
    if (services.length) {
      const clear = element("button", t("清除筛选"), "secondary"); clear.type = "button";
      clear.onclick = () => { byId("service-search").value = ""; byId("service-kind").value = ""; renderServices(); byId("service-search").focus(); };
      list.append(clear);
    }
  }
  if (!services.some((s) => s.config.id === selected)) selected = visible[0]?.config.id || "";
  renderServiceDetails();
}

async function renderServiceDetails() {
  const current = ++generation, service = services.find((s) => s.config.id === selected);
  const body = byId("service-details"); body.replaceChildren(); byId("service-detail-panel").hidden = !service;
  if (!service) return;
  const item = service.config;
  byId("service-detail-title").textContent = item.id;
  for (const button of document.querySelectorAll("[data-service-tab]")) button.setAttribute("aria-pressed", String(button.dataset.serviceTab === tab));
  body.append(element("p", `${service.kind.toUpperCase()} · ${stateLabel(item.runtime.state)}`, "field-note"));
  const action = (label, callback) => { const button = element("button", t(label), "secondary"); button.type = "button"; button.onclick = callback; body.append(button); };
  if (tab === "connection") {
    body.append(element("p", item.url || item.base_url));
    body.append(element("p", t(item.credentials?.mode === "personal" ? "个人账号 · Vault" : item.credentials ? "共享凭证 · Vault" : item.oauth ? "OAuth client_credentials" : item.headers?.length ? "静态 Headers" : "无需后端认证")));
    if (service.kind === "http") body.append(element("p", t("HTTP 服务使用共享 Header 或 OAuth 凭证。"), "field-note"));
    action("编辑连接与凭证", () => actions.edit(service.kind, item));
  } else if (tab === "capabilities") {
    body.append(element("p", t("{count} 个工具", { count: item.runtime.tools })));
    if (service.kind === "mcp") body.append(element("p", `${t("提示词")} ${item.runtime.prompts} · ${t("资源")} ${item.runtime.resources}`));
    action("配置工具发布与审批", () => showToolPolicies(item.id));
    if (service.kind === "http") action("管理接口与 OpenAPI", () => actions.edit(service.kind, item));
  } else if (tab === "access") {
    body.append(element("p", `${t("所需 Scope")} · ${item.required_scopes?.join(", ") || "—"}`));
    body.append(element("p", t(item.require_client_grant ? "此服务要求客户端授权。" : "用户权限、工具策略与客户端授权共同决定最终访问。"), "field-note"));
    action("管理权限组", () => { location.hash = "identities"; });
    try {
      if (location.hash !== "#services") return;
      const data = await actions.api("/identities"); if (current !== generation) return;
      const groups = (data.identities || []).filter((p) => p.kind !== "user" && p.permissions.access?.some((a) => a.endpoint_id === item.id));
      body.append(element("h3", t("权限来源")));
      for (const group of groups) body.append(element("p", `${group.name} · ${t(group.enabled ? "已启用" : "已停用")}`));
      if (!groups.length) body.append(element("p", t("尚未给权限组分配此服务。"), "field-note"));
    } catch (error) { if (current === generation) body.append(element("p", error.message)); }
  } else {
    body.append(element("p", `${t("运行状态")} · ${stateLabel(item.runtime.state)}`));
    body.append(element("p", `${t("最近连接检查")} · ${item.last_probe_at ? new Date(item.last_probe_at).toLocaleString(getLocale()) : "—"}`));
    const details = element("details", "", "service-technical"); details.append(element("summary", t("技术详情")), element("p", `${t("版本")} ${item.revision} · ${item.endpoint_uid || item.id}`)); body.append(details);
    action("查看请求", () => { const form = byId("request-filters"); form.reset(); form.elements.endpoint.value = item.id; location.hash = "requests"; form.requestSubmit(); });
    action("查看配置变更", () => { location.hash = "operations"; });
  }
  translateDOM(body);
}
