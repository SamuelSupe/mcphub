import { canInspectDelivery } from "./auth.js";
import { getLocale, t } from "./i18n.js";

const query = new URLSearchParams(location.search);
const requestedID = query.get("approval");
const verification = query.get("verification");
if (requestedID && /^[A-Za-z0-9]{26}$/.test(requestedID)) sessionStorage.setItem("mcphub.approval", requestedID);
const selectedID = requestedID || sessionStorage.getItem("mcphub.approval");
if (selectedID) {
  history.replaceState(null, "", `/?approval=${encodeURIComponent(selectedID)}#approvals`);
  sessionStorage.removeItem("mcphub.approval");
}
const statuses = {
  pending: "待审批", approved: "已批准，等待恢复", rejected: "已拒绝", expired: "已过期",
  revoked: "已撤销", cancelled: "已取消", executing: "执行中", succeeded: "已完成", failed: "执行返回错误", unknown: "结果不确定，需人工核查",
};
const actions = { reviewed:"已记录一票批准", reminded:"已发送到期提醒", requested: "已申请写入审批", approved: "已批准写入", rejected: "已拒绝写入", revoked: "已撤销", cancelled: "已取消", expired: "已过期", executing: "开始执行写入", succeeded: "写入已完成", failed: "写入返回错误", unknown: "写入结果不确定", investigated: "已记录人工核查" };
let snapshot = "", records = [], enabled = false, request, cursor = "", nextCursor = "", sequence = 0, pinSelection = true;
let initialized = false;
const previous = [], drafts = new Map(), histories = new Map();
const filters = new URLSearchParams();

function element(tag, text, className = "") {
  const node = document.createElement(tag);
  node.textContent = text;
  if (className) node.className = className;
  return node;
}
function button(label, action) {
  const node = element("button", t(label), "secondary"); node.type = "button";
  node.addEventListener("click", action); return node;
}
function feedback(text) { document.querySelector("#approval-feedback").textContent = text; }
async function reload() { try { await refreshApprovals(request); } catch (error) { feedback(error.message); } }

export async function refreshApprovals(api) {
  request = api;
  if (!initialized) {
    initialized = true;
    document.querySelector("#approval-filter").addEventListener("submit", (event) => {
      event.preventDefault();
      for (const [key, value] of new FormData(event.currentTarget)) filters.set(key, value.trim());
      cursor = ""; previous.length = 0; pinSelection = false; feedback(""); reload();
    });
    document.querySelector("#approval-next").addEventListener("click", () => { previous.push(cursor); cursor = nextCursor; pinSelection = false; reload(); });
    document.querySelector("#approval-prev").addEventListener("click", () => { cursor = previous.pop() || ""; pinSelection = false; reload(); });
    if (verification) feedback(t(verification === "complete" ? "身份验证已完成，请核对请求并填写批准理由。" : "身份验证未通过，请重试或联系身份服务管理员。"));
  }
  const generation = ++sequence;
  try {
    const delivery = canInspectDelivery() ? await api("/approvals/delivery") : {};
    const warning = document.querySelector("#approval-delivery");
    warning.hidden = !(delivery.archive_failed || delivery.notifications_failed || (delivery.archive_pending && !delivery.archive_enabled) || (delivery.notifications_pending && !delivery.notifications_enabled));
    warning.textContent = t("审批通知或审计归档投递失败，正在重试。请检查接收服务；未归档记录不会被清理。");
  } catch (error) { if (error.status !== 403) feedback(error.message); }

  const parameters = new URLSearchParams(filters); parameters.set("cursor", cursor); parameters.set("limit", "25");
  const data = await api(`/approvals?${parameters}`);
  if (generation !== sequence) return;
  let values = data.approvals || [];
  if (data.enabled && pinSelection && selectedID && !values.some((item) => item.id === selectedID)) {
    try { values.unshift(await api(`/approvals/${encodeURIComponent(selectedID)}`)); }
    catch (error) { if (error.status !== 404) throw error; }
  }
  if (generation !== sequence) return;
  if (pinSelection && selectedID) values.sort((a, b) => Number(b.id === selectedID) - Number(a.id === selectedID));
  enabled = data.enabled; records = values; nextCursor = data.next_cursor || "";
  renderApprovals();
}

export function renderApprovals() {
  document.querySelector("#approval-prev").disabled = previous.length === 0;
  document.querySelector("#approval-next").disabled = !nextCursor;
  const next = JSON.stringify([enabled, records, getLocale()]);
  if (next === snapshot) return;
  snapshot = next;
  document.querySelector("#approval-disabled").hidden = enabled;
  const list = document.querySelector("#approval-list"); list.replaceChildren();
  if (!records.length) { list.append(element("p", t("暂无审批请求"))); return; }
  const date = new Intl.DateTimeFormat(getLocale(), { dateStyle: "short", timeStyle: "medium" });
  for (const record of records) {
    const card = element("article", "", "approval-card");
    if (record.id === selectedID) card.classList.add("selected");
    const header = element("header", "", "approval-card-header");
    const status = element("span", t(statuses[record.status] || record.status), "policy-tag");
    status.dataset.tone = ["succeeded", "approved"].includes(record.status) ? "success" : ["failed", "unknown", "rejected"].includes(record.status) ? "danger" : "warning";
    header.append(element("h3", record.tool), status);
    card.append(header);
    for (const summary of record.summaries || []) {
      const block = element("section", "", "approval-summary");
      block.append(element("h4", summary.action));
      if (summary.environment) block.append(element("p", `${t("环境")}：${summary.environment}`));
      for (const [pointer, value] of Object.entries(summary.resources || {})) block.append(element("p", `${pointer}：${value}`));
      if (summary.version) block.append(element("p", `${t("前置版本")}：${summary.version}`));
      card.append(block);
    }
    const facts = document.createElement("dl");
    for (const [label, value] of [
      ["审批编号", record.id], ["申请人", record.subject], ["身份服务", record.issuer], ["目标服务", record.target],
      ["工具类型", t(record.kind === "configuration" ? "配置变更" : record.effect === "write" ? "写入" : "未分类（按写入处理）")], ["申请时间", date.format(new Date(record.created_at))],
      [record.status === "approved" ? "执行期限" : "有效期至", date.format(new Date(record.expires_at))], ["审批人", record.reviewer || "—"],
    ]) facts.append(element("dt", t(label)), element("dd", value));
    facts.append(element("dt", t("批准进度")), element("dd", `${(record.approved_by || []).length} / ${record.required_approvals || 1}`));
    if (record.approved_by?.length) facts.append(element("dt", t("已批准人员")), element("dd", record.approved_by.join(", ")));
    if (record.operation_id) facts.append(element("dt", t("业务操作 ID")), element("dd", record.operation_id));
    if(record.client_grant) for(const [label,value] of [["客户端入口",record.client_grant.client_instance_id],["Broker 会话",record.client_grant.broker_session_id],["客户端授权",record.client_grant.grant_id],["授权版本",String(record.client_grant.grant_revision)],["目标身份",record.client_grant.endpoint_uid]]) facts.append(element("dt",t(label)),element("dd",value));
    card.append(facts);
    if (record.credentials_changed) card.append(element("strong",t("此提案会更换上游凭证；凭证值已隐藏。")));
    if (record.kind === "configuration") card.append(element("p", t("配置变更需要另一位安全管理员审批，批准后立即应用；版本变化会使提案失效。")));
    if (record.already_approved && record.status === "pending") card.append(element("p", t("你已批准，正在等待另一位审批人。")));

    if (record.preview_json) {
      const preview = element("section", "", "approval-preview");
      for (const [label, data] of [["变更前", record.before_json], ["拟变更后", record.after_json]]) {
        const part = document.createElement("div"); part.append(element("h4", t(label)), element("pre", data)); preview.append(part);
      }
      if (record.kind !== "configuration") card.append(element("p", t("预览来自配置的只读工具；执行时后端仍需原子检查前置版本。"))); card.append(preview);
    }
    const details = document.createElement("details"); details.open = record.status === "pending" || record.id === selectedID;
    details.append(element("summary", t("完整执行请求")), element("pre", record.request_json)); card.append(details);
    if (enabled) renderActions(card, record);
    const history = document.createElement("div");
    const showHistory = async () => {
      try {
        const value = await request(`/approvals/${encodeURIComponent(record.id)}`);
        histories.set(record.id, value.history || []); renderHistory();
      } catch (error) { history.textContent = error.message; }
    };
    const renderHistory = () => {
      history.replaceChildren();
      for (const event of histories.get(record.id) || []) {
        const row = element("p", `${date.format(new Date(event.created_at))} · ${event.actor} · ${t(actions[event.action] || event.action)}`);
        if (event.detail?.reason) row.append(element("span", ` — ${event.detail.reason}`));
        if (event.detail?.outcome) row.append(element("strong", ` (${t({applied:"已生效",not_applied:"未生效",uncertain:"仍不确定"}[event.detail.outcome])})`));
        if (event.detail?.acr) row.append(element("small", ` · ACR: ${event.detail.acr}`));
        history.append(row);
      }
    };
    card.append(button("查看审计记录", showHistory), history); renderHistory();
    list.append(card);
  }
}

function renderActions(card, record) {
  const active = ["pending", "approved"].includes(record.status);
  if (!(active && (record.can_review || record.can_cancel)) && !(record.status === "unknown" && record.can_review)) return;
  const draft = drafts.get(record.id) || { reason: "", checked: false };
  drafts.set(record.id, draft);
  const label = element("label", t(record.status === "unknown" ? "核查说明" : "操作理由"));
  const reason = document.createElement("textarea"); reason.name = "reason"; reason.rows = 2; reason.maxLength = 2048; reason.value = draft.reason;
  label.append(reason); card.append(label);
  const controls = element("div", "", "page-actions");
  const message = element("p", ""); message.setAttribute("role", "alert");
  const perform = async (decision, outcome = "") => {
    if (!reason.value.trim()) { message.textContent = t("请填写操作理由"); reason.focus(); return; }
    for (const control of controls.querySelectorAll("button")) control.disabled = true;
    try {
      await request(`/approvals/${encodeURIComponent(record.id)}`, { method: "POST", body: JSON.stringify({ decision, reason: reason.value, outcome }) });
      feedback("");
      histories.delete(record.id); drafts.delete(record.id); snapshot = "";
      await refreshApprovals(request);
    } catch (error) { message.textContent = error.message; for (const control of controls.querySelectorAll("button")) control.disabled = false; syncApprove(); }
  };
  let approve, checked;
  const syncApprove = () => { if (approve) approve.disabled = record.already_approved ||  !checked.checked || !reason.value.trim() || (record.step_up_required && !record.step_up_verified); };
  reason.addEventListener("input", () => { draft.reason = reason.value; syncApprove(); });
  if (record.status === "pending" && record.can_review) {
    if (record.step_up_required && !record.already_approved) {
      card.append(element("p", t(record.step_up_verified ? "本次身份验证已通过，短时有效且仅能使用一次。" : "批准前需要加强身份验证。")));
      controls.append(button("加强身份验证", async () => {
        try {
          const value = await request(`/approvals/${encodeURIComponent(record.id)}/verify`, { method: "POST" });
          sessionStorage.setItem("mcphub.approval", record.id); location.assign(value.authorization_url);
        } catch (error) { message.textContent = error.message; }
      }));
    }
    const confirm = element("label", "", "approval-confirm"); checked = document.createElement("input"); checked.name = "acknowledged"; checked.type = "checkbox"; checked.checked = draft.checked;
    confirm.append(checked, document.createTextNode(t("我已核对申请人、目标和完整请求"))); card.append(confirm);
    approve = button(record.kind === "configuration" ? "批准并应用" : "批准一次", () => perform("approved")); approve.className = "primary";
    checked.addEventListener("change", () => { draft.checked = checked.checked; syncApprove(); });
    controls.append(approve, button("拒绝", () => perform("rejected"))); syncApprove();
  }
  if (record.status === "approved" && record.can_review) controls.append(button("撤销批准", () => perform("revoked")));
  if (active && record.can_cancel) controls.append(button("取消申请", () => perform("cancelled")));
  if (record.status === "unknown") {
    const label = element("label", t("核查结果")); const result = document.createElement("select"); result.name = "outcome";
    for (const [value, text] of [["uncertain","仍不确定"],["applied","已生效"],["not_applied","未生效"]]) { const option = element("option", t(text)); option.value = value; result.append(option); }
    label.append(result); card.append(label, element("p", t("核查只添加证据，不改变执行状态，也不会重放请求。")));
    controls.append(button("记录核查", () => perform("investigated", result.value)));
  }
  card.append(controls, message);
}
