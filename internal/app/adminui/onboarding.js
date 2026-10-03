import { t, getLocale } from "./i18n.js";
import { element, showToolPolicies } from "./tool-policies.js";
import { openAccessCheck } from "./access-check.js";
import { showClientRequests } from "./request-diagnostics.js";

let api, services = [], publicURL = "", users = [], snapshot;
let generation = 0;
let renderedServices = "";
const byId = (id) => document.getElementById(id);

export function pairingCommand(publicURL) {
  if (!publicURL) return "";
  const quotedURL = "'" + publicURL.replaceAll("'", "'\\''") + "'";
  return `mcpbridge pair start --server ${quotedURL} --profile work --name 'Work Agent' --json`;
}

// A successful catalog read or permission simulation is not a tool execution.
export function setupProgress(endpoint, tool, identity, grants, requests, now = Date.now()) {
  const permissions = identity?.permissions || {};
  const scopes = permissions.scopes || [];
  const allowed = identity?.active && (permissions.access || []).some((access) =>
    access.endpoint_id === endpoint?.id && access.tools?.includes(tool?.name) && (tool?.effect === "read" || access.allow_write_requests));
  const grant = grants.find((g) => g.status === "active" && Date.parse(g.expires_at) > now &&
    g.endpoint_id === endpoint?.id && g.subject === identity?.identity_id && g.capabilities?.tools &&
    g.allowed_tools?.includes(tool?.name) && (tool?.required_scopes || []).every((s) => g.allowed_scopes?.includes(s)));
  const call = requests.find((r) => r.method === "tools/call" && r.outcome === "success" && r.endpoint === endpoint?.id &&
    r.tool === tool?.name && r.subject === identity?.identity_id);
  return { grant, call, complete: [
    !!(endpoint?.enabled && endpoint?.ready),
    !!(tool?.published && tool.available && tool.effect === "read"),
    !!(allowed && (tool?.required_scopes || []).every((s) => scopes.includes(s))),
    !!(endpoint && !endpoint.require_client_grant || grant),
    !!call,
  ] };
}

export function initOnboarding(request) {
  api = request;
  byId("setup-refresh").onclick = refreshOnboarding;
  byId("setup-service").onchange = refreshOnboarding;
  byId("setup-user").onchange = loadSelection;
  byId("setup-tool").onchange = loadSelection;
}

export function updateOnboarding(overview, backends, groups) {
  services = [...backends.map((b) => b.id), ...groups.map((g) => g.id)];
  publicURL = overview.public_url || "";
  const key = JSON.stringify([getLocale(), services, publicURL]);
  if (key === renderedServices) return;
  renderedServices = key;
  const select = byId("setup-service"), previous = select.value;
  select.replaceChildren(new Option(t("选择目标服务"), ""), ...services.map((id) => new Option(id, id)));
  select.value = services.includes(previous) ? previous : services[0] || "";
  renderOnboarding();
}

export async function refreshOnboarding() {
  if (!api || (location.hash && location.hash !== "#overview")) return;
  const current = ++generation;
  byId("setup-feedback").textContent = t("正在读取配置…");
  try {
    const data = await api("/identities");
    if (current !== generation) return;
    users = (data.identities || []).filter((p) => p.kind === "user");
    const select = byId("setup-user"), previous = select.value;
    select.replaceChildren(new Option(t("选择验收用户"), ""), ...users.map((p) => new Option(`${p.name || p.external_id} · ${p.provider === "mcphub:local" ? t("内建账号") : p.provider.split("#")[0]}`, p.id)));
    select.value = users.some((p) => p.id === previous) ? previous : "";
    const endpointID = byId("setup-service").value;
    const endpoint = endpointID ? await api(`/tool-policies?${new URLSearchParams({ endpoint: endpointID })}`) : null;
    if (current !== generation) return;
    snapshot = { endpoint };
    const toolSelect = byId("setup-tool"), previousTool = toolSelect.value;
    const tools = snapshot.endpoint?.tools || [];
    toolSelect.replaceChildren(new Option(t("选择验收工具"), ""), ...tools.map((tool) => new Option(`${tool.name} · ${t({ read: "只读", write: "写入", unknown: "未分类" }[tool.effect] || tool.effect)}`, tool.name)));
    toolSelect.value = tools.some((tool) => tool.name === previousTool) ? previousTool : tools.find((tool) => tool.published && tool.available && tool.effect === "read")?.name || "";
    await loadSelection();
  } catch (error) {
    if (current !== generation) return;
    snapshot = null;
    renderOnboarding();
    byId("setup-feedback").textContent = error.message;
  }
}

async function loadSelection() {
  const current = ++generation;
  const endpoint = snapshot?.endpoint;
  const subject = byId("setup-user").value;
  const tool = endpoint?.tools.find((t) => t.name === byId("setup-tool").value);
  snapshot = { endpoint, tool };
  renderOnboarding();
  if (!endpoint || !subject || !tool) return;
  byId("setup-feedback").textContent = t("正在读取配置…");
  try {
    const query = new URLSearchParams({ subject, endpoint: endpoint.id, limit: "100" });
    const [identity, grants, history] = await Promise.all([
      api(`/identities/${encodeURIComponent(subject)}/effective`),
      api(`/client-grants?${query}&status=active`),
      api(`/requests?${query}&tool=${encodeURIComponent(tool.name)}&outcome=success`),
    ]);
    if (current !== generation) return;
    snapshot = { endpoint, tool, identity, grants: grants.grants || [], history };
    renderOnboarding();
  } catch (error) {
    if (current === generation) byId("setup-feedback").textContent = error.message;
  }
}

export function renderOnboarding() {
  if (!byId("setup-progress")) return;
  const { endpoint, tool, identity, grants = [], history } = snapshot || {};
  const progress = setupProgress(endpoint, tool, identity, grants, history?.requests || []);
  const steps = [
    ["1. 服务可供调用", "接入服务并启用。HTTP 工具组的连通性以实际调用为准。", "#services"],
    ["2. 发布只读验收工具", "选择一个已发布的只读工具，便于首次接入验收。", "#tool-policies"],
    ["3. 用户组授权齐全", "启用用户，将其加入已授权工具与 Scope 的有效组。资源条件仍需检查。", "#identities"],
    ["4. 客户端已授权", "由验收用户在浏览器确认客户端和工具范围；当前策略仍需权限检查。", "#client-grants"],
    ["5. 完成真实调用", "用验收用户的 Agent 调用所选工具，然后刷新。这里只统计最近 24 小时的成功 tools/call。", "#requests"],
  ];
  const list = byId("setup-progress");
  list.replaceChildren();
  steps.forEach(([title, description, href], index) => {
    const row = element("li", "", progress.complete[index] ? "setup-complete" : "");
    row.append(element("span", t(progress.complete[index] ? "通过" : "待完成"), "policy-tag"));
    const link = element("a", t(title)); link.href = endpoint?.kind === "tool-group" && index === 0 ? "#tool-groups" : href;
    if (index === 1 && endpoint) link.onclick = (event) => { event.preventDefault(); showToolPolicies(endpoint.id, tool?.name); };
    if (index === 4 && identity) link.onclick = (event) => { event.preventDefault(); showClientRequests({ subject: identity.identity_id, endpoint_id: endpoint.id, client_instance_id: "" }); };
    row.append(link, element("p", t(description)));
    if (index === 4 && progress.call) row.append(element("small", new Date(progress.call.completed_at).toLocaleString(), "field-note"));
    list.append(row);
  });
  byId("setup-feedback").textContent = !endpoint || !tool || !identity
    ? t("选择服务、工具与验收用户，检查这一次接入。")
    : t("当前配置检查 {count} / 5 项通过。权限检查不会执行工具；历史成功不代表当前凭证仍有效。", { count: progress.complete.filter(Boolean).length });
  const check = byId("setup-check"); check.disabled = !tool || !identity?.active;
  check.onclick = () => openAccessCheck(endpoint, tool, api, { subject: identity.identity_id, allowed_scopes: identity.permissions.scopes, grant_id: progress.grant?.grant_id || "" });
  byId("setup-command").value = pairingCommand(publicURL);
  byId("setup-command").hidden = !publicURL;
}
