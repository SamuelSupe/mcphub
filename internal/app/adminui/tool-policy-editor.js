import { t } from "./i18n.js";

const byId = (id) => document.getElementById(id);
const scopes = (value) => [...new Set(value.split(/[\s,]+/).filter(Boolean))];

export function resourceRow(rule = {}, parent = byId("policy-resources")) {
  const row = document.createElement("div");
  row.className = "policy-resource-row";
  const pointer = Object.assign(document.createElement("input"), {
    value: rule.argument || "",
    placeholder: "/project",
    required: true,
  });
  pointer.setAttribute("aria-label", t("资源参数 JSON Pointer"));
  const values = Object.assign(document.createElement("textarea"), {
    value: (rule.allowed_values || []).join("\n"),
    placeholder: t("每行一个精确允许值"),
    required: true,
  });
  values.setAttribute("aria-label", t("允许值"));
  const remove = Object.assign(document.createElement("button"), {
    type: "button",
    textContent: "×",
    className: "icon-button",
  });
  remove.setAttribute("aria-label", t("删除资源条件"));
  remove.addEventListener("click", () => row.remove());
  row.append(pointer, values, remove);
  parent.append(row);
}

function groupInput(value) {
  const input = {};
  for (const key of [
    "id",
    "base_url",
    "enabled",
    "require_client_grant",
    "required_scopes",
    "tool_rules",
    "request_timeout",
    "max_response_body_bytes",
    "rate_limit",
  ])
    input[key] = value[key];
  input.headers = (value.headers || []).map(({ name }) => ({ name }));
  if (value.oauth) {
    input.oauth = {};
    for (const key of ["type", "issuer", "client_id", "scopes"])
      input.oauth[key] = value.oauth[key];
  }
  return input;
}

export function editToolPolicy(endpoint, record, tool, actions, saved) {
  const dialog = byId("policy-editor"),
    form = byId("policy-edit-form");
  form.reset();
  const existing =
    (record.tool_rules || []).find((rule) => rule.match === tool.name) || {};
  const approval = existing.approval;
  byId("policy-editor-title").textContent =
    `${t("编辑工具规则")} · ${tool.name}`;
  byId("policy-inherited").textContent = t(
    "此表单只修改当前工具的精确规则。继承的限制仍然生效，read 不能覆盖其他 write 或审批规则。",
  );
  byId("policy-publish-label").hidden = endpoint.kind !== "backend";
  byId("policy-published").checked = tool.published;
  byId("policy-effect").value = existing.effect || "";
  byId("policy-scopes").value = (existing.required_scopes || []).join(", ");
  byId("policy-resources").replaceChildren();
  for (const rule of existing.resource_rules || []) resourceRow(rule);
  byId("policy-add-resource").onclick = () => resourceRow();
  byId("policy-custom-approval").checked = !!approval;
  byId("policy-approval-fields").hidden = !approval;
  byId("policy-custom-approval").onchange = () => {
    byId("policy-approval-fields").hidden = !byId("policy-custom-approval")
      .checked;
  };
  byId("policy-action").value = approval?.action || "";
  byId("policy-quorum").value = approval?.required_approvals || 1;
  byId("policy-reviewers").value = [
    ...new Set((approval?.approvers || []).flatMap((grant) => grant.subjects)),
  ].join("\n");
  const complexReviewers = (approval?.approvers || []).some(
    (grant) =>
      grant.resources?.length ||
      grant.subjects.some((subject) => /[\r\n]/.test(subject)),
  );
  byId("policy-reviewers").disabled = complexReviewers;
  byId("policy-reviewer-note").hidden = !complexReviewers;
  byId("policy-different").checked = !!approval?.require_different_reviewer;
  const syncQuorum = () => {
    const quorum = Number(byId("policy-quorum").value);
    if (quorum > 1) byId("policy-different").checked = true;
    byId("policy-different").disabled = quorum > 1;
  };
  byId("policy-quorum").onchange = syncQuorum;
  syncQuorum();
  byId("policy-step-up").checked = !!approval?.require_step_up;
  byId("policy-edit-error").textContent = "";
  byId("policy-save").disabled = false;
  form.onsubmit = async (event) => {
    event.preventDefault();
    byId("policy-edit-error").textContent = "";
    try {
      if (
        (existing.resource_rules || []).some((rule) =>
          rule.allowed_values.some((value) => /[\r\n]/.test(value)),
        )
      )
        throw new Error(
          t("已有允许值包含换行，请使用服务的高级 JSON 编辑器。"),
        );
      const rule = {
        ...existing,
        match: tool.name,
        effect: byId("policy-effect").value,
        required_scopes: scopes(byId("policy-scopes").value),
        resource_rules: [],
      };
      for (const row of byId("policy-resources").children) {
        const values = [
          ...new Set(
            row
              .querySelector("textarea")
              .value.split("\n")
              .filter((value) => value !== ""),
          ),
        ];
        rule.resource_rules.push({
          argument: row.querySelector("input").value.trim(),
          allowed_values: values,
        });
      }
      if (byId("policy-custom-approval").checked) {
        if (rule.effect === "read")
          throw new Error(
            t(
              "只读标记不能同时配置审批。请选择写入，或明确移除本工具的审批策略。",
            ),
          );
        rule.approval = {
          ...approval,
          action: byId("policy-action").value,
          required_approvals: Number(byId("policy-quorum").value),
          require_different_reviewer: byId("policy-different").checked,
          require_step_up: byId("policy-step-up").checked,
        };
        if (!complexReviewers) {
          const subjects = [
            ...new Set(
              byId("policy-reviewers")
                .value.split("\n")
                .filter((value) => value !== ""),
            ),
          ];
          rule.approval.approvers = subjects.length ? [{ subjects }] : [];
        }
      } else delete rule.approval;
      const input =
        endpoint.kind === "backend"
          ? actions.backendInput(record, record.enabled)
          : groupInput(record);
      input.tool_rules = (record.tool_rules || []).filter(
        (value) => value.match !== tool.name,
      );
      if (
        rule.effect ||
        rule.required_scopes.length ||
        rule.resource_rules.length ||
        rule.approval
      )
        input.tool_rules.push(rule);
      if (endpoint.kind === "backend") {
        input.published_tools = (record.published_tools || []).filter(
          (name) => name !== tool.name,
        );
        if (byId("policy-published").checked)
          input.published_tools.push(tool.name);
      }
      byId("policy-save").disabled = true;
      await actions.api(
        `/${endpoint.kind === "backend" ? "backends" : "tool-groups"}/${encodeURIComponent(endpoint.id)}`,
        {
          method: "PUT",
          headers: { "If-Match": `"${record.revision}"` },
          body: JSON.stringify(input),
        },
      );
      dialog.close();
      await saved();
    } catch (error) {
      byId("policy-edit-error").textContent = error.message;
    } finally {
      byId("policy-save").disabled = false;
    }
  };
  dialog.showModal();
}
