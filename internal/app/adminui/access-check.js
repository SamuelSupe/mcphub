import { t } from "./i18n.js";

const byId = (id) => document.getElementById(id);
const labels = {
  user_active: "MCPHub 用户已授权且目录状态有效",
  user_tool_resource_allowed: "符合用户与组织的工具和资源权限",
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
  if (grant) {
    byId("access-check-subject").value = grant.subject;
    byId("access-check-grant").value = grant.grant_id;
    byId("access-check-scopes").value = grant.allowed_scopes?.join(" ") || "";
  }
  output.replaceChildren();
  byId("access-check-target").textContent = `${endpoint.id} / ${tool.name}`;
  form.onsubmit = async (event) => {
    event.preventDefault();
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
      if (result.required_approvals) {
        const approval = document.createElement("p");
        approval.textContent =
          t("至少 {count} 人审批", { count: result.required_approvals }) +
          (result.require_step_up ? ` · ${t("加强身份验证")}` : "");
        output.append(approval);
      }
    } catch (error) {
      output.textContent =
        error instanceof SyntaxError
          ? t("调用参数必须是有效 JSON。")
          : error.message;
    } finally {
      byId("access-check-run").disabled = false;
    }
  };
  dialog.showModal();
}
