import { t } from "./i18n.js";
import { editToolPolicy } from "./tool-policy-editor.js";
import { openAccessCheck } from "./access-check.js";

const byId = (id) => document.getElementById(id);
let actions,
  endpoints = [],
  snapshot = null,
  details = null,
  generation = 0;

export function element(tag, text, className) {
  const node = document.createElement(tag);
  node.textContent = text;
  if (className) node.className = className;
  return node;
}

export function showToolPolicies(endpoint, tool) {
  byId("policy-endpoint").value = endpoint;
  byId("policy-search").value = tool || "";
  byId("policy-filter").value = "all";
  if (location.hash === "#tool-policies") loadPolicies();
  else location.hash = "tool-policies";
}

export function initToolPolicies(options) {
  actions = options;
  byId("policy-endpoint").addEventListener("change", loadPolicies);
  byId("policy-reload").addEventListener("click", loadPolicies);
  byId("policy-search").addEventListener("input", renderToolPolicies);
  byId("policy-filter").addEventListener("change", renderToolPolicies);
  for (const button of document.querySelectorAll("[data-policy-close]")) {
    button.setAttribute("aria-label", t("关闭"));
    button.addEventListener("click", () => button.closest("dialog").close());
  }
  window.addEventListener("hashchange", () => {
    if (location.hash === "#tool-policies") loadPolicies();
  });
}

export function updateToolEndpoints(backends, groups) {
  endpoints = [
    ...backends.map((value) => ({ kind: "backend", ...value })),
    ...groups.map((value) => ({ kind: "tool-group", ...value })),
  ];
  const select = byId("policy-endpoint"),
    previous = select.value;
  select.replaceChildren(new Option(t("选择 endpoint"), ""));
  for (const value of endpoints)
    select.add(
      new Option(
        `${value.id} · ${t(value.kind === "backend" ? "MCP 后端" : "HTTP 工具组")}`,
        value.id,
      ),
    );
  select.value = endpoints.some((value) => value.id === previous)
    ? previous
    : "";
  const current = endpoints.find((value) => value.id === select.value);
  if (!current && snapshot) {
    generation++;
    snapshot = details = null;
    renderToolPolicies();
  }
  if (
    location.hash === "#tool-policies" &&
    current &&
    (!snapshot ||
      snapshot.revision !== current.revision ||
      snapshot.enabled !== current.enabled)
  )
    loadPolicies();
}

async function loadPolicies() {
  const seq = ++generation,
    id = byId("policy-endpoint").value;
  const selected = endpoints.find((value) => value.id === id);
  snapshot = details = null;
  renderToolPolicies();
  if (!selected) return;
  byId("policy-feedback").textContent = t("正在读取工具权限…");
  try {
    const [policy, record] = await Promise.all([
      actions.api(`/tool-policies?endpoint=${encodeURIComponent(id)}`),
      actions.api(
        `/${selected.kind === "backend" ? "backends" : "tool-groups"}/${encodeURIComponent(id)}`,
      ),
    ]);
    if (seq !== generation) return;
    if (policy.revision !== record.revision)
      throw new Error(t("配置在读取期间发生变化，请刷新工具。"));
    snapshot = policy;
    details = record;
    renderToolPolicies();
  } catch (error) {
    if (seq === generation) byId("policy-feedback").textContent = error.message;
  }
}

export function renderToolPolicies() {
  for (const option of byId("policy-endpoint").options) {
    const endpoint = endpoints.find((value) => value.id === option.value);
    option.textContent = endpoint
      ? `${endpoint.id} · ${t(endpoint.kind === "backend" ? "MCP 后端" : "HTTP 工具组")}`
      : t("选择 endpoint");
  }
  const list = byId("policy-list");
  list.replaceChildren();
  if (!snapshot) {
    byId("policy-feedback").textContent =
      t("选择目标服务，查看工具的最终权限。");
    return;
  }
  const query = byId("policy-search").value.toLowerCase(),
    filter = byId("policy-filter").value;
  const tools = snapshot.tools.filter(
    (tool) =>
      `${tool.name} ${tool.description}`.toLowerCase().includes(query) &&
      (filter === "all" ||
        (filter === "unpublished" ? !tool.published : tool.effect === filter)),
  );
  const state = !snapshot.enabled
    ? "已停用"
    : snapshot.kind === "tool-group"
      ? "已启用"
      : snapshot.ready
        ? "已连接"
        : "连接异常";
  byId("policy-feedback").textContent =
    `${snapshot.id} · ${t("版本")} ${snapshot.revision} · ${t("{count} 个工具", { count: tools.length })} · ${t(state)}`;
  if (!tools.length)
    list.append(
      element(
        "p",
        t("没有匹配的工具。检查筛选条件，或在服务详情中测试连接。"),
        "event-empty",
      ),
    );
  const endpoint = snapshot,
    record = details;
  for (const tool of tools) {
    const card = element("article", "", "policy-card"),
      header = element("header", "");
    header.append(
      element("h3", tool.name),
      element("span", t(tool.published ? "已发布" : "未发布"), "policy-tag"),
      element(
        "span",
        t(
          { read: "只读", write: "写入", unknown: "未分类（需要审批）" }[
            tool.effect
          ],
        ),
        `policy-tag ${tool.effect}`,
      ),
    );
    if (!tool.available)
      header.append(element("span", t("当前目录中不可用"), "policy-tag"));
    card.append(header);
    if (tool.description) card.append(element("p", tool.description));
    const facts = element("dl", "");
    const resourceText = (tool.resource_rules || [])
      .map(
        (rule) => `${rule.argument} ∈ ${JSON.stringify(rule.allowed_values)}`,
      )
      .join("\n");
    const approvals = tool.approval_policies || [];
    for (const [label, value] of [
      [
        "最终 Scope",
        tool.required_scopes?.join(", ") || t("未追加 Scope 限制"),
      ],
      ["资源范围", resourceText || t("未追加参数资源限制")],
      [
        "审批要求",
        tool.effect === "read"
          ? t("无需写审批")
          : t("至少 {count} 人审批", {
              count: Math.max(
                1,
                ...approvals.map((p) => p.required_approvals || 1),
              ),
            }) +
            (approvals.some((p) => p.require_step_up)
              ? ` · ${t("加强身份验证")}`
              : ""),
      ],
      [
        "客户端授权",
        t(
          endpoint.require_client_grant ? "必须携带有效 Grant" : "兼容普通连接",
        ),
      ],
    ])
      facts.append(element("dt", t(label)), element("dd", value));
    card.append(facts);
    const matched = element("details", "");
    matched.append(
      element("summary", t("匹配规则与完整审批策略")),
      element("pre", JSON.stringify(tool.matching_rules || [], null, 2)),
    );
    card.append(matched);
    const buttons = element("div", "", "policy-card-actions");
    const button = (label, run) => {
      const b = element("button", t(label), "secondary");
      b.type = "button";
      b.addEventListener("click", run);
      buttons.append(b);
    };
    button("编辑工具规则", () =>
      editToolPolicy(endpoint, record, tool, actions, async () => {
        await actions.refresh();
        await loadPolicies();
      }),
    );
    button("权限检查", () => openAccessCheck(endpoint, tool, actions.api));
    button("服务与发布设置", () => actions.editEndpoint(endpoint.kind, record));
    card.append(buttons);
    list.append(card);
  }
}
