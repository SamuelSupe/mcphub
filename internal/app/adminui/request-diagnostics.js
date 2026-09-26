import { t, getLocale } from "./i18n.js";
import { element, showToolPolicies } from "./tool-policies.js";

const byId = (id) => document.getElementById(id);
const outcomes = {
  success: "成功",
  auth_denied: "身份认证失败",
  scope_denied: "缺少 Scope",
  grant_denied: "客户端授权拒绝",
  policy_denied: "策略拒绝",
  rate_limited: "触发限流",
  approval_pending: "等待审批或恢复",
  tool_error: "工具返回错误",
  protocol_error: "协议错误",
  cancelled: "已取消",
  unavailable: "服务不可用",
};
let api,
  snapshot,
  generation = 0,
  cursors = [""],
  filters = new URLSearchParams();

export function initRequestDiagnostics(request) {
  api = request;
  byId("request-filters").addEventListener("submit", (event) => {
    event.preventDefault();
    filters = new URLSearchParams(new FormData(event.target));
    cursors = [""];
    refreshRequestDiagnostics();
  });
  byId("request-reset").addEventListener("click", () => {
    byId("request-filters").reset();
    filters = new URLSearchParams();
    cursors = [""];
    refreshRequestDiagnostics();
  });
  byId("request-prev").addEventListener("click", () => {
    if (cursors.length > 1) cursors.pop();
    refreshRequestDiagnostics();
  });
  byId("request-next").addEventListener("click", () => {
    if (snapshot?.next_cursor) cursors.push(String(snapshot.next_cursor));
    refreshRequestDiagnostics();
  });
}

export function showClientRequests(grant) {
  byId("request-filters").reset();
  const form = byId("request-filters");
  form.elements.subject.value = grant.subject;
  form.elements.client.value = grant.client_instance_id;
  form.elements.endpoint.value = grant.endpoint_id;
  filters = new URLSearchParams(new FormData(form));
  cursors = [""];
  location.hash = "requests";
}

export async function refreshRequestDiagnostics() {
  const current = ++generation;
  const query = new URLSearchParams(filters);
  query.set("cursor", cursors.at(-1));
  byId("request-feedback").textContent = t("正在读取请求…");
  byId("request-list").setAttribute("aria-busy", "true");
  byId("request-prev").disabled = byId("request-next").disabled = true;
  try {
    const result = await api(`/requests?${query}`);
    if (current !== generation) return;
    snapshot = result;
    renderRequestDiagnostics();
  } catch (error) {
    if (current !== generation) return;
    snapshot = null;
    byId("request-list").replaceChildren();
    byId("request-stats").replaceChildren();
    byId("request-feedback").textContent = error.message;
    byId("request-prev").disabled = cursors.length < 2;
  } finally {
    if (current === generation)
      byId("request-list").setAttribute("aria-busy", "false");
  }
}

export function renderRequestDiagnostics() {
  if (!snapshot) return;
  const list = byId("request-list"),
    stats = byId("request-stats");
  list.replaceChildren();
  stats.replaceChildren();
  const s = snapshot.statistics;
  const denied = Object.entries(s.outcomes)
    .filter(([key]) => key.endsWith("_denied") || key === "rate_limited")
    .reduce((total, [, count]) => total + count, 0);
  for (const [label, value] of [
    ["窗口内请求", s.total],
    ["拒绝与限流", denied],
    ["工具返回错误", s.outcomes.tool_error || 0],
    ["P95 耗时", `${s.p95_ms} ms`],
    [
      "平均审批等待",
      s.approval_resumes
        ? `${Math.round(s.average_approval_wait_ms / 1000)} s`
        : "—",
    ],
  ]) {
    const card = element("div", "", "diagnostic-stat");
    card.append(element("span", t(label)), element("strong", String(value)));
    stats.append(card);
  }
  byId("request-feedback").textContent = snapshot.requests.length
    ? t("第 {page} 页 · {count} 条请求", {
        page: cursors.length,
        count: snapshot.requests.length,
      })
    : t("没有符合条件的已完成请求");
  byId("request-prev").disabled = cursors.length < 2;
  byId("request-next").disabled = !snapshot.next_cursor;
  for (const record of snapshot.requests) {
    const card = element("details", "", "policy-card diagnostic-record");
    const header = element("summary", "", "record-summary");
    const title = element("span", "", "record-title");
    title.append(element("strong", record.tool ? `${record.endpoint} / ${record.tool}` : record.method || "HTTP"));
    title.append(element("small", `${record.subject || "—"} · ${new Date(record.started_at).toLocaleTimeString(getLocale())}`));
    const status = element("span", t(outcomes[record.outcome] || record.outcome), "policy-tag");
    status.dataset.tone = record.outcome === "success" ? "success" : record.outcome === "approval_pending" || record.outcome === "cancelled" ? "warning" : "danger";
    header.append(
      title,
      status,
      element("span", `${record.duration_ms} ms`, "record-duration"),
    );
    const body = element("div", "", "record-detail");
    const fields = element("dl", "");
    const rows = [
      ["请求 ID", record.request_id],
      ["开始时间", new Date(record.started_at).toLocaleString(getLocale())],
      ["耗时", `${record.duration_ms} ms · HTTP ${record.http_status}`],
      ["用户 Subject", record.subject || "—"],
      ["客户端 ID", record.client_id || "—"],
    ];
    if (record.reason) rows.push(["原因代码", record.reason]);
    if (record.grant_id) rows.push(["Grant ID", record.grant_id]);
    if (record.credential_id) rows.push(["凭证关联 ID", record.credential_id]);
    if (record.upstream_account) rows.push(["上游账号", record.upstream_account]);
    if (record.approval_id) rows.push(["审批 ID", record.approval_id]);
    if (record.approval_wait_ms)
      rows.push([
        "审批等待",
        `${Math.round(record.approval_wait_ms / 1000)} s`,
      ]);
    for (const [label, value] of rows)
      fields.append(element("dt", t(label)), element("dd", value));
    body.append(fields);
    card.append(header, body);
    if (record.endpoint) {
      const actions = element("div", "", "policy-card-actions");
      const policy = element("button", t("查看工具权限"), "secondary");
      policy.type = "button";
      policy.onclick = () => showToolPolicies(record.endpoint, record.tool);
      actions.append(policy);
      body.append(actions);
    }
    list.append(card);
  }
}
