import { loginForm } from "/client-auth/local-account.js";
let language = localStorage.getItem("mcphub-client-language") || "zh",
  session,
  request,
  busy = false;
const el = (id) => document.getElementById(id),
  text = (zh, en) => (language === "zh" ? zh : en);
const node = (tag, value, cls) => {
  const n = document.createElement(tag);
  if (value) n.textContent = value;
  if (cls) n.className = cls;
  return n;
};
let code =
  new URLSearchParams(location.search).get("user_code") ||
  sessionStorage.getItem("mcphub-device-code") ||
  "";
code = code.toUpperCase().replace(/[^A-Z2-7]/g, "");
if (code.length === 8) code = code.slice(0, 4) + "-" + code.slice(4);
if (/^[A-Z2-7]{4}-[A-Z2-7]{4}$/.test(code))
  sessionStorage.setItem("mcphub-device-code", code);
else code = "";
// Keep only the public comparison code in this tab. No credential is carried through redirects.
history.replaceState(null, "", location.pathname);
async function api(path, body) {
  const r = await fetch("/client-auth/" + path, {
    method: body ? "POST" : "GET",
    headers: body
      ? {
          "Content-Type": "application/json",
          "X-MCPHub-CSRF": session?.csrf || "",
        }
      : {},
    body: body ? JSON.stringify(body) : undefined,
    credentials: "same-origin",
  });
  const d = r.status === 204 ? {} : await r.json();
  if (!r.ok) {
    const messages = {
      invalid_grant: text(
        "配对码无效或请求已领取，请在 Agent 中重新开始。",
        "Invalid or already collected request. Start pairing again in your Agent.",
      ),
      expired_token: text(
        "配对已过期，请在 Agent 中重新开始。",
        "Pairing expired. Start again in your Agent.",
      ),
      access_denied: text(
        "当前权限或请求范围不允许此授权。",
        "Your current permissions or requested scope do not allow this authorization.",
      ),
      temporarily_unavailable: text(
        "请求过于频繁或服务暂不可用，请稍后重试。",
        "Too many requests or service unavailable. Retry later.",
      ),
    };
    throw Error(
      messages[d.error] ||
        text(
          "请求失败，请重新登录后重试。",
          "Request failed. Sign in again and retry.",
        ),
    );
  }
  return d;
}
function button(label, fn, cls = "secondary") {
  const b = node("button", label, cls);
  b.type = "button";
  b.onclick = fn;
  return b;
}
function fail(error) {
  el("message").textContent = error.message;
}
async function load() {
  document.documentElement.lang = language === "zh" ? "zh-CN" : "en";
  el("language").textContent = language === "zh" ? "English" : "中文";
  el("eyebrow").textContent = text(
    "Agent 配对 · 1 登录 → 2 确认范围 → 3 返回 Agent",
    "Agent pairing · 1 Sign in → 2 Review access → 3 Return to Agent",
  );
  el("title").textContent = text(
    "授权这个 Agent 连接",
    "Authorize this Agent connection",
  );
  el("intro").textContent = text(
    "仅当你刚刚发起了配对才继续。核对 Agent 中显示的配对码；登录本身不会授予客户端访问权限。",
    "Continue only if you just requested pairing. Compare the code shown by your Agent. Signing in does not grant client access.",
  );
  el("identity").replaceChildren();
  el("device").replaceChildren();
  el("message").textContent = "";
  try {
    session = await api("auth/session");
    if (!session.authenticated) {
      if (session.builtin)
        el("identity").append(loginForm("/client-auth/auth/local-login", load));
      if (!session.builtin || session.enterprise) {
        const a = node(
          "a",
          text("LDAP / OIDC 企业登录", "LDAP / OIDC enterprise sign-in"),
          "button",
        );
        a.href =
          "/client-auth/auth/login?lang=" +
          (language === "en" ? "en" : "zh-CN");
        el("identity").append(a);
      }
      return;
    }
    el("identity").append(
      node(
        "strong",
        text("当前账号：", "Signed in as: ") +
          (session.display_name || session.subject),
      ),
      node("p", session.subject, "muted"),
      button(text("切换账号", "Switch account"), async () => {
        try {
          await api("auth/logout", {});
          await load();
        } catch (e) {
          fail(e);
        }
      }),
    );
    if (!code) {
      const f = node("form", null, "card"),
        label = node(
          "label",
          text("输入 Agent 显示的配对码", "Enter the code shown by your Agent"),
        ),
        input = node("input");
      input.autocomplete = "off";
      input.maxLength = 9;
      input.required = true;
      input.placeholder = "ABCD-2345";
      label.append(input);
      const submit = node(
        "button",
        text("查找请求", "Find request"),
        "primary",
      );
      f.append(label, submit);
      f.onsubmit = (e) => {
        e.preventDefault();
        const c = input.value.toUpperCase().replace(/[ -]/g, "");
        if (!/^[A-Z2-7]{8}$/.test(c)) {
          fail(
            Error(
              text("请输入 8 位配对码。", "Enter the eight-character code."),
            ),
          );
          return;
        }
        code = c.slice(0, 4) + "-" + c.slice(4);
        sessionStorage.setItem("mcphub-device-code", code);
        load();
      };
      el("device").append(f);
      return;
    }
    request = await api("api/device?user_code=" + encodeURIComponent(code));
    render();
  } catch (e) {
    fail(e);
  }
}
function render() {
  const box = node("div", null, "card");
  box.append(
    node("h2", request.user_code),
    node(
      "p",
      text("核对配对码与 Agent 一致", "Compare this code with your Agent"),
    ),
    node("strong", request.client_name),
    node(
      "p",
      text(
        "此名称由请求方填写，不代表经过验证的客户端。",
        "This name is provided by the requester; it is not a verified client identity.",
      ),
      "notice",
    ),
    node(
      "p",
      text("实例：", "Instance: ") + request.client_instance_id,
      "muted",
    ),
    node(
      "p",
      text("身份来源：", "Identity source: ") +
        (request.provider === "mcphub:local"
          ? text("MCPHub 本地账号", "MCPHub local account")
          : request.provider.startsWith("ldap:")
            ? "LDAP"
            : "OIDC"),
      "muted",
    ),
    node(
      "p",
      text("创建时间：", "Requested: ") +
        new Date(request.created_at).toLocaleString(),
    ),
    node(
      "p",
      text("有效至：", "Expires: ") +
        new Date(request.expires_at).toLocaleString(),
    ),
  );
  if (request.status !== "pending") {
    sessionStorage.removeItem("mcphub-device-code");
    box.append(
      node(
        "p",
        ["approved", "redeemed", "completed"].includes(request.status)
          ? text(
              "已确认。返回 Agent 查看连接结果。",
              "Approved. Return to your Agent to check the connection.",
            )
          : text(
              "请求已结束。请在 Agent 中重新开始。",
              "This request ended. Start again in your Agent.",
            ),
      ),
    );
    el("device").append(box);
    return;
  }
  const form = node("form"),
    label = node("label", text("选择服务", "Choose a service")),
    select = node("select");
  select.required = true;
  select.append(new Option(text("请选择服务", "Choose a service"), ""));
  for (const ep of request.endpoints) {
    if (!request.endpoint_id || ep.id === request.endpoint_id)
      select.append(new Option(ep.id, ep.id));
  }
  if (request.endpoint_id) select.value = request.endpoint_id;
  label.append(select);
  const tools = node("fieldset"),
    legend = node(
      "legend",
      text("选择工具（默认不勾选）", "Select tools (none selected by default)"),
    );
  tools.append(legend);
  const writeLabel = node("label"),
    write = node("input");
  write.type = "checkbox";
  writeLabel.append(
    write,
    node(
      "span",
      text(
        "允许发起写入请求（仍需满足审批规则）",
        "Allow write requests (existing approval rules still apply)",
      ),
    ),
  );
  writeLabel.hidden = !request.allow_write_requests;
  const redraw = () => {
    tools.replaceChildren(legend);
    const ep = request.endpoints.find((e) => e.id === select.value);
    for (const t of ep?.tools || []) {
      if (
        request.allowed_tools?.length &&
        !request.allowed_tools.includes(t.name)
      )
        continue;
      if (t.effect !== "read" && !write.checked) continue;
      const l = node("label"),
        i = node("input");
      i.type = "checkbox";
      i.name = "tools";
      i.value = t.name;
      l.append(i, node("span", t.name + " · " + t.effect));
      tools.append(l);
    }
    if (!select.value)
      tools.append(
        node(
          "p",
          request.endpoints.length
            ? text(
                "先选择服务，再勾选所需工具。",
                "Choose a service, then select the tools you need.",
              )
            : text(
                "没有可访问的服务。请联系管理员启用账号并授予组权限。",
                "No accessible services. Ask an administrator to enable your account and grant group access.",
              ),
        ),
      );
    else if (!ep || !tools.querySelector("input"))
      tools.append(
        node(
          "p",
          text(
            "此服务没有你可授权的工具。联系管理员检查组权限和工具发布。",
            "No authorized tools for this service. Ask an administrator to check group access and tool publication.",
          ),
        ),
      );
  };
  select.onchange = redraw;
  write.onchange = redraw;
  redraw();
  const ttlLabel = node(
      "label",
      text("授权时长（分钟）", "Access duration (minutes)"),
    ),
    ttl = node("input");
  ttl.type = "number";
  ttl.min = "1";
  ttl.max = String(Math.floor(request.max_ttl_seconds / 60));
  ttl.value = String(Math.min(60, Number(ttl.max)));
  ttl.required = true;
  ttlLabel.append(ttl);
  const resourceLabel = node(
      "label",
      text(
        "资源限制（可选，JSON 数组）",
        "Resource restrictions (optional JSON array)",
      ),
    ),
    resources = node("textarea");
  resources.rows = 3;
  resources.placeholder = "[]";
  resources.value = request.resource_rules?.length
    ? JSON.stringify(request.resource_rules, null, 2)
    : "";
  resources.readOnly = !!request.resource_rules?.length;
  resourceLabel.append(resources);
  const resourceDetails = node("details");
  resourceDetails.append(
    node(
      "summary",
      text(
        "进一步限制数据范围（高级，可选）",
        "Narrow data access (advanced, optional)",
      ),
    ),
    node(
      "p",
      text(
        '限制工具参数中的数据范围，不改变组权限。例如：[{"argument":"/project","allowed_values":["project-a"]}]。',
        'Restrict data through tool arguments without changing group permissions. Example: [{"argument":"/project","allowed_values":["project-a"]}].',
      ),
      "muted",
    ),
    resourceLabel,
  );
  if (resources.readOnly) resourceDetails.open = true;
  const approve = node("button", text("确认授权", "Approve access"), "primary");
  approve.type = "submit";
  approve.disabled = request.endpoints.length === 0;
  const deny = button(text("拒绝", "Deny"), () => decide("deny", {}), "danger");
  form.append(
    label,
    writeLabel,
    tools,
    ttlLabel,
    resourceDetails,
    node(
      "p",
      text(
        "授权只能缩小当前组允许的范围。新增工具不会自动加入此连接。",
        "Access is limited by your current groups. Newly published tools are not added to this connection.",
      ),
      "muted",
    ),
    approve,
    deny,
  );
  form.onsubmit = (e) => {
    e.preventDefault();
    const selected = [
      ...form.querySelectorAll("input[name=tools]:checked"),
    ].map((i) => i.value);
    if (!selected.length) {
      fail(Error(text("至少选择一个工具。", "Select at least one tool.")));
      return;
    }
    let rules;
    try {
      rules = resources.readOnly
        ? undefined
        : resources.value.trim()
          ? JSON.parse(resources.value)
          : undefined;
      if (rules && !Array.isArray(rules)) throw Error();
    } catch {
      fail(
        Error(
          text(
            "资源限制须为 JSON 数组。",
            "Resource restrictions must be a JSON array.",
          ),
        ),
      );
      return;
    }
    decide("confirm", {
      endpoint_id: select.value,
      allowed_tools: selected,
      allow_write_requests: write.checked,
      ttl_seconds: Number(ttl.value) * 60,
      resource_rules: rules,
    });
  };
  box.append(form);
  el("device").append(box);
}
async function decide(action, body) {
  if (busy) return;
  busy = true;
  el("message").textContent = "";
  for (const b of el("device").querySelectorAll("button")) b.disabled = true;
  try {
    const result = await api("api/device/" + action, {
      user_code: code,
      ...body,
    });
    sessionStorage.removeItem("mcphub-device-code");
    el("device").replaceChildren(
      node(
        "p",
        result.status === "approved"
          ? text(
              "授权已确认。请返回 Agent，等待凭证保存和连接检查完成。",
              "Access approved. Return to your Agent while credentials are saved and the connection is checked.",
            )
          : text(
              "已拒绝，未授予客户端权限。",
              "Denied. No client access was granted.",
            ),
        "card",
      ),
    );
  } catch (e) {
    fail(e);
    for (const b of el("device").querySelectorAll("button")) b.disabled = false;
  } finally {
    busy = false;
  }
}
el("language").onclick = () => {
  language = language === "zh" ? "en" : "zh";
  localStorage.setItem("mcphub-client-language", language);
  load();
};
load();
