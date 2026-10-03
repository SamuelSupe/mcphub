const messages = {
  zh: {
    eyebrow: "个人授权中心",
    title: "管理账号与客户端授权",
    intro:
      "连接服务账号，确认每个客户端可访问的工具与数据。所有授权都可以随时撤销。",
    login: "登录以查看并确认",
    logout: "退出网页会话",
    loading: "正在读取授权…",
    empty: "尚无有效连接。在 Agent 终端运行 mcpbridge pair start，或在本机运行 mcpbridge login --native 发起授权。",
    request: "确认客户端授权",
    pair: "请与终端显示的配对码核对",
    confirm: "确认授权",
    deny: "拒绝",
    revoke: "撤销此服务授权",
    session: "连接 ID",
    endSession: "撤销整个连接",
    done: "已保存。返回终端或 MCP 客户端继续。",
    serviceRevoked: "已撤销此服务授权。该连接的其他服务保持有效。",
    connectionRevoked: "已撤销整个连接。需要使用时，请在客户端重新发起授权。",
    requestDenied: "已拒绝此次授权请求。客户端未获得访问权限。",
    mine: "我的连接",
    portalLabel: "个人授权中心",
    history: "历史授权",
    technical: "技术详情",
    services: "个服务",
    service: "个服务",
    revokeService: "仅撤销此服务的授权？该连接的其他服务仍可使用。",
    revokeConnection: "撤销此连接的全部服务授权？已执行的操作不会撤回。",
    cancel: "取消",
    confirmRevoke: "确认撤销",
    toolsCapability: "工具调用",
    promptsCapability: "提示词",
    resourcesCapability: "读取资源",
    subscriptionsCapability: "资源订阅",
    owner: "当前用户",
    client: "客户端",
    endpoint: "目标",
    scopes: "Scope",
    tools: "允许的工具",
    resources: "业务资源条件",
    capabilities: "能力",
    expires: "到期时间",
    status: "状态",
    pairing: "配对码",
    write: "可申请写入；每次具体写入仍需独立审批。",
    read: "只读授权。不能申请写操作。",
    footer:
      "本页只管理你自己的客户端授权。客户端名称不证明应用身份；同一系统账户下的恶意程序仍可能冒用本机入口。",
    error: "请求未完成，请刷新或重新登录。",
    invalidRequest: "此授权链接已失效或不属于当前账号。请在客户端重新发起授权；已有授权仍可管理。",
    retryRequest: "重试读取授权请求",
    accountAccessRequired: "身份已记录，账号尚未获准访问或已被停用。请联系 MCPHub 管理员授权后重新登录。",
    pending: "待确认",
    confirmed: "已确认，等待终端领取",
    active: "有效",
    revoked: "已撤销",
    expired: "已到期",
    denied: "已拒绝",
    reconfirmation_required: "需要重新确认",
  },
  en: {
    eyebrow: "Personal authorization",
    title: "Manage accounts and client access",
    intro:
      "Connect service accounts and choose the tools and data each client may access. Revoke access at any time.",
    login: "Sign in to review",
    logout: "Sign out of portal",
    loading: "Loading authorizations…",
    empty:
      "No active connections. Run mcpbridge pair start in your Agent terminal, or mcpbridge login --native on your computer.",
    request: "Review client authorization",
    pair: "Compare this pairing code with your terminal",
    confirm: "Authorize client",
    deny: "Deny",
    revoke: "Revoke service access",
    session: "Connection ID",
    endSession: "Revoke connection",
    done: "Saved. Return to your terminal or MCP client to continue.",
    serviceRevoked: "Service access revoked. Other services in this connection remain available.",
    connectionRevoked: "Connection revoked. Start authorization again in your client when needed.",
    requestDenied: "Authorization request denied. The client has not been granted access.",
    mine: "Your connections",
    portalLabel: "Personal authorization",
    history: "Authorization history",
    technical: "Technical details",
    services: "services",
    service: "service",
    revokeService: "Revoke access to this service? Other services in this connection stay available.",
    revokeConnection: "Revoke every service in this connection? Completed operations cannot be undone.",
    cancel: "Cancel",
    confirmRevoke: "Confirm revocation",
    toolsCapability: "Tool calls",
    promptsCapability: "Prompts",
    resourcesCapability: "Read resources",
    subscriptionsCapability: "Resource subscriptions",
    owner: "Signed in as",
    client: "Client",
    endpoint: "Endpoint",
    scopes: "Scopes",
    tools: "Allowed tools",
    resources: "Business resource rules",
    capabilities: "Capabilities",
    expires: "Expires",
    status: "Status",
    pairing: "Pairing code",
    write:
      "May request writes. Every individual write still requires independent approval.",
    read: "Read-only access. Cannot request writes.",
    footer:
      "This portal manages only your own client access. Client names do not authenticate applications; malicious processes under the same OS account may impersonate a local entry.",
    error: "Request could not be completed. Refresh or sign in again.",
    invalidRequest: "This authorization link is no longer available or belongs to another account. Start authorization again in your client. You can still manage existing grants.",
    retryRequest: "Retry authorization request",
    accountAccessRequired: "Your identity was recorded, but access is pending or disabled. Ask your MCPHub administrator for access, then sign in again.",
    pending: "Awaiting consent",
    confirmed: "Confirmed; waiting for terminal",
    active: "Active",
    revoked: "Revoked",
    expired: "Expired",
    denied: "Denied",
    reconfirmation_required: "Reconfirmation required",
  },
};
let language =
    localStorage.getItem("mcphub-client-language") ||
    (navigator.language.startsWith("zh") ? "zh" : "en"),
  session;
const byId = (id) => document.getElementById(id),
  t = (key) => messages[language][key] || accountMessages[language][key] || key;
const requestId = new URLSearchParams(location.search).get("request");
if (requestId && /^gr_[A-Za-z0-9_-]+$/.test(requestId))
  sessionStorage.setItem("mcphub-client-request", requestId);
function element(tag, text, className) {
  const node = document.createElement(tag);
  if (text !== undefined) node.textContent = text;
  if (className) node.className = className;
  return node;
}
async function api(path, method = "GET", body) {
  const response = await fetch("/client-auth/" + path, {
    method,
    credentials: "same-origin",
    headers: method === "GET" ? {} : { "X-MCPHub-CSRF": session?.csrf || "", ...(body ? {"Content-Type":"application/json"} : {}) },
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!response.ok) {
    let code = "";
    try {
      code = (await response.json()).error?.code || "";
    } catch {}
    const error = new Error(code && t(code) !== code ? t(code) : t("error"));
    error.code = code;
    throw error;
  }
  return response.status === 204 ? null : response.json();
}
function action(label, fn, kind = "") {
  const button = element("button", label, kind);
  button.addEventListener("click", async () => {
    button.disabled = true;
    try {
      await fn();
    } catch (error) {
      byId("message").textContent = error.message;
    } finally {
      button.disabled = false;
    }
  });
  return button;
}
function confirmRevocation(message, heading = t("revoke")) {
  return new Promise((resolve) => {
    const dialog = element("dialog", undefined, "local-account-dialog");
    dialog.setAttribute("aria-labelledby", "revoke-title");
    const title = element("h2", heading); title.id = "revoke-title";
    const buttons = element("div", undefined, "actions");
    let accepted = false;
    const cancel = element("button", t("cancel"), "secondary"); cancel.type = "button"; cancel.autofocus = true; cancel.onclick = () => dialog.close();
    const confirm = element("button", t("confirmRevoke"), "danger"); confirm.type = "button"; confirm.onclick = () => { accepted = true; dialog.close(); };
    buttons.append(cancel, confirm); dialog.append(title, element("p", message), buttons);
    dialog.addEventListener("close", () => { dialog.remove(); resolve(accepted); }, { once: true });
    document.body.append(dialog); dialog.showModal();
  });
}
const activeGrant = (grant) => ["active", "confirmed", "pending", "reconfirmation_required"].includes(grant.status);
function connectionCard(grants) {
  const node = element("article", undefined, "card connection-card"), heading = element("div", undefined, "connection-heading");
  heading.append(element("h2", grants[0].client_name), element("span", `${grants.length} ${t(grants.length === 1 ? "service" : "services")}`, "muted"));
  if (grants.some(activeGrant)) heading.append(action(t("endSession"), async () => {
    if (!await confirmRevocation(`${grants[0].client_name} — ${t("revokeConnection")}`, t("endSession"))) return;
    await api("api/broker-sessions/" + encodeURIComponent(grants[0].broker_session_id) + "/revoke", "POST");
    byId("message").textContent = t("connectionRevoked"); await load(false);
  }, "danger"));
  node.append(heading);
  for (const grant of grants) node.append(card(grant, false));
  return node;
}
function card(grant, pending) {
  const node = element("article", undefined, "card");
  node.append(element(pending ? "h2" : "h3", pending ? grant.client_name : grant.endpoint_id));
  const fields = [
    ...(pending ? [[t("endpoint"), grant.endpoint_id], [t("scopes"), grant.allowed_scopes?.join(", ") || "—"]] : []),
    [t("tools"), grant.allowed_tools?.join(", ") || "—"],
    [
      t("resources"),
      grant.resource_rules?.length
        ? JSON.stringify(grant.resource_rules, null, 2)
        : "—",
    ],
    [
      t("capabilities"),
      Object.entries(grant.capabilities)
        .filter(([, on]) => on)
        .map(([name]) => t(name + "Capability"))
        .join(", "),
    ],
    [t("expires"), new Date(grant.expires_at).toLocaleString(language === "zh" ? "zh-CN" : "en")],
    [t("status"), t(grant.status)],
  ];
  if (pending) fields.unshift([t("pairing"), grant.pairing_code]);
  const list = element("dl");
  for (const [name, value] of fields) {
    list.append(element("dt", name), element("dd", value));
  }
  node.append(
    list,
    element("p", t(grant.allow_write_requests ? "write" : "read"), "notice"),
  );
  const details = element("details", undefined, "grant-technical");
  details.append(element("summary", t("technical")));
  const technical = element("dl");
  for (const [name, value] of [[t("session"), grant.broker_session_id], [t("client"), grant.client_instance_id], ["Grant ID", grant.grant_id], ["Endpoint UID", grant.endpoint_uid], [t("scopes"), grant.allowed_scopes?.join(", ") || "—"]]) technical.append(element("dt", name), element("dd", value));
  details.append(technical); node.append(details);
  const buttons = element("div", undefined, "actions");
  const mutate = async (path, message = "done") => {
    await api(path, "POST");
    byId("message").textContent = t(message);
    await load(false);
  };
  if (pending && grant.status === "pending") {
    node.append(element("p", t("pair"), "muted"));
    buttons.append(
      action(t("confirm"), () =>
        mutate(
          "api/client-authorization-requests/" + grant.grant_id + "/confirm",
        ),
      ),
      action(
        t("deny"),
        () =>
          mutate(
            "api/client-authorization-requests/" + grant.grant_id + "/deny", "requestDenied",
          ),
        "secondary",
      ),
    );
  } else if (activeGrant(grant)) {
    buttons.append(action(t("revoke"), async () => {
      if (await confirmRevocation(`${grant.endpoint_id} — ${t("revokeService")}`)) await mutate("api/client-grants/" + grant.grant_id + "/revoke", "serviceRevoked");
    }, "danger"));
  }
  node.append(buttons);
  return node;
}
async function load(showLoading = true) {
  for (const id of ["eyebrow", "title", "intro", "footer"])
    byId(id).textContent = t(id);
  byId("portal-label").textContent = t("portalLabel");
  document.title = "MCPHub · " + t("portalLabel");
  document.documentElement.lang = language === "zh" ? "zh-CN" : "en";
  byId("language").textContent = language === "zh" ? "English" : "中文";
  if (showLoading) byId("message").textContent = t("loading");
  byId("identity").replaceChildren();
  byId("request").replaceChildren();
  byId("grants").replaceChildren();
  byId("accounts").replaceChildren();
  byId("accounts").hidden = true;
  try {
    session = await api("auth/session");
    if (!session.authenticated) {
      if (session.builtin) {
        const local = await import("/client-auth/local-account.js");
        byId("identity").append(local.loginForm("/client-auth/auth/local-login", () => load()));
      }
      const link = element("a", t("login"), "button");
      link.href = "/client-auth/auth/login";
      link.hidden = session.builtin && !session.enterprise;
      byId("identity").append(link);
      const reason = new URLSearchParams(location.search).get("login_error");
      byId("message").textContent = reason === "account_access_required" ? t("accountAccessRequired") : reason ? t("error") : "";
      return;
    }
    const deviceCode = sessionStorage.getItem("mcphub-device-code");
    if (deviceCode && /^[A-Z2-7]{4}-[A-Z2-7]{4}$/.test(deviceCode)) { location.replace("/client-auth/device?user_code="+encodeURIComponent(deviceCode)); return; }
    byId("identity").append(
      element("span", t("owner") + ": " + (session.display_name || session.subject)),
      action(
        t("logout"),
        async () => {
          await api("auth/logout", "POST");
          await load();
        },
        "secondary",
      ),
    );
    if (session.local_account) {
      const local = await import("/client-auth/local-account.js");
      byId("identity").append(action(
        language === "zh" ? "我的账号" : "My account",
        () => local.openLocalAccount("/client-auth/auth", session.csrf), "secondary",
      ));
    }
    await loadAccounts();
    const id = sessionStorage.getItem("mcphub-client-request");
    let shownRequest = "";
    if (id) {
      try {
        const grant = await api("api/client-authorization-requests/" + encodeURIComponent(id));
        byId("request").append(element("h2", t("request")), card(grant, true));
        shownRequest = grant.grant_id;
        if (grant.status !== "pending") clearRequest();
      } catch (error) {
        if (["client_grant_invalid", "client_grant_expired", "client_grant_revoked", "client_broker_session_ended"].includes(error.code)) {
          clearRequest();
          byId("request").append(element("p", t("invalidRequest"), "notice"));
        } else {
          byId("request").append(element("p", error.message, "notice"), action(t("retryRequest"), () => load()));
        }
      }
    }
    const result = await api("api/client-grants");
    byId("grants").append(element("h2", t("mine")));
    const connections = new Map();
    for (const grant of result.grants) {
      if (grant.grant_id === shownRequest) continue;
      const id = grant.broker_session_id || grant.grant_id;
      if (!connections.has(id)) connections.set(id, []);
      connections.get(id).push(grant);
    }
    const current = [...connections.values()].filter((grants) => grants.some(activeGrant));
    const previous = [...connections.values()].filter((grants) => !grants.some(activeGrant));
    if (!current.length) byId("grants").append(element("p", t("empty"), "card muted"));
    for (const grants of current) byId("grants").append(connectionCard(grants));
    if (previous.length) {
      const history = element("details", undefined, "grant-history");
      history.append(element("summary", `${t("history")} (${previous.length})`));
      for (const grants of previous) history.append(connectionCard(grants));
      byId("grants").append(history);
    }
    if (showLoading) byId("message").textContent = "";
    const outcome = new URLSearchParams(location.search).get("connection");
    if (["connected","reconnected","cancelled","expired","changed","rejected","failed"].includes(outcome)) {
      byId("message").textContent = t("connection_" + outcome);
      history.replaceState(null,"",location.pathname + "#accounts");
    }
  } catch (error) {
    byId("message").textContent = error.message;
  }
}
function clearRequest() {
  sessionStorage.removeItem("mcphub-client-request");
  const url = new URL(location.href);
  url.searchParams.delete("request");
  history.replaceState(null, "", url.pathname + url.search + url.hash);
}
byId("language").addEventListener("click", () => {
  language = language === "zh" ? "en" : "zh";
  localStorage.setItem("mcphub-client-language", language);
  load();
});
load();
