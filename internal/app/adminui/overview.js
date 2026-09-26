import { getLocale, t } from "./i18n.js";
import { element } from "./tool-policies.js";

let rendered = "";

export function renderOverview(overview, backends, editBackend) {
  const snapshot = JSON.stringify([getLocale(), overview.unavailable, backends]);
  if (snapshot === rendered) return;
  rendered = snapshot;
  const list = document.getElementById("service-status-list");
  const rows = backends.map((value) => ({ value, state: value.runtime.state }));
  // Surface unavailable connections before healthy and disabled services.
  const priority = { unavailable: 0, ready: 1, disabled: 2 };
  rows.sort((a, b) => priority[a.state] - priority[b.state] || a.value.id.localeCompare(b.value.id));
  list.replaceChildren();
  for (const { value, state } of rows.slice(0, 3)) {
    const row = element("tr", "");
    const status = element("span", "", "state-pill");
    status.append(element("i", "", `status-dot ${state}`));
    status.append(document.createTextNode(t({ ready: "已连接", unavailable: "连接异常", disabled: "已停用" }[state])));
    const statusCell = element("td", "");
    statusCell.append(status);
    const action = element("button", "", "text-button");
    action.type = "button";
    action.setAttribute("aria-label", `${t("编辑")} ${value.id}`);
    const icon = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    icon.classList.add("icon");
    icon.setAttribute("aria-hidden", "true");
    const use = document.createElementNS(icon.namespaceURI, "use");
    use.setAttribute("href", "#i-chevron");
    icon.append(use);
    action.append(icon);
    action.onclick = () => editBackend(value);
    const actionCell = element("td", "");
    actionCell.append(action);
    row.append(element("td", value.id), element("td", t("MCP 后端")), statusCell, actionCell);
    list.append(row);
  }
  document.getElementById("overview-empty").hidden = rows.length !== 0;
  document.querySelector(".service-table-wrap").hidden = rows.length === 0;
  const summary = document.getElementById("service-summary");
  summary.classList.toggle("attention", overview.unavailable > 0);
  summary.textContent = overview.unavailable
    ? t("{count} 个 MCP 后端连接异常，请检查服务配置。", { count: overview.unavailable })
    : rows.length > 3
      ? t("优先展示异常服务，当前显示 3 / {count} 项。", { count: rows.length })
      : t("检查连接状态与已发布能力。");
}
