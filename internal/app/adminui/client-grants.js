import { t, getLocale } from "./i18n.js";
import { element } from "./tool-policies.js";
import { openAccessCheck } from "./access-check.js";
import { showClientRequests } from "./request-diagnostics.js";

const byId = (id) => document.getElementById(id);
const statuses = {
  active: "授权有效",
  pending: "等待用户确认",
  confirmed: "等待客户端接收",
  expired: "已过期",
  revoked: "已撤销",
  denied: "已拒绝",
  reconfirmation_required: "需要重新确认",
};
let api,
  snapshot,
  generation = 0,
  cursors = [""],
  filters = new URLSearchParams();

export function initClientGrants(request) {
  api = request;
  byId("grant-filters").addEventListener("submit", (event) => {
    event.preventDefault();
    filters = new URLSearchParams(new FormData(event.target));
    cursors = [""];
    refreshClientGrants();
  });
  byId("grant-reset").addEventListener("click", () => {
    byId("grant-filters").reset();
    filters = new URLSearchParams();
    cursors = [""];
    refreshClientGrants();
  });
  byId("grant-prev").addEventListener("click", () => {
    if (cursors.length > 1) cursors.pop();
    refreshClientGrants();
  });
  byId("grant-next").addEventListener("click", () => {
    if (snapshot?.next_cursor) cursors.push(snapshot.next_cursor);
    refreshClientGrants();
  });
}

export async function refreshClientGrants() {
  const current = ++generation;
  const query = new URLSearchParams(filters);
  query.set("cursor", cursors.at(-1));
  byId("grant-feedback").textContent = t("正在读取授权…");
  byId("grant-list").setAttribute("aria-busy", "true");
  byId("grant-prev").disabled = byId("grant-next").disabled = true;
  try {
    const result = await api(`/client-grants?${query}`);
    if (current !== generation) return;
    snapshot = result;
    renderClientGrants();
  } catch (error) {
    if (current !== generation) return;
    snapshot = null;
    byId("grant-list").replaceChildren();
    byId("grant-feedback").textContent = error.message;
    byId("grant-prev").disabled = cursors.length < 2;
  } finally {
    if (current === generation)
      byId("grant-list").setAttribute("aria-busy", "false");
  }
}

export function renderClientGrants() {
  if (!snapshot) return;
  const list = byId("grant-list");
  list.replaceChildren();
  byId("grant-feedback").textContent = !snapshot.enabled
    ? t("客户端授权服务未启用，请先配置 client_authorization。")
    : snapshot.grants.length
      ? t("第 {page} 页 · {count} 条授权", {
          page: cursors.length,
          count: snapshot.grants.length,
        })
      : t("没有符合条件的授权");
  byId("grant-prev").disabled = cursors.length < 2;
  byId("grant-next").disabled = !snapshot.next_cursor;
  for (const grant of snapshot.grants) {
    const card = element("article", "", "policy-card");
    const header = element("header", "");
    const status = element("span", t(statuses[grant.status] || grant.status), "policy-tag");
    status.dataset.tone = grant.status === "active" ? "success" : ["pending", "confirmed", "reconfirmation_required"].includes(grant.status) ? "warning" : "neutral";
    header.append(
      element("h3", grant.client_name),
      status,
    );
    const fields = element("dl", "");
    const rows = [
      ["用户 Subject", grant.subject],
      ["目标服务", grant.endpoint_id],
      ["客户端 ID", grant.client_instance_id],
      ["Grant ID", grant.grant_id],
      ["授权截止", new Date(grant.expires_at).toLocaleString(getLocale())],
      ["授权 Scope", grant.allowed_scopes?.join(", ") || "—"],
      ["允许的工具", grant.allowed_tools?.join(", ") || "—"],
      [
        "允许申请写操作",
        t(grant.allow_write_requests ? "是，仍需逐次审批" : "否"),
      ],
      ["授权版本", String(grant.grant_revision)],
      [
        "协议能力",
        Object.entries(grant.capabilities || {})
          .filter(([, allowed]) => allowed)
          .map(([name]) => name)
          .join(", ") || "—",
      ],
    ];
    for (const [label, value] of rows)
      fields.append(element("dt", t(label)), element("dd", value));
    const details = element("details", "", "grant-details");
    details.append(element("summary", t("查看完整授权范围")), fields);
    card.append(header, element("p", `${grant.endpoint_id} · ${grant.subject}`, "record-subtitle"), details);
    if (grant.resource_rules?.length) {
      const details = element("details", "");
      details.append(
        element("summary", t("客户端资源范围")),
        element("pre", JSON.stringify(grant.resource_rules, null, 2)),
      );
      card.append(details);
    }
    const actions = element("div", "", "policy-card-actions");
    const requests = element("button", t("查看请求"), "secondary");
    requests.type = "button";
    requests.onclick = () => showClientRequests(grant);
    actions.append(requests);
    if (grant.allowed_tools?.length) {
      const tools = document.createElement("select");
      tools.setAttribute("aria-label", t("选择工具检查权限"));
      for (const name of grant.allowed_tools) tools.add(new Option(name, name));
      const check = element("button", t("权限检查"), "secondary");
      check.type = "button";
      check.onclick = () =>
        openAccessCheck(
          { id: grant.endpoint_id },
          { name: tools.value },
          api,
          grant,
        );
      actions.append(tools, check);
    }
    const feedback = element("p", "");
    feedback.setAttribute("role", "status");
    if (
      ["active", "confirmed", "pending", "reconfirmation_required"].includes(
        grant.status,
      )
    ) {
      const revoke = element("button", t("撤销授权"), "secondary");
      revoke.type = "button";
      revoke.onclick = () => {
        const confirmation = element("div", "", "grant-confirmation");
        confirmation.append(
          element(
            "p",
            t(
              "撤销 {name} 的授权？后续请求将被拒绝，已开始的写操作可能继续执行。",
              { name: grant.client_name },
            ),
          ),
        );
        const confirm = element("button", t("确认撤销"), "danger-button");
        const cancel = element("button", t("取消"), "secondary");
        confirm.type = cancel.type = "button";
        cancel.onclick = () => {
          confirmation.remove();
          revoke.disabled = false;
        };
        confirm.onclick = async () => {
          confirm.disabled = cancel.disabled = true;
          try {
            await api(
              `/client-grants/${encodeURIComponent(grant.grant_id)}/revoke`,
              {
                method: "POST",
                body: JSON.stringify({ subject: grant.subject }),
              },
            );
            await refreshClientGrants();
          } catch (error) {
            feedback.textContent = error.message;
            confirm.disabled = cancel.disabled = false;
          }
        };
        revoke.disabled = true;
        confirmation.append(confirm, cancel);
        card.append(confirmation);
        confirm.focus();
      };
      actions.append(revoke);
    }
    card.append(actions, feedback);
    list.append(card);
  }
}
