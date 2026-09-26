import { t, translateDOM } from "./i18n.js";

let session = null;
let ready = () => {};

export function isAuthenticated() { return session?.authenticated === true; }
export function canManage() { return isAuthenticated() && (session.mode === "local" || session.can_configure === true); }
export function canInspectDelivery() { return isAuthenticated() && (session.can_configure || session.can_review_configuration); }
export function canApprove() { return isAuthenticated() && session.can_view_approvals === true; }
export function authHeaders() { return session?.csrf ? { "X-MCPHub-CSRF": session.csrf } : {}; }

export function renderManagementMode() {
  if (!session) return;
  const remote = session.mode === "remote";
  document.querySelector("#management-mode").textContent = t(remote ? "远程管理" : "本地管理");
  document.querySelector("#management-boundary").textContent = t(remote ? (canManage() ? "需要管理员权限" : "审批人访问") : "仅在本机访问");
}

export function requireLogin(status = 401) {
  session = null;
  document.title = `${t("管理员登录")} · MCPHub`;
  document.querySelectorAll("dialog[open]").forEach((dialog) => dialog.close());
  document.querySelectorAll("form").forEach((form) => form.reset());
  document.querySelector("#inspector").hidden = true;
  document.querySelector("#scrim").hidden = true;
  document.querySelector(".app-shell").hidden = true;
  document.querySelector(".app-shell").inert = false;
  document.body.style.overflow = "";
  document.querySelector("#auth-panel").hidden = false;
  document.querySelector("#auth-message").textContent = t(status === 403 ? "当前账号没有管理员权限，请联系身份服务管理员授权。" : "请使用具有管理员权限的账号登录。" );
  document.querySelector("#admin-login").hidden = false;
  translateDOM(document.querySelector("#auth-panel"));
}

async function loadSession() {
  try {
    const response = await fetch("/auth/session", { cache: "no-store" });
    if (!response.ok) throw new Error(t("暂时无法检查登录状态，请稍后重试。"));
    session = await response.json();
    if (!isAuthenticated()) {
      const reason = new URLSearchParams(location.search).get("login_error");
      requireLogin(session.forbidden || reason === "forbidden" ? 403 : 401);
      if (reason && reason !== "forbidden") document.querySelector("#auth-message").textContent = t("登录未完成。请重试，或检查身份服务的客户端配置。" );
      if (reason) history.replaceState(null, "", location.pathname + location.hash);
      return;
    }
    document.querySelector("#auth-panel").hidden = true;
    document.querySelector(".app-shell").hidden = false;
    document.body.dataset.canConfigure = String(canManage());
    document.body.dataset.canApprove = String(canApprove());
    for (const link of document.querySelectorAll(".nav-item")) link.hidden = link.hash === "#approvals" ? !canApprove() : !canManage();
    if (!canManage()) location.hash = "approvals";
    else if (!canApprove() && location.hash === "#approvals") location.hash = "overview";
    const remote = session.mode === "remote";
    document.querySelector("#admin-identity").hidden = !remote;
    document.querySelector("#admin-subject").textContent = session.subject;
    document.querySelector("#admin-subject").title = session.subject;
    renderManagementMode();
    await ready();
  } catch (error) {
    requireLogin();
    document.querySelector("#auth-message").textContent = error.message;
  }
}

export function initializeAuth(onReady) {
  ready = onReady;
  document.querySelector("#auth-retry").addEventListener("click", loadSession);
  document.querySelector("#admin-logout").addEventListener("click", async () => {
    try {
      const response = await fetch("/auth/logout", { method: "POST", headers: authHeaders() });
      if (!response.ok && response.status !== 401) throw new Error(t("退出失败，请重试。"));
      location.assign("/");
    } catch (error) {
      document.querySelector("#page-error").hidden = false;
      document.querySelector("#page-error-message").textContent = error.message;
    }
  });
  return loadSession();
}
