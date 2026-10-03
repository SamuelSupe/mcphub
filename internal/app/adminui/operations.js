import { t, getLocale } from "./i18n.js";
import { element } from "./tool-policies.js";
import { reviewConfigurationDraft, configurationState } from "./configuration-changes.js";
import { markClean, confirmDiscard } from "./unsaved.js";

let api, snapshot, clients, changes, busy = false, loading = false, generation = 0;
const byId = (id) => document.getElementById(id);

export function initOperations(request) {
  api = request;
  byId("operations-refresh").onclick = refreshOperations;
  byId("oauth-client-add").onclick = () => addClient({ id: "", name: "", redirect_uris: [] });
  byId("oauth-client-form").onsubmit = saveClients;
}

export async function refreshOperations() {
  if (busy || loading) return;
	if (clients && !await confirmDiscard(byId("oauth-client-form"))) return;
  loading = true; byId("operations-refresh").disabled = true; byId("operations-status").setAttribute("aria-busy", "true");
  const current = ++generation; byId("operations-feedback").textContent = t("正在读取配置…");
  try {
    const data = await Promise.all([api("/operations"), api("/configuration-changes"), api("/oauth-clients")]); if (current !== generation) return;
    [snapshot, changes, clients] = data; renderOperations();
	byId("oauth-client-form").hidden = clients.enabled === false;
    byId("oauth-client-rows").replaceChildren(); for (const client of clients.clients) addClient(client);
    const fixed = clients.built_in.filter((c) => c.require_consent).map((c) => `${c.name || c.id} · ${c.id}`).join("\n"); byId("oauth-client-built-in").textContent = fixed;
    markClean(byId("oauth-client-form")); byId("operations-feedback").textContent = "";
  } catch (error) { if (current === generation) byId("operations-feedback").textContent = error.message; }
  finally { loading = false; byId("operations-refresh").disabled = false; byId("operations-status").setAttribute("aria-busy", "false"); }
}

function addClient(client) {
  const row = element("fieldset", "", "identity-access"); row.append(element("legend", t("OAuth 客户端")));
  for (const [name, label, value] of [["id", "客户端 ID", client.id], ["name", "显示名称", client.name], ["redirects", "回调地址（每行一个）", (client.redirect_uris || []).join("\n")]]) {
    const field = element("label", t(label), "field"), input = element(name === "redirects" ? "textarea" : "input", ""); input.name = name; input.value = value || ""; input.required = true; input.maxLength = name === "redirects" ? 32768 : 128; field.append(input); row.append(field);
  }
  const remove = element("button", t("删除"), "secondary"); remove.type = "button"; remove.onclick = () => { row.remove(); byId("oauth-client-form").dispatchEvent(new Event("input", { bubbles: true })); }; row.append(remove); byId("oauth-client-rows").append(row);
}

async function saveClients(event) {
  event.preventDefault(); if (busy) return;
  if (!confirm(t("保存客户端配置会撤销被修改或删除客户端的现有会话和服务授权。继续？"))) return;
  busy = true; byId("oauth-client-save").disabled = true;
  try {
    const values = [...byId("oauth-client-rows").children].map((row) => ({ id: row.querySelector('[name="id"]').value.trim(), name: row.querySelector('[name="name"]').value.trim(), redirect_uris: row.querySelector('[name="redirects"]').value.split("\n").map((v) => v.trim()).filter(Boolean) }));
    clients = await api("/oauth-clients", { method: "PUT", headers: { "If-Match": `"${clients.revision}"` }, body: JSON.stringify({ clients: values }) }); markClean(event.target); byId("operations-feedback").textContent = t("OAuth 客户端已保存。");
  } catch (error) { byId("operations-feedback").textContent = error.message; }
  finally { busy = false; byId("oauth-client-save").disabled = false; }
}

export function renderOperations() {
  if (!snapshot) return;
  const status = byId("operations-status"); status.replaceChildren();
  const date = (value) => new Date(value).toLocaleString(getLocale());
  let listOfFacts;
  const section = (label) => { const panel = element("section", "", "operation-status-card"); listOfFacts = element("dl"); panel.append(element("h3", t(label)), listOfFacts); status.append(panel); };
  const row = (label, value) => listOfFacts.append(element("dt", t(label)), element("dd", String(value)));
  section("运行与配置");
  row("服务版本", snapshot.version); row("配置版本", snapshot.configuration_version); row("存储引擎", snapshot.database_engine); row("部署配置", t(snapshot.deployment_file_changed ? "文件已变化，请校验并重启服务。" : "运行配置与启动文件一致。")); row("加密密钥标识", snapshot.key_id);
  section("身份与审计");
  const age = (value) => [...value.matchAll(/(\d+(?:\.\d+)?)(h|m|s)/g)].filter((match) => Number(match[1]) > 0).map((match) => t({ h: "{count} 小时", m: "{count} 分钟", s: "{count} 秒" }[match[2]], { count: Number(match[1]) })).join(" ") || value;
  const d = snapshot.directory; row("企业组验证", t("{users} 个企业用户，{stale} 个需要重新验证，最长有效 {age}。", { users: d.users, stale: d.stale, age: age(d.maximum_age) }));
  row("请求记录写入失败", snapshot.request_record_failures);
  const a = snapshot.audit_delivery; row("审计归档", a.archive_enabled ? `${a.archive_pending} ${t("等待投递")} · ${a.archive_failed} ${t("投递失败")}` : t("未启用外部审计归档"));
  section("备份与恢复");
  const backup = snapshot.operations.backup, drill = snapshot.operations.recovery_drill;
  row("最近备份", backup ? `${date(backup.created_at)} · ${backup.rows} ${t("行")}` : t("尚未备份"));
  row("恢复演练", drill ? `${date(drill.at)} · ${t(drill.backup_sha256 === backup?.sha256 ? "当前备份已通过隔离恢复检查" : "通过的是较早备份，请重新演练")}` : t("尚未进行恢复演练"));
  row("最近恢复", snapshot.operations.recovery ? `${date(snapshot.operations.recovery.at)} · ${t("原有会话与授权已撤销")}` : t("尚未恢复"));
  const list = byId("configuration-change-list"); list.replaceChildren();
  for (const change of changes.changes) {
    const card = element("article", "", "policy-card"), actions = element("div", "", "page-actions");
    card.append(element("h3", change.target), element("p", `${configurationState(change.state)} · ${date(change.updated_at)}`, "field-note"));
    const attribution = element("details", "", "service-technical"); attribution.append(element("summary", t("技术详情")), element("p", `${t("操作人")} · ${change.actor}`)); card.append(attribution);
    const action = (label, callback) => { const button = element("button", t(label), "secondary"); button.type = "button"; button.onclick = async () => { button.disabled = true; try { await callback(); await refreshOperations(); } catch (error) { byId("operations-feedback").textContent = error.message; } finally { button.disabled = false; } }; actions.append(button); };
    action("查看与继续", () => reviewConfigurationDraft(change, api));
    if (change.state === "applied" && change.before) action("创建回退草稿", async () => { const draft = await api(`/configuration-changes/${change.id}/rollback`, { method: "POST", headers: { "If-Match": `"${change.revision}"` } }); await reviewConfigurationDraft(draft, api); });
    if (["draft", "validated", "failed"].includes(change.state)) action("丢弃草稿", () => api(`/configuration-changes/${change.id}/discard`, { method: "POST", headers: { "If-Match": `"${change.revision}"` } }));
    if (["applied", "failed", "discarded"].includes(change.state)) action("移除历史记录", async () => { if (confirm(t("移除历史记录后，此记录不能再用于回退。继续？"))) await api(`/configuration-changes/${change.id}`, { method: "DELETE", headers: { "If-Match": `"${change.revision}"` } }); });
    if (change.approval_id) { const link = element("a", t("打开审批")); link.href = `/?approval=${encodeURIComponent(change.approval_id)}#approvals`; actions.append(link); }
    card.append(actions); list.append(card);
  }
  if (!changes.changes.length) list.append(element("p", t("尚无配置草稿。编辑服务后可校验、查看影响并应用。"), "field-note"));
}
