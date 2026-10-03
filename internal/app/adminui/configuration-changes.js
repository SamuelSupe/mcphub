import { clearUnsaved } from "./unsaved.js";
import { t } from "./i18n.js";
import { element } from "./tool-policies.js";

export function configurationState(state) { return t({ draft: "草稿", validated: "已校验", awaiting_approval: "等待审批", applied: "已应用", failed: "失败", discarded: "已丢弃" }[state] || state); }

export function isConfigurationWrite(path, options) {
  return ["POST", "PUT"].includes(options.method) && !options.headers?.["X-MCPHub-Change-Mode"] &&
    (/^\/backends(?:\/[^/]+)?$/.test(path) && path !== "/backends/probe" || /^\/tool-groups(?:\/[^/]+(?:\/(?:tools|imports)(?:\/[^/]+)?)?)?$/.test(path) && !path.endsWith("/inspect"));
}

export async function reviewConfigurationDraft(initial, api) {
  let draft = initial, busy = false, outcome = null;
  const dialog = element("dialog", "", "configuration-review local-account-dialog");
  const title = element("h2", t("检查配置变更")), content = element("div", ""), feedback = element("p", ""); feedback.setAttribute("role", "status");
  const actions = element("div", "", "page-actions");
  const button = (label, callback) => { const b = element("button", t(label), "secondary"); b.type = "button"; b.onclick = callback; actions.append(b); return b; };
  const run = async (action) => {
    if (busy) return; busy = true; feedback.textContent = t("正在处理配置变更…"); render();
    try {
      draft = await api(`/configuration-changes/${encodeURIComponent(draft.id)}/${action}`, { method: "POST", headers: { "If-Match": `"${draft.revision}"` } });
      if (action === "apply" && ["applied", "awaiting_approval"].includes(draft.state)) { outcome = draft; dialog.close(); }
      else feedback.textContent = t(draft.state === "validated" ? "校验通过，尚未应用。" : draft.state === "failed" ? "校验或应用失败，请检查服务版本与连接。" : "配置草稿已保存。");
    } catch (error) { if (error.change?.id) draft = error.change; feedback.textContent = error.message; }
    finally { busy = false; render(); }
  };
  const validate = button("校验草稿", () => run("validate")), apply = button("应用或提交审批", () => run("apply")), close = button("保留草稿并关闭", () => dialog.close());
  apply.className = "primary";
  const render = () => {
    content.replaceChildren(element("p", `${draft.target} · ${t("配置状态")} ${configurationState(draft.state)}`));
    const impact = draft.impact || {};
    content.append(element("p", t("影响估计：{groups} 个权限组、{users} 个用户、{sessions} 个会话、{grants} 个服务授权。", { groups: impact.permission_groups || 0, users: impact.users || 0, sessions: impact.sessions || 0, grants: impact.grants || 0 }), "field-note"));
    content.append(element("p", t("配置保存在数据库，应用后立即生效。权限相关变化可能要求重新授权；回退不会恢复已撤销的授权。"), "field-note"));
    if (draft.credentials_changed) content.append(element("p", t("上游凭证发生变化；差异中隐藏了凭证内容。"), "context-note"));
    for (const [label, data] of [["变更前", draft.before], ["变更后", draft.after]]) { const details = element("details", ""); details.append(element("summary", t(label)), element("pre", JSON.stringify(data, null, 2))); content.append(details); }
    if (draft.error) content.append(element("p", t("校验或应用失败，请检查服务版本与连接。"), "form-error"));
    validate.disabled = busy || !["draft", "validated", "failed"].includes(draft.state);
    apply.disabled = busy || draft.state !== "validated";
    close.disabled = busy;
  };
  dialog.addEventListener("cancel", (e) => { if (busy) e.preventDefault(); });
  dialog.append(title, content, feedback, actions); document.body.append(dialog); render(); dialog.showModal();
  await new Promise((resolve) => dialog.addEventListener("close", resolve, { once: true })); dialog.remove();
  return outcome;
}

export async function finishConfigurationWrite(draft, api) {
  const result = await reviewConfigurationDraft(draft, api);
  if (!result) throw new Error(t("草稿已保留，可以在运维中心继续。"));
  if (result.state === "awaiting_approval") { clearUnsaved(); document.querySelectorAll("dialog[open]").forEach((dialog) => dialog.close()); sessionStorage.setItem("mcphub.approval", result.approval_id); location.assign(`/?approval=${encodeURIComponent(result.approval_id)}#approvals`); throw new Error(t("配置变更已提交审批，批准前不会生效。")); }
  const [service, child] = result.target.split("/");
  const path = result.kind === "backend" ? `/backends/${encodeURIComponent(service)}` : result.kind === "tool_group" ? `/tool-groups/${encodeURIComponent(service)}` : `/tool-groups/${encodeURIComponent(service)}/${result.kind === "http_tool" ? "tools" : "imports"}/${encodeURIComponent(child)}`;
  return api(path);
}
