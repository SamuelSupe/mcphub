package sso

import (
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
)

var consentTemplate = template.Must(template.New("consent").Funcs(template.FuncMap{
	"selected": func(form url.Values, field, value string) bool { return slices.Contains(form[field], value) },
	"value":    func(form url.Values, field string) string { return form.Get(field) },
	"writes": func(tools []config.ClientToolOption) bool {
		return slices.ContainsFunc(tools, func(tool config.ClientToolOption) bool { return tool.Effect != "read" })
	},
}).Parse(`<!doctype html>
<html lang="{{.Lang}}"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>MCPHub · {{.Text.title}}</title>
<style>
*{box-sizing:border-box}body{margin:0;background:#f5f7fa;color:#1d2b40;font:16px/1.65 system-ui}
main{width:calc(100% - 32px);max-width:680px;margin:5vh auto;padding:28px;background:white;border:1px solid #e2e7ef;border-radius:12px}
h1{font-size:26px;line-height:1.3}fieldset{margin:18px 0;padding:16px;border:1px solid #d5deec;border-radius:8px}legend{font-weight:600;padding:0 8px}label{display:block;margin:10px 0}
input[type=checkbox]{margin-right:8px;accent-color:#3265dc}textarea,select{width:100%;padding:10px;font:inherit;border:1px solid #cbd5e1;border-radius:6px}textarea{margin-top:12px;min-height:100px}
button{padding:10px 18px;font:inherit;border:1px solid #d5deec;border-radius:7px;background:white;color:inherit;cursor:pointer}.primary{background:#3265dc;color:white;border-color:#3265dc}button:disabled{opacity:.5;cursor:default}
.actions,.heading{display:flex;align-items:center;justify-content:space-between;gap:12px;flex-wrap:wrap}.actions{justify-content:flex-start;margin-top:22px}.language{font-size:14px}.error{color:#9c372b;background:#fff1ef;padding:12px;border-radius:6px}small,.muted{color:#68778d}.muted{font-size:14px}summary{cursor:pointer}input:focus-visible,button:focus-visible,select:focus-visible,summary:focus-visible{outline:3px solid #3265dc60;outline-offset:3px} @media(max-width:480px){main{padding:20px;margin:20px auto}fieldset{padding:12px}h1{font-size:23px}}
</style>
<main><form method="post" action="/sso/consent">
<div class="heading"><strong>MCPHub</strong><button class="language" name="decision" value="language" formnovalidate>{{.Text.language}}</button></div>
<h1>{{.Text.title}}</h1><p><b>{{.Pending.Name}}</b> · {{.Pending.Identity.Name}}</p><p>{{.Text.intro}}</p>
{{if .Error}}<p class="error" role="alert">{{.Error}}</p>{{end}}
<input type="hidden" name="request" value="{{.ID}}"><input type="hidden" name="lang" value="{{.Lang}}">
{{$form := .Form}}{{$text := .Text}}
{{range .Pending.Options}}{{$service := .ID}}<fieldset><legend>{{.ID}}</legend>
{{range .Tools}}<label><input type="checkbox" name="tool:{{$service}}" value="{{.Name}}" {{if selected $form (printf "tool:%s" $service) .Name}}checked{{end}}>{{.Name}} <small>· {{if eq .Effect "read"}}{{$text.read}}{{else}}{{$text.write}}{{end}}</small></label>{{end}}
{{if writes .Tools}}<label><input type="checkbox" name="write:{{.ID}}" {{if selected $form (printf "write:%s" .ID) "on"}}checked{{end}}>{{$text.allowWrite}}</label>{{end}}
{{if .Prompts}}<label><input type="checkbox" name="prompts:{{.ID}}" {{if selected $form (printf "prompts:%s" .ID) "on"}}checked{{end}}>{{$text.prompts}}</label>{{end}}
{{if .Resources}}<label><input type="checkbox" name="resources:{{.ID}}" {{if selected $form (printf "resources:%s" .ID) "on"}}checked{{end}}>{{$text.resources}}</label>{{end}}
{{if .Subscriptions}}<label><input type="checkbox" name="subscriptions:{{.ID}}" {{if selected $form (printf "subscriptions:%s" .ID) "on"}}checked{{end}}>{{$text.subscriptions}}</label>{{end}}
{{if .Tools}}<details {{if value $form (printf "rules:%s" .ID)}}open{{end}}><summary>{{$text.conditions}}</summary><textarea name="rules:{{.ID}}" aria-label="{{.ID}} · {{$text.conditions}}" placeholder='[{"argument":"/project","allowed_values":["work"]}]'>{{value $form (printf "rules:%s" .ID)}}</textarea></details>{{end}}
</fieldset>{{else}}<p class="error">{{.Text.empty}}</p>{{end}}
<label>{{.Text.duration}}<select name="ttl">{{range .Durations}}<option value="{{.Value}}" {{if selected $form "ttl" .Value}}selected{{end}}>{{.Label}}</option>{{end}}</select></label>
<div class="actions"><button class="primary" name="decision" value="confirm" {{if not .Pending.Options}}disabled{{end}}>{{.Text.confirm}}</button><button name="decision" value="deny">{{.Text.deny}}</button></div>
<p class="muted">{{.Text.footer}}</p></form></main></html>`))

// Re-rendering keeps selections without persisting them or extending consent expiry.
func (s *Server) renderConsent(w http.ResponseWriter, id string, pending nativeConsent, message string, form url.Values) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", browserFormPolicy(pending.Code.Redirect))
	lang := "zh-CN"
	text := map[string]string{"title": "授权 Agent 连接",
		"language":      "English",
		"intro":         "选择此连接可访问的服务、工具和能力。只授权需要的范围；写操作仍需逐次审批。",
		"read":          "只读",
		"write":         "需审批",
		"allowWrite":    "允许申请写操作（具体操作仍需审批）",
		"prompts":       "提示词",
		"resources":     "读取资源",
		"subscriptions": "订阅资源（需同时勾选读取资源）",
		"conditions":    "高级资源条件",
		"duration":      "授权有效期",
		"confirm":       "确认授权",
		"deny":          "拒绝",
		"footer":        "新增工具不会自动扩权。凭证留在 MCPBridge 或客户端中；可在个人授权中心撤销连接。",
		"empty":         "当前账号没有可授权的服务。请联系管理员配置权限组和发布工具，再重新发起连接。",
	}
	if pending.Lang == "en" {
		lang = "en"
		text = map[string]string{"title": "Authorize Agent connection",
			"language":      "中文",
			"intro":         "Select the services, tools and capabilities this connection needs. Writes still require approval for each operation.",
			"read":          "Read-only",
			"write":         "Approval required",
			"allowWrite":    "Allow write requests (each operation still needs approval)",
			"prompts":       "Prompts",
			"resources":     "Read resources",
			"subscriptions": "Subscribe to resources (also select read resources)",
			"conditions":    "Advanced resource rules",
			"duration":      "Access duration",
			"confirm":       "Authorize",
			"deny":          "Deny",
			"footer":        "New tools do not expand access. Credentials stay in MCPBridge or your client. Revoke the connection in your personal authorization portal.",
			"empty":         "This account has no services available for authorization. Ask your administrator to configure permission groups and publish tools, then start again.",
		}
	}
	if parts := strings.SplitN(message, " / ", 2); len(parts) == 2 {
		if lang == "en" {
			message = parts[1]
		} else {
			message = parts[0]
		}
	}
	type durationOption struct{ Value, Label string }
	durations := []durationOption{}
	values := []int64{}
	maximum := int64(s.cfg.ClientAuthorization.GrantTTL() / time.Second)
	for _, value := range []int64{900, 3600, maximum} {
		if value > maximum || slices.Contains(values, value) {
			continue
		}
		values = append(values, value)
		number, unit := value, "秒"
		if value%3600 == 0 {
			number, unit = value/3600, "小时"
		} else if value%60 == 0 {
			number, unit = value/60, "分钟"
		}
		if lang == "en" {
			unit = map[string]string{"秒": "seconds", "分钟": "minutes", "小时": "hours"}[unit]
			if number == 1 {
				unit = strings.TrimSuffix(unit, "s")
			}
			unit = " " + unit
		}
		durations = append(durations, durationOption{strconv.FormatInt(value, 10), strconv.FormatInt(number, 10) + unit})
	}
	if form == nil {
		form = url.Values{}
	}
	selectedDuration, _ := strconv.ParseInt(form.Get("ttl"), 10, 64)
	if !slices.Contains(values, selectedDuration) && len(durations) > 0 {
		form.Set("ttl", durations[0].Value)
	}
	_ = consentTemplate.Execute(w, map[string]any{"ID": id, "Pending": pending, "Lang": lang, "Text": text, "Error": message, "Form": form, "Durations": durations})
}
