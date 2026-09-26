import { t } from "./i18n.js";
import { element } from "./tool-policies.js";
import { resourceRow } from "./tool-policy-editor.js";

const byId = (id) => document.getElementById(id);
const values = (raw) => [...new Set(raw.split(/[\s,]+/).filter(Boolean))];
let api,
  snapshot,
  generation = 0,
  page = 0;

export function initIdentities(request) {
  api = request;
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

export async function refreshIdentities() {
  const current = ++generation;
  byId("identity-feedback").textContent = t("正在读取配置…");
  try {
    const result = await api("/identities");
    if (generation !== current) return;
    snapshot = result;
    renderIdentities();
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
  const endpoint = field(box, "目标服务", "input", access.endpoint_id || "");
  endpoint.required = true;
  endpoint.setAttribute("list", "identity-endpoints");
  const tools = field(
    box,
    "允许的工具（每行一个原始名称）",
    "textarea",
    (access.tools || []).join("\n"),
  );
  const hint = element("p", "", "field-note");
  const updateHint = () => {
    hint.textContent =
      t("已发布工具") +
      ": " +
      (snapshot.endpoints
        ?.find((e) => e.id === endpoint.value)
        ?.tools.map((tool) => tool.name)
        .join(", ") || "—");
  };
  endpoint.oninput = updateHint;
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
  button(box, "删除此授权", () => box.remove());
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

export function renderIdentities() {
  if (!snapshot) return;
  const list = byId("identity-list");
  list.replaceChildren();
  const query = byId("identity-search").value.toLowerCase();
  const kind = byId("identity-kind").value;
  const items = (snapshot.identities || []).filter(
    (p) =>
      (!kind || p.kind === kind) &&
      `${p.name} ${p.external_id} ${p.id}`.toLowerCase().includes(query),
  );
  page = Math.max(0, Math.min(page, Math.ceil(items.length / 30) - 1));
  byId("identity-prev").disabled = page === 0;
  byId("identity-next").disabled = (page + 1) * 30 >= items.length;
  byId("identity-feedback").textContent = !snapshot.enabled
    ? t("SSO 未启用，请配置 auth.sso 后重启。")
    : `${snapshot.provider} · ${t(snapshot.directory_sync ? "目录快照同步" : "登录声明同步")} · ${t("第 {page} 页 · {count} 条记录", { page: page + 1, count: items.length })}`;
  if (snapshot.enabled && !items.length) {
    const empty = element("div", "", "empty-state");
    empty.append(element("h3", t("没有匹配的用户或组织")), element("p", t("调整筛选条件，或等待用户登录与目录同步。")));
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
    const avatar = element("span", (identity.name || identity.external_id).slice(0, 2).toUpperCase(), "identity-avatar");
    avatar.setAttribute("aria-hidden", "true");
    const labels = element("span", "");
    const kind = t({ user: "用户", group: "用户组", department: "部门" }[identity.kind]);
    labels.append(element("strong", identity.name || identity.external_id), element("small", `${kind} · ${identity.external_id}`));
    title.append(avatar, labels);
    const status = element("span", t(state), "policy-tag");
    status.dataset.tone = !identity.directory_active ? "danger" : identity.enabled ? "success" : "warning";
    header.append(title, status, element("span", t("编辑本地权限"), "identity-edit"));
    const body = element("div", "", "identity-detail");
    body.append(element("p", `MCPHub ID · ${identity.id}`, "identity-id"));
    card.append(header, body);
    if (identity.kind === "user") {
      const memberships = (identity.groups || [])
        .map((id) => snapshot.identities.find((p) => p.id === id))
        .filter(Boolean);
      body.append(
        element(
          "p",
          t("继承自") +
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
    account.append(element("legend", t("账户与角色")));
    const enabled = checkbox(account, "在 MCPHub 启用", identity.enabled);
    const roleInputs = [
      ["admin", "管理员"],
      ["approver", "审批员"],
      ["security_reviewer", "安全审批员"],
    ].map(([role, title]) => [
      role,
      checkbox(account, title, identity.permissions.roles?.includes(role)),
    ]);
    form.append(account);
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
    button(form, "添加服务授权", () => accessEntry(entries));
    const save = element("button", t("保存"), "primary");
    save.type = "submit";
    const feedback = element("p", "");
    feedback.setAttribute("role", "status");
    const actions = element("div", "", "identity-form-actions");
    actions.append(save, feedback);
    form.append(actions);
    form.onsubmit = async (event) => {
      event.preventDefault();
      save.disabled = true;
      feedback.textContent = "";
      try {
        await api(`/identities/${encodeURIComponent(identity.id)}`, {
          method: "PUT",
          headers: { "If-Match": `"${identity.revision}"` },
          body: JSON.stringify({
            enabled: enabled.checked,
            permissions: {
              roles: roleInputs
                .filter(([, input]) => input.checked)
                .map(([role]) => role),
              scopes: values(scopes.value),
              access: [...entries.children].map((entry) => entry.collect()),
            },
          }),
        });
        await refreshIdentities();
        byId("identity-feedback").textContent = `${t("权限已保存。")} ${byId("identity-feedback").textContent}`;
        byId("identity-list").querySelector(`[data-identity-id="${CSS.escape(identity.id)}"] > summary`)?.focus();
      } catch (error) {
        feedback.textContent = error.message;
      } finally {
        save.disabled = false;
      }
    };
    body.append(form);
    list.append(card);
  }
}
