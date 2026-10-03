import { t } from "./i18n.js";
import { element } from "./tool-policies.js";

export function missingToolScopes(permissions, endpoints, roleScopes = {}) {
  const granted = new Set([
    ...(permissions.scopes || []),
    ...(permissions.roles || []).flatMap((role) => roleScopes[role] || []),
  ]);
  const required = (permissions.access || []).flatMap((access) => {
    const endpoint = endpoints.find((e) => e.id === access.endpoint_id);
    return [...(access.prompts || access.resources || access.subscriptions ? endpoint?.required_scopes || [] : []), ...(endpoint?.tools || [])
      .filter((tool) => access.tools?.includes(tool.name))
      .flatMap((tool) => tool.required_scopes || [])];
  });
  return [...new Set(required)].filter((scope) => !granted.has(scope)).sort();
}

export function scopeAssistant(form, scopes, collect, snapshot) {
	const modeLabel = element("label", t("Scope 配置方式"), "field");
	const mode = element("select", "");
	mode.append(new Option(t("按访问意图生成"), "derived"), new Option(t("高级：显式 Scope"), "explicit"));
	mode.value = collect().permissions.scope_mode || "explicit";
	modeLabel.append(mode); scopes.parentElement.before(modeLabel);
  const box = element("div", "", "scope-assistant");
  const note = element("p", "", "field-note");
  note.setAttribute("role", "status");
  const add = element("button", t("补齐所选工具所需 Scope"), "secondary");
  add.type = "button";
  const missing = () => missingToolScopes(collect().permissions, snapshot.endpoints || [], snapshot.role_scopes);
  const render = () => {
    scopes.readOnly = mode.value === "derived";
    if (scopes.readOnly) {
      const permissions = collect().permissions;
      const required = missingToolScopes({ ...permissions, scopes: [], roles: [] }, snapshot.endpoints || []);
      scopes.value = [...new Set(required)].sort().join("\n");
    }
    const values = missing();
    const access = collect().permissions.access || [];
    const selected = access.some((entry) => entry.tools?.length || entry.prompts || entry.resources || entry.subscriptions);
    note.textContent = !selected ? t("此组还未授予工具或资源访问。通过「添加服务授权」选择服务和能力。") : values.length
      ? t("所选工具缺少 Scope：{scopes}", { scopes: values.join(", ") })
      : t("所选工具的 Scope 已齐全。资源条件、写操作审批和客户端授权仍需单独满足。");
    add.hidden = !values.length;
  };
  add.onclick = () => {
    scopes.value = [...new Set([...scopes.value.split(/[\s,]+/).filter(Boolean), ...missing()])].join("\n");
    render();
  };
  mode.onchange = render;
  form.addEventListener("input", render);
  form.addEventListener("change", render);
  box.append(note, add);
  scopes.parentElement.after(box);
  render();
  return { render, missing, mode: () => mode.value };
}

export async function showEffectivePermissions(identity, snapshot, api) {
  const dialog = element("dialog", "", "local-account-dialog effective-permissions");
  dialog.append(element("h2", t("有效权限")), element("p", identity.name || identity.external_id));
  const content = element("div", "", "identity-form");
  content.setAttribute("aria-live", "polite");
  content.textContent = t("正在读取配置…");
  const close = element("button", t("关闭"), "secondary");
  close.type = "button";
  close.onclick = () => dialog.close();
  dialog.append(content, close);
  dialog.addEventListener("close", () => dialog.remove(), { once: true });
  document.body.append(dialog);
  dialog.showModal();
  try {
    const result = await api(`/identities/${encodeURIComponent(identity.id)}/effective`);
    content.replaceChildren();
    content.append(element("p", t("基于已保存配置与当前组状态；未保存的修改不计入。实际调用还受 Token、客户端授权、资源条件与审批约束。"), "field-note"));
    if (!result.active) {
      content.append(element("p", t(result.membership_stale ? "企业组验证已过期，请重新登录或刷新目录快照。" : "此用户当前未启用或目录状态无效，没有有效权限。")));
      return;
    }
    const permissions = result.permissions;
    content.append(element("p", `${t("有效 Scope")} · ${permissions.scopes?.join(", ") || "—"}`));
    content.append(element("p", `${t("角色")} · ${permissions.roles?.join(", ") || "—"}`));
    if (result.verified_at && !result.verified_at.startsWith("0001")) content.append(element("p", `${t("身份最后验证时间")} · ${new Date(result.verified_at).toLocaleString()}`, "field-note"));
    for (const source of result.groups || []) {
      const group = source.group;
      const names = (source.matched_groups || []).map((id) => snapshot.identities.find((g) => g.id === id)?.name || id).join(", ");
      content.append(element("p", `${group.name || group.external_id} · ${t({ mapping: "组织组映射", direct: "直接成员关系", organization: "身份源组织组" }[source.origin])}${names ? ` · ${names}` : ""}`, "field-note"));
    }
    for (const access of permissions.access || []) {
      const box = element("section", "", "overview-panel");
      box.append(element("h3", access.endpoint_id));
      box.append(element("p", `${t("允许的工具")} · ${access.tools?.join(", ") || "—"}`));
      const capabilities = [
        [access.allow_write_requests, "允许申请写操作（仍需逐次审批）"],
        [access.prompts, "允许提示词"], [access.resources, "允许资源读取"],
        [access.subscriptions && access.resources, "允许资源订阅"],
      ].filter(([allowed]) => allowed).map(([, label]) => t(label));
      if (capabilities.length) box.append(element("p", capabilities.join(" · ")));
      for (const rule of access.resource_rules || []) box.append(element("p", `${rule.argument} ∈ ${rule.allowed_values.join(", ")}`, "identity-id"));
      content.append(box);
    }
    const missing = missingToolScopes(permissions, snapshot.endpoints || []);
    if (missing.length) content.append(element("p", t("所选工具缺少 Scope：{scopes}", { scopes: missing.join(", ") }), "attention"));
  } catch (error) {
    content.textContent = error.message;
  }
}
