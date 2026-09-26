import { getLocale, t, translateDOM } from "./i18n.js";

const pages = {
  identities: ["用户与组织", "配置用户、部门和用户组的登录状态、角色与工具权限。"],
  "client-grants": ["客户端授权", "按用户、客户端和服务查看授权范围，检查权限或撤销授权。"],
  requests: ["请求诊断", "查看近期请求的耗时、拒绝原因和工具执行状态。"],
  "tool-policies": ["工具权限", "查看工具的发布状态、最终权限和审批要求，检查调用受阻的原因。"],
  approvals: ["审批中心", "核对写操作与配置变更。写操作批准后由客户端恢复，配置提案批准后应用。"],
  overview: ["概览", "集中管理服务接入、访问权限与调用治理。"],
  backends: ["MCP 后端", "连接已有的 MCP 服务，统一管理工具、资源和提示词。"],
  "tool-groups": ["HTTP 工具组", "把 REST API 转为 MCP 工具，在组内统一配置连接和权限。"],
  activity: ["变更记录", "追踪后端、工具组和接口的配置变更。"],
};

export function renderPage() {
  let page = Object.hasOwn(pages, location.hash.slice(1)) ? location.hash.slice(1) : "overview";
  if (document.body.dataset.canConfigure === "false") page = "approvals";
  else if (page === "approvals" && document.body.dataset.canApprove === "false") page = "overview";
  const [title, description] = pages[page];
  document.body.dataset.page = page;
  document.querySelector("#page-title").textContent = t(title);
  document.querySelector("#page-description").textContent = t(description);
  document.querySelector("#breadcrumb-page").textContent = t(title);
  document.querySelector("#breadcrumb-group").textContent = t(
    page === "overview" ? "总览" : ["backends", "tool-groups"].includes(page) ? "服务接入"
      : ["tool-policies", "identities", "client-grants"].includes(page) ? "访问控制" : "治理与审计",
  );
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
  for (const group of document.querySelectorAll(".nav-group")) {
    group.hidden = !group.querySelector(".nav-item:not([hidden])");
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
  const navigation = document.querySelector("#navigation-dialog");
  const sidebar = document.querySelector("#sidebar");
  const toggle = document.querySelector("#open-navigation");
  toggle.addEventListener("click", () => {
    navigation.append(sidebar);
    navigation.showModal();
    toggle.setAttribute("aria-expanded", "true");
  });
  navigation.addEventListener("close", () => {
    document.querySelector(".app-shell").prepend(sidebar);
    toggle.setAttribute("aria-expanded", "false");
  });
  navigation.addEventListener("click", (event) => {
    const bounds = navigation.getBoundingClientRect();
    if (event.target === navigation && (event.clientX > bounds.right || event.clientX < bounds.left)) navigation.close();
  });
  document.querySelector("#close-navigation").addEventListener("click", () => navigation.close());
  sidebar.addEventListener("click", (event) => {
    if (navigation.open && event.target.closest("a")) navigation.close();
  });
  matchMedia("(min-width: 761px)").addEventListener("change", (event) => {
    if (event.matches && navigation.open) navigation.close();
  });
  document.querySelector(".skip-link").addEventListener("click", (event) => {
    event.preventDefault();
    document.querySelector("#main-content").focus();
  });
  window.addEventListener("hashchange", () => {
    renderPage();
    window.scrollTo({ top: 0, behavior: "instant" });
    document.querySelector("#page-title").focus({ preventScroll: true });
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
