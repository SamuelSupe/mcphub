import { t } from "./i18n.js";

const byId = (id) => document.getElementById(id);
let selectionGeneration = 0;
const labels = {
	personal_account_connected: "个人账号已连接，保存的凭证可用",
	personal_account_not_connected: "需要先在个人授权中心连接此服务账号",
	personal_account_expired: "个人账号凭证已过期，需要重新连接",
	personal_account_reconnect_required: "个人账号需要重新连接",
	personal_account_unavailable: "暂时无法验证个人凭证，请检查凭证服务",
	personal_account_subject_required: "检查个人账号需要填写用户 Subject",
  user_active: "MCPHub 用户已授权且目录状态有效",
  user_tool_resource_allowed: "符合用户与组的工具和资源权限",
  endpoint_enabled: "Endpoint 已启用",
  endpoint_ready: "Endpoint 当前可用",
  tool_available: "工具存在于当前目录",
  tool_published: "工具已发布",
  client_grant_required: "已满足客户端 Grant 要求",
  client_grant_active: "客户端授权仍有效",
  client_tool_allowed: "客户端允许此工具及操作类型",
  client_resource_allowed: "符合客户端资源范围",
  client_policy_current: "符合当前 Grant 的完整调用策略",
  required_scopes: "具备全部所需 Scope",
  resource_allowed: "符合工具资源范围",
  approval_service: "远程审批服务已启用",
  approval_arguments: "审批风险条件可评估",
  client_grant_expired: "客户端授权已过期",
  client_grant_revoked: "客户端授权已撤销",
  client_grant_invalid: "客户端授权不存在或不属于该用户",
  client_grant_reconfirmation_required: "客户端授权需要重新确认",
  client_authorization_enabled: "客户端授权服务已启用",
};

export function openAccessCheck(endpoint, tool, api, grant = null) {
  const dialog = byId("access-check-dialog"),
    form = byId("access-check-form"),
    output = byId("access-check-result");
  form.reset();
  let current = ++selectionGeneration;
  const userSelect = byId("access-check-user"), clientSelect = byId("access-check-client"), feedback = byId("access-check-selection-feedback");
  byId("access-check-selection").hidden = true;
  userSelect.replaceChildren(new Option(t("手动填写或选择用户"), ""));
  clientSelect.replaceChildren(new Option(t("不指定授权，只检查用户权限"), ""));
  feedback.textContent = "";
  byId("access-check-run").disabled = false;
  if (grant) {
    byId("access-check-subject").value = grant.subject;
    byId("access-check-grant").value = grant.grant_id;
    byId("access-check-scopes").value = grant.allowed_scopes?.join(" ") || "";
  }
  output.replaceChildren();
  byId("access-check-target").textContent = `${endpoint.id} / ${tool.name}`;
  form.onsubmit = async (event) => {
    event.preventDefault();
    const request = selectionGeneration;
    output.replaceChildren();
    byId("access-check-run").disabled = true;
    try {
      const rawArguments = byId("access-check-arguments").value.trim() || "{}";
      JSON.parse(rawArguments);
      // Keep the original JSON text: parsing and stringifying would round large
      // integers before the server evaluates the submitted resource selectors.
      const fields = JSON.stringify({
        endpoint: endpoint.id,
        tool: tool.name,
        scopes: [
          ...new Set(
            byId("access-check-scopes")
              .value.split(/[\s,]+/)
              .filter(Boolean),
          ),
        ],
        subject: byId("access-check-subject").value,
        grant_id: byId("access-check-grant").value.trim(),
      });
      const result = await api("/access-check", {
        method: "POST",
        body: fields.slice(0, -1) + ',"arguments":' + rawArguments + "}",
      });
      if (request !== selectionGeneration || !dialog.open) return;
      const heading = document.createElement("h3");
      heading.textContent = t(
        {
          denied: "当前条件下不能调用",
          requires_approval: "通过权限检查，仍需逐次审批",
          checks_passed: "通过本次权限检查",
        }[result.outcome],
      );
      output.append(heading);
      for (const check of result.checks) {
        const row = document.createElement("div");
        row.className = `access-step ${check.passed ? "access-pass" : "access-fail"}`;
        row.textContent = `${t(check.passed ? "通过" : "未通过")} · ${t(labels[check.code] || check.code)}`;
        if (check.missing_scopes?.length) {
          const missing = document.createElement("small");
          missing.textContent = `${t("缺失 Scope")}：${check.missing_scopes.join(", ")}`;
          row.append(missing);
        }
        output.append(row);
      }
      const scopes = document.createElement("p");
      scopes.textContent = `${t("有效 Scope")}：${result.effective_scopes?.join(", ") || "—"}`;
      output.append(scopes);
	  if (result.group_sources?.length) { const sources = document.createElement("p"); sources.textContent = `${t("权限来源")} · ${result.group_sources.map((s) => s.group.name).join(", ")}`; output.append(sources); }
      if (result.required_approvals) {
        const approval = document.createElement("p");
        approval.textContent =
          t("至少 {count} 人审批", { count: result.required_approvals }) +
          (result.require_step_up ? ` · ${t("加强身份验证")}` : "");
        output.append(approval);
      }
    } catch (error) {
      if (request !== selectionGeneration || !dialog.open) return;
      output.textContent =
        error instanceof SyntaxError
          ? t("调用参数必须是有效 JSON。")
          : error.message;
    } finally {
      if (request === selectionGeneration) byId("access-check-run").disabled = false;
    }
  };
  dialog.showModal();
  const selectUser = async () => {
    current = ++selectionGeneration;
    const subject = userSelect.value;
    byId("access-check-subject").value = subject;
    byId("access-check-grant").value = "";
    byId("access-check-scopes").value = "";
    clientSelect.replaceChildren(new Option(t("不指定授权，只检查用户权限"), ""));
    feedback.textContent = "";
    byId("access-check-run").disabled = clientSelect.disabled = false;
    if (!subject) return;
    const request = current;
    byId("access-check-run").disabled = clientSelect.disabled = true;
    try {
      const [effective, result] = await Promise.all([
        api(`/identities/${encodeURIComponent(subject)}/effective`),
        api(`/client-grants?${new URLSearchParams({ subject, endpoint: endpoint.id, status: "active", limit: "100" })}`),
      ]);
      if (request !== selectionGeneration || !dialog.open) return;
      byId("access-check-scopes").value = effective.active ? (effective.permissions.scopes || []).join(" ") : "";
      for (const item of result.grants || []) {
        const option = new Option(`${item.client_name} · ${item.grant_id.slice(-8)}`, item.grant_id);
        option.dataset.scopes = JSON.stringify(item.allowed_scopes || []);
        clientSelect.append(option);
      }
      feedback.textContent = t(effective.active ? "已载入用户当前权限。选择客户端授权可进一步检查其范围；Scope 仍可修改为假设条件。" : "此用户尚未启用或企业身份需要重新验证。");
      if (result.next_cursor) feedback.textContent += " " + t("仅列出前 100 条有效授权，其他授权可手动填写 ID。");
    } catch (error) { if (request === selectionGeneration && dialog.open) feedback.textContent = error.message; }
    finally { if (request === selectionGeneration) byId("access-check-run").disabled = clientSelect.disabled = false; }
  };
  userSelect.onchange = selectUser;
  clientSelect.onchange = () => {
    byId("access-check-grant").value = clientSelect.value;
    // Returning to user-only diagnosis must restore the user's current scopes.
    if (!clientSelect.value) { selectUser(); return; }
    byId("access-check-scopes").value = JSON.parse(clientSelect.selectedOptions[0].dataset.scopes).join(" ");
  };
  api("/identities").then((data) => {
    if (current !== selectionGeneration || !dialog.open || !data.enabled) return;
    const providers = new Map((data.providers || []).map((p) => [p.id, p.name]));
    for (const user of data.identities || []) if (user.kind === "user") userSelect.append(new Option(`${user.name || user.external_id} · ${providers.get(user.provider) || user.provider}`, user.id));
    byId("access-check-selection").hidden = false;
    // A supplied grant is deliberately preserved, even when no longer active.
    if (grant) {
      userSelect.value = grant.subject;
      const option = new Option(grant.client_name || grant.grant_id, grant.grant_id); option.dataset.scopes = JSON.stringify(grant.allowed_scopes || []); clientSelect.append(option); clientSelect.value = grant.grant_id;
    }
  }).catch((error) => { if (current === selectionGeneration && dialog.open) { byId("access-check-selection").hidden = false; feedback.textContent = error.message; } });
}
