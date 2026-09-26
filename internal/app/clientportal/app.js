const messages = {
  zh: {
    eyebrow: "个人授权中心",
    title: "管理客户端的访问范围",
    intro:
      "登录与授权分开管理。你可以确认客户端能访问的工具、权限与业务资源，也可以随时撤销。",
    login: "登录以查看并确认",
    logout: "退出网页会话",
    loading: "正在读取授权…",
    empty: "尚无客户端授权。在本机运行 mcphub-cli client add 发起申请。",
    request: "确认客户端授权",
    pair: "请与终端显示的配对码核对",
    confirm: "确认授权",
    deny: "拒绝",
    revoke: "撤销授权",
    session: "Broker 会话",
    endSession: "撤销此会话全部授权",
    done: "已保存。返回终端或 MCP 客户端继续。",
    mine: "我的客户端",
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
    title: "Control client access",
    intro:
      "Review the tools, scopes and business resources each client may access. Revoke access at any time.",
    login: "Sign in to review",
    logout: "Sign out of portal",
    loading: "Loading authorizations…",
    empty:
      "No client authorizations. Run mcphub-cli client add on your computer to start.",
    request: "Review client authorization",
    pair: "Compare this pairing code with your terminal",
    confirm: "Authorize client",
    deny: "Deny",
    revoke: "Revoke access",
    session: "Broker session",
    endSession: "Revoke all access in this session",
    done: "Saved. Return to your terminal or MCP client to continue.",
    mine: "Your clients",
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
  t = (key) => messages[language][key] || key;
const requestId = new URLSearchParams(location.search).get("request");
if (requestId && /^gr_[A-Za-z0-9_-]+$/.test(requestId))
  sessionStorage.setItem("mcphub-client-request", requestId);
function element(tag, text, className) {
  const node = document.createElement(tag);
  if (text !== undefined) node.textContent = text;
  if (className) node.className = className;
  return node;
}
async function api(path, method = "GET") {
  const response = await fetch("/client-auth/" + path, {
    method,
    credentials: "same-origin",
    headers: method === "GET" ? {} : { "X-MCPHub-CSRF": session?.csrf || "" },
  });
  if (!response.ok) {
    let code = "";
    try {
      code = (await response.json()).error?.code || "";
    } catch {}
    throw new Error(t("error") + (code ? " (" + code + ")" : ""));
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
function card(grant, pending) {
  const node = element("article", undefined, "card");
  node.append(element("h2", grant.client_name));
  const fields = [
    [t("session"), grant.broker_session_id],
    [t("client"), grant.client_instance_id],
    [t("endpoint"), grant.endpoint_id + " · " + grant.endpoint_uid],
    [t("scopes"), grant.allowed_scopes?.join(", ") || "—"],
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
        .map(([name]) => name)
        .join(", "),
    ],
    [t("expires"), new Date(grant.expires_at).toLocaleString()],
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
  const buttons = element("div", undefined, "actions");
  const mutate = async (path) => {
    await api(path, "POST");
    byId("message").textContent = t("done");
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
            "api/client-authorization-requests/" + grant.grant_id + "/deny",
          ),
        "secondary",
      ),
    );
  } else if (
    ["active", "confirmed", "pending", "reconfirmation_required"].includes(
      grant.status,
    )
  ) {
    buttons.append(
      action(
        t("revoke"),
        () => mutate("api/client-grants/" + grant.grant_id + "/revoke"),
        "danger",
      ),
      action(
        t("endSession"),
        () =>
          mutate("api/broker-sessions/" + grant.broker_session_id + "/revoke"),
        "secondary",
      ),
    );
  }
  node.append(buttons);
  return node;
}
async function load(showLoading = true) {
  for (const id of ["eyebrow", "title", "intro", "footer"])
    byId(id).textContent = t(id);
  document.documentElement.lang = language === "zh" ? "zh-CN" : "en";
  byId("language").textContent = language === "zh" ? "English" : "中文";
  if (showLoading) byId("message").textContent = t("loading");
  byId("identity").replaceChildren();
  byId("request").replaceChildren();
  byId("grants").replaceChildren();
  try {
    session = await api("auth/session");
    if (!session.authenticated) {
      const link = element("a", t("login"), "button");
      link.href = "/client-auth/auth/login";
      byId("identity").append(link);
      byId("message").textContent = "";
      return;
    }
    byId("identity").append(
      element("span", t("owner") + ": " + session.subject),
      action(
        t("logout"),
        async () => {
          await api("auth/logout", "POST");
          await load();
        },
        "secondary",
      ),
    );
    const id = sessionStorage.getItem("mcphub-client-request");
    if (id) {
      const grant = await api(
        "api/client-authorization-requests/" + encodeURIComponent(id),
      );
      byId("request").append(element("h2", t("request")), card(grant, true));
      if (grant.status !== "pending")
        sessionStorage.removeItem("mcphub-client-request");
    }
    const result = await api("api/client-grants");
    byId("grants").append(element("h2", t("mine")));
    if (!result.grants.length)
      byId("grants").append(element("p", t("empty"), "muted"));
    for (const grant of result.grants) {
      if (grant.grant_id !== id) byId("grants").append(card(grant, false));
    }
    if (showLoading) byId("message").textContent = "";
  } catch (error) {
    byId("message").textContent = error.message;
  }
}
byId("language").addEventListener("click", () => {
  language = language === "zh" ? "en" : "zh";
  localStorage.setItem("mcphub-client-language", language);
  load();
});
load();
