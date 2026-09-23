import { getLocale, t, translateDOM } from "./i18n.js";

const pages = {
  overview: ["概览", "将分散的服务与 API，统一为可供 AI 调用的工具。"],
  backends: ["MCP 后端", "连接已有的 MCP 服务，统一管理工具、资源和提示词。"],
  "tool-groups": ["HTTP 工具组", "把 REST API 转为 MCP 工具，在组内统一配置连接和权限。"],
  activity: ["变更记录", "追踪后端、工具组和接口的配置变更。"],
};

export function renderPage() {
  const page = Object.hasOwn(pages, location.hash.slice(1)) ? location.hash.slice(1) : "overview";
  const [title, description] = pages[page];
  document.querySelector("#page-title").textContent = t(title);
  document.querySelector("#page-description").textContent = t(description);
  document.querySelector("#breadcrumb-page").textContent = t(title);
  document.title = `${t(title)} · MCPHub`;
  for (const section of document.querySelectorAll("[data-page]")) {
    section.hidden = section.dataset.page !== page;
  }
  for (const link of document.querySelectorAll(".nav-item")) {
    const selected = link.hash === `#${page}`;
    link.classList.toggle("active", selected);
    if (selected) link.setAttribute("aria-current", "page");
    else link.removeAttribute("aria-current");
  }
  document.querySelector("#register-button").hidden = page !== "backends";
  document.querySelector("#new-group-button").hidden = page !== "tool-groups";
}

export function renderSummary(overview) {
  const values = {
    "metric-total": overview.total,
    "metric-ready": overview.ready,
    "metric-groups": overview.tool_groups,
    "metric-tools": overview.http_tools,
    "nav-backend-count": overview.total,
    "nav-group-count": overview.tool_groups,
  };
  for (const [id, value] of Object.entries(values)) document.getElementById(id).textContent = value;
  document.querySelector("#onboarding-title").textContent = t(overview.total || overview.tool_groups ? "继续接入你的服务" : "从接入第一个服务开始");
}

let lastUpdated = null;
let refreshError = "";

export function updateRefreshState(error = null) {
  refreshError = error?.message || "";
  if (!error) lastUpdated = new Date();
  renderRefreshState();
  for (const list of document.querySelectorAll(".backend-list")) list.setAttribute("aria-busy", "false");
}

export function renderRefreshState() {
  const banner = document.querySelector("#page-error");
  banner.hidden = !refreshError;
  document.querySelector("#page-error-message").textContent = refreshError ? `${t("无法读取最新配置，显示内容可能已过期。")} ${refreshError}` : "";
  const status = document.querySelector("#sync-status");
  status.classList.toggle("sync-error", Boolean(refreshError));
  status.textContent = refreshError ? t("刷新失败") : lastUpdated
    ? `${t("更新于")} ${new Intl.DateTimeFormat(getLocale(), { hour: "2-digit", minute: "2-digit" }).format(lastUpdated)}`
    : t("正在读取配置…");
}

export function initShell({ refresh, addBackend, addGroup }) {
  renderPage();
  document.querySelector(".skip-link").addEventListener("click", (event) => {
    event.preventDefault();
    document.querySelector("#main-content").focus();
  });
  window.addEventListener("hashchange", () => {
    renderPage();
    window.scrollTo({ top: 0, behavior: "instant" });
  });
  for (const button of document.querySelectorAll("[data-create]")) {
    button.addEventListener("click", button.dataset.create === "backend" ? addBackend : addGroup);
  }
  for (const id of ["refresh-button", "retry-button"]) {
    document.getElementById(id).addEventListener("click", async () => {
      const button = document.getElementById(id);
      button.disabled = true;
      try { await refresh(); } finally { button.disabled = false; }
    });
  }
  for (const form of document.querySelectorAll("form")) {
    form.addEventListener("invalid", (event) => {
      for (let parent = event.target.parentElement; parent && parent !== form; parent = parent.parentElement) {
        if (parent instanceof HTMLDetailsElement) parent.open = true;
      }
    }, true);
  }
  translateDOM(document);
}
