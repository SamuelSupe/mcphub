import { t } from "./i18n.js";
import { element } from "./tool-policies.js";
import { resourceRow } from "./tool-policy-editor.js";
import { editAccountCredentials } from "./local-account.js";
import { markClean, confirmDiscard, lockForm } from "./unsaved.js";
import { renderToolSelection } from "./tool-selection.js";
import { scopeAssistant, showEffectivePermissions } from "./identity-permissions.js";

const byId = (id) => document.getElementById(id);
const values = (raw) => [...new Set(raw.split(/[\s,]+/).filter(Boolean))];
let api,
  snapshot,
  generation = 0,
  page = 0;
let renderedFilters = { query: "", kind: "", page: 0 };

const providerName = (id) => id === "mcphub:permissions" ? t("权限组") : snapshot?.providers?.find((source) => source.id === id)?.name || id;

export function initIdentities(request) {
  api = request;
  byId("create-local-user").onclick = () =>
    editAccountCredentials(async (input) => {
      await api("/identities", { method: "POST", body: JSON.stringify(input) });
      await refreshIdentities();
    });
  byId("create-group").onclick = showCreateGroup;
  for (const id of ["identity-search", "identity-kind"])
    byId(id).addEventListener("input", () => {
      page = 0;
      renderIdentities();
    });
  byId("identity-prev").onclick = () => {
    page--;
    renderIdentities();
  };
  byId("identity-next").onclick = () => {
    page++;
    renderIdentities();
  };
}

function showCreateGroup() {
  const dialog = element("dialog", "", "local-account-dialog");
  const form = element("form", "", "identity-form");
  form.append(element("h2", t("创建权限组")));
  const name = field(form, "组名称", "input");
  name.required = true;
  name.maxLength = 128;
  form.append(element("p", t("权限组独立于身份源，可直接添加用户，或映射 LDAP/OIDC 组织组。"), "field-note"));
  const feedback = element("p", "");
  feedback.setAttribute("role", "status");
  const actions = element("div", "", "identity-form-actions");
  button(actions, "取消", async () => { if (await confirmDiscard(form)) dialog.close(); });
  const save = element("button", t("创建权限组"), "primary");
  save.type = "submit";
  actions.append(save);
  form.append(feedback, actions);
  form.onsubmit = async (event) => {
    event.preventDefault();
    const input = { name: name.value.trim(), provider: "mcphub:permissions" };
    const unlock = lockForm(form);
    try {
      const created = await api("/identities/groups", {
        method: "POST",
        body: JSON.stringify(input),
      });
      markClean(form);
      unlock();
      dialog.close();
      byId("identity-search").value = created.name;
      byId("identity-kind").value = "permission_group";
      page = 0;
      await refreshIdentities();
      const record = byId("identity-list").querySelector(`[data-identity-id="${CSS.escape(created.id)}"]`);
      if (record) { record.open = true; record.querySelector("summary").focus(); record.scrollIntoView({ block: "nearest" }); }
    } catch (error) {
      feedback.textContent = error.message;
    } finally {
      unlock();
    }
  };
  dialog.append(form);
  dialog.addEventListener("close", () => dialog.remove(), { once: true });
  document.body.append(dialog);
  dialog.showModal();
  markClean(form);
  dialog.addEventListener("cancel", async (event) => { event.preventDefault(); if (await confirmDiscard(form)) dialog.close(); });
  name.focus();
}

export async function refreshIdentities() {
  if (!await confirmDiscard(byId("identity-list"))) return;
  const current = ++generation;
  byId("identity-feedback").textContent = t("正在读取配置…");
  try {
    const result = await api("/identities");
    if (generation !== current) return;
    snapshot = result;
    await renderIdentities();
  } catch (error) {
    if (generation !== current) return;
    snapshot = null;
    byId("identity-list").replaceChildren();
    byId("identity-feedback").textContent = error.message;
    byId("identity-prev").disabled = byId("identity-next").disabled = true;
  }
}

function field(parent, title, tag, value = "") {
  const label = element("label", t(title), "field");
  const input = element(tag, "");
  input.value = value;
  label.append(input);
  parent.append(label);
  return input;
}

function checkbox(parent, title, checked) {
  const label = element("label", "", "checkbox-label");
  const input = element("input", "");
  input.type = "checkbox";
  input.checked = !!checked;
  label.append(input, document.createTextNode(t(title)));
  parent.append(label);
  return input;
}

function button(parent, title, click) {
  const node = element("button", t(title), "secondary");
  node.type = "button";
  node.onclick = click;
  parent.append(node);
  return node;
}

function accessEntry(parent, access = {}) {
  const box = element("fieldset", "", "identity-access");
  box.append(element("legend", t("服务授权")));
  const endpoint = field(box, "目标服务", "select");
  endpoint.append(new Option(t("选择目标服务"), ""));
  for (const service of snapshot.endpoints || []) endpoint.append(new Option(service.id, service.id));
  if (access.endpoint_id && ![...endpoint.options].some((option) => option.value === access.endpoint_id)) endpoint.append(new Option(access.endpoint_id, access.endpoint_id));
  endpoint.value = access.endpoint_id || "";
  endpoint.required = true;
  const choices = element("div", "", "tool-options");
  box.append(element("p", t("允许的工具"), "field-note"), choices);
  const advanced = element("details", "", "advanced");
  advanced.append(element("summary", t("高级：手动填写工具名称")));
  box.append(advanced);
  const tools = field(
    advanced,
    "允许的工具（每行一个原始名称）",
    "textarea",
    (access.tools || []).join("\n"),
  );
  const hint = element("p", "", "field-note");
  const updateHint = () => {
    renderToolSelection(choices, tools, snapshot.endpoints?.find((e) => e.id === endpoint.value)?.tools || [], "此服务没有已发布工具。请先接入服务并发布工具。");
    hint.textContent =
      t("已发布工具") +
      ": " +
      (snapshot.endpoints
        ?.find((e) => e.id === endpoint.value)
        ?.tools.map((tool) => tool.name)
        .join(", ") || "—");
  };
  endpoint.onchange = () => {
    tools.value = "";
    updateHint();
  };
  tools.oninput = updateHint;
  updateHint();
  box.append(hint);
  const write = checkbox(
    box,
    "允许申请写操作（仍需逐次审批）",
    access.allow_write_requests,
  );
  const prompts = checkbox(box, "允许提示词", access.prompts);
  const resources = checkbox(box, "允许资源读取", access.resources);
  const subscriptions = checkbox(box, "允许资源订阅", access.subscriptions);
  const rules = element("div", "", "field");
  box.append(
    element("p", t("业务资源条件：同一条授权内必须全部满足。"), "field-note"),
    rules,
  );
  for (const rule of access.resource_rules || []) resourceRow(rule, rules);
  button(box, "添加资源条件", () => resourceRow({}, rules));
  button(box, "删除此授权", () => { box.remove(); parent.dispatchEvent(new Event("change", { bubbles: true })); });
  box.collect = () => ({
    endpoint_id: endpoint.value.trim(),
    tools: values(tools.value),
    allow_write_requests: write.checked,
    prompts: prompts.checked,
    resources: resources.checked,
    subscriptions: subscriptions.checked,
    resource_rules: [...rules.children].map((row) => ({
      argument: row.querySelector("input").value.trim(),
      allowed_values: row
        .querySelector("textarea")
        .value.split("\n")
        .map((v) => v.trim())
        .filter(Boolean),
    })),
  });
  parent.append(box);
}

export async function renderIdentities() {
  byId("create-local-user").hidden = !snapshot?.builtin;
  byId("create-group").hidden = !snapshot?.enabled;
  if (!snapshot) return;
  const list = byId("identity-list");
  if (!await confirmDiscard(list)) {
    byId("identity-search").value = renderedFilters.query;
    byId("identity-kind").value = renderedFilters.kind;
    page = renderedFilters.page;
    return;
  }
  list.replaceChildren();
  const query = byId("identity-search").value.toLowerCase();
  const kind = byId("identity-kind").value;
  const items = (snapshot.identities || []).filter(
    (p) =>
      (!kind || (kind === "permission_group" ? p.kind !== "user" && p.managed_locally : kind === "organization_group" ? p.kind !== "user" && !p.managed_locally : p.kind === kind)) &&
      `${p.name} ${p.external_id} ${p.id} ${providerName(p.provider)}`.toLowerCase().includes(query),
  );
  page = Math.max(0, Math.min(page, Math.ceil(items.length / 30) - 1));
  renderedFilters = { query: byId("identity-search").value, kind, page };
  byId("identity-prev").disabled = page === 0;
  byId("identity-next").disabled = (page + 1) * 30 >= items.length;
  byId("identity-feedback").textContent = !snapshot.enabled
    ? t("SSO 未启用，请配置 auth.sso 后重启。")
    : `${snapshot.providers?.length > 1 ? t("身份来源：{count} 个", { count: snapshot.providers.length }) : `${snapshot.provider} · ${t(snapshot.builtin ? "内建账号" : snapshot.directory_sync ? "目录快照同步" : "登录声明同步")}`} · ${t("第 {page} 页 · {count} 条记录", { page: page + 1, count: items.length })}`;
  if (snapshot.enabled && !items.length) {
    const empty = element("div", "", "empty-state");
    empty.append(
      element("h3", t("没有匹配的用户或组")),
      element("p", t("调整筛选条件，或等待用户登录与目录同步。")),
    );
    list.append(empty);
  }
  const datalist = byId("identity-endpoints");
  datalist.replaceChildren(
    ...(snapshot.endpoints || []).map((e) => new Option(e.id, e.id)),
  );
  for (const identity of items.slice(page * 30, page * 30 + 30)) {
    const card = element("details", "", "identity-record");
    card.dataset.identityId = identity.id;
    const header = element("summary", "", "identity-summary");
    const state = !identity.directory_active
      ? "目录已停用"
      : identity.enabled
        ? "已启用"
        : "待授权或已停用";
    const title = element("span", "", "identity-name");
    const avatar = element(
      "span",
      (identity.name || identity.external_id).slice(0, 2).toUpperCase(),
      "identity-avatar",
    );
    avatar.setAttribute("aria-hidden", "true");
    const labels = element("span", "");
    const kind = t(
      identity.kind === "user" ? "用户" : identity.managed_locally ? "权限组" : identity.kind === "department" ? "部门" : "组织组",
    );
    labels.append(
      element("strong", identity.name || identity.external_id),
      element(
        "small",
        identity.provider === "mcphub:permissions" ? kind : `${kind} · ${providerName(identity.provider)}`,
      ),
    );
    title.append(avatar, labels);
    const status = element("span", t(state), "policy-tag");
    status.dataset.tone = !identity.directory_active
      ? "danger"
      : identity.enabled
        ? "success"
        : "warning";
    header.append(
      title,
      status,
      element(
        "span",
        t(identity.kind === "user" ? "编辑组成员关系" : identity.managed_locally ? "编辑组权限" : "管理组织组状态"),
        "identity-edit",
      ),
    );
    const body = element("div", "", "identity-detail");
    const identifiers = element("details", "", "service-technical");
    identifiers.append(element("summary", t("技术详情")), element("p", `MCPHub ID · ${identity.id}`, "identity-id"), element("p", `${t("外部身份 ID")} · ${identity.external_id}`, "identity-id"));
    body.append(identifiers);
    body.append(
      element(
        "p",
        t(
          identity.kind !== "user"
            ? identity.managed_locally
              ? "管理员维护的组"
              : "身份源同步的组"
            : identity.provider === "mcphub:local"
              ? "MCPHub 账号"
              : "企业身份",
        ),
        "field-note",
      ),
    );
    if (identity.kind === "user" && identity.provider === "mcphub:local") {
      button(body, "重置密码", () =>
        editAccountCredentials(async (input) => {
          await api(`/identities/${encodeURIComponent(identity.id)}/password`, {
            method: "POST",
            body: JSON.stringify({ password: input.password }),
          });
          await refreshIdentities();
        }, identity.external_id),
      );
    }
    card.append(header, body);
    if (identity.kind === "user") {
      button(body, "查看有效权限", () => showEffectivePermissions(identity, snapshot, api));
      const memberships = (identity.groups || [])
        .map((id) => snapshot.identities.find((p) => p.id === id))
        .filter(Boolean);
      body.append(
        element(
          "p",
          t("所属组") +
            ": " +
            (memberships
              .map(
                (g) =>
                  `${g.name || g.external_id}${g.enabled && g.directory_active ? "" : ` (${t("已停用")})`}`,
              )
              .join(", ") || "—"),
        ),
      );
    }
    const form = element("form", "", "identity-form");
    const account = element("fieldset", "", "identity-account");
    account.append(
      element(
        "legend",
        t(identity.kind === "user" ? "账户状态" : identity.managed_locally ? "组状态与角色" : "组状态"),
      ),
    );
    const enabled = checkbox(account, "在 MCPHub 启用", identity.enabled);
    form.append(account);
    let collect, scopeHelp;
    if (identity.kind === "user") {
      const groups = element("fieldset", "", "identity-memberships");
      groups.append(element("legend", t("所属组")));
      const available = snapshot.identities.filter(
        (g) => g.kind !== "user" && (g.provider === identity.provider || g.provider === "mcphub:permissions"),
      );
      const inputs = available.map((g) => {
        const input = checkbox(
          groups,
          g.name || g.external_id,
          identity.groups?.includes(g.id),
        );
        input.disabled = !g.managed_locally;
        if (!g.managed_locally)
          input.parentElement.append(
            element("small", t("由企业身份源管理"), "field-note"),
          );
        if (!g.enabled || !g.directory_active)
          input.parentElement.append(
            element("small", t("已停用"), "field-note"),
          );
        return [g.id, input];
      });
      groups.append(
        element(
          "p",
          t(
            "用户权限由有效组的授权合并而来。请先创建组，在组上配置角色、Scope 和服务权限，再将用户加入组。",
          ),
          "field-note",
        ),
      );
      form.append(groups);
      collect = () => ({
        groups: inputs.filter(([, input]) => input.checked).map(([id]) => id),
      });
    } else if (!identity.managed_locally) {
      form.append(element("p", t("组织组由身份源同步。请在权限组中映射此组并配置访问权限。"), "field-note"));
      collect = () => ({ permissions: identity.permissions });
    } else {
      let mappingInputs = [];
      if (identity.provider === "mcphub:permissions") {
        const mappings = element("fieldset", "", "identity-memberships");
        mappings.append(element("legend", t("组织组映射")), element("p", t("任一有效组织组匹配时继承本权限组；撤销成员关系或停用组后立即失效。"), "field-note"));
        mappingInputs = snapshot.identities.filter((g) => g.kind !== "user" && !g.managed_locally).map((g) => [g.id, checkbox(mappings, `${providerName(g.provider)} · ${g.name || g.external_id}`, identity.source_groups?.includes(g.id))]);
        if (!mappingInputs.length) mappings.append(element("p", t("尚未发现组织组。企业用户登录或目录同步后，可在这里建立映射。"), "field-note"));
        form.append(mappings);
      }
      const roleInputs = [
        ["admin", "管理员"],
        ["approver", "审批员"],
        ["security_reviewer", "安全审批员"],
      ].map(([role, title]) => [
        role,
        checkbox(account, title, identity.permissions.roles?.includes(role)),
      ]);
      const scopes = field(
        form,
        "授权 Scope",
        "textarea",
        (identity.permissions.scopes || []).join("\n"),
      );
      form.append(
        element(
          "p",
          t("服务所需 Scope") + ": " + (snapshot.scopes?.join(", ") || "—"),
          "field-note",
        ),
      );
      const entries = element("div", "");
      form.append(entries);
      for (const access of identity.permissions.access || [])
        accessEntry(entries, access);
      button(form, "添加服务授权", () => { accessEntry(entries); scopeHelp.render(); });
      const members = snapshot.identities.filter(
        (p) => p.kind === "user" && p.groups?.includes(identity.id),
      );
      body.append(
        element(
          "p",
          t("直接成员") +
            ": " +
            (members.map((p) => p.name || p.external_id).join(", ") || "—"),
          "field-note",
        ),
      );
      collect = () => ({
        ...(identity.provider === "mcphub:permissions" ? { source_groups: mappingInputs.filter(([, input]) => input.checked).map(([id]) => id) } : {}),
        permissions: {
          scope_mode: scopeHelp?.mode() || identity.permissions.scope_mode || (identity.provider === "mcphub:permissions" ? "derived" : "explicit"),
          roles: roleInputs
            .filter(([, input]) => input.checked)
            .map(([role]) => role),
          scopes: values(scopes.value),
          access: [...entries.children].map((entry) => entry.collect()),
        },
      });
      scopeHelp = scopeAssistant(form, scopes, collect, snapshot);
    }
    const save = element("button", t("保存"), "primary");
    save.type = "submit";
    const feedback = element("p", "");
    feedback.setAttribute("role", "status");
    const actions = element("div", "", "identity-form-actions");
    actions.append(save, feedback);
    form.append(actions);
    form.onsubmit = async (event) => {
      event.preventDefault();
      if (!await confirmDiscard(list, form)) return;
      const input = { enabled: enabled.checked, ...collect() };
      if (input.enabled && scopeHelp?.missing().length) {
        feedback.textContent = t("请补齐所选工具所需 Scope，或取消对应工具授权，再保存启用的组。");
        scopesFocus(form);
        return;
      }
      const unlock = lockForm(form);
      feedback.textContent = "";
      try {
        await api(`/identities/${encodeURIComponent(identity.id)}`, {
          method: "PUT",
          headers: { "If-Match": `"${identity.revision}"` },
          body: JSON.stringify(input),
        });
        markClean(form);
        unlock();
        await refreshIdentities();
        byId("identity-feedback").textContent =
          `${t("组与账号设置已保存。")} ${byId("identity-feedback").textContent}`;
        byId("identity-list")
          .querySelector(
            `[data-identity-id="${CSS.escape(identity.id)}"] > summary`,
          )
          ?.focus();
      } catch (error) {
        feedback.textContent = error.message;
      } finally {
        unlock();
      }
    };
    body.append(form);
    list.append(card);
    markClean(form);
  }
}

function scopesFocus(form) {
  form.querySelector(".scope-assistant")?.scrollIntoView({ block: "center" });
  form.querySelector(".scope-assistant button")?.focus();
}
