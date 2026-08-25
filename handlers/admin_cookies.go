package handlers

import (
	"bytes"
	"encoding/json"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

const cookieAdminMaxCookieBytes = 64 << 10

var cookieSlotNameRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,80}$`)

type cookieAdminAccount struct {
	AccountID                string `json:"account_id"`
	Slot                     string `json:"slot"`
	File                     string `json:"file"`
	Available                bool   `json:"available"`
	NeedsLogin               bool   `json:"needs_login"`
	CooldownRemainingSeconds int    `json:"cooldown_remaining_seconds"`
	Username                 string `json:"username"`
	LastSuccessAt            string `json:"last_success_at"`
	LastFailureAt            string `json:"last_failure_at"`
	LastErrorCode            string `json:"last_error_code"`
	LastErrorMessage         string `json:"last_error_message"`
	ConsecutiveFailures      int    `json:"consecutive_failures"`
	LastTestAt               string `json:"last_test_at"`
	LastTestCode             string `json:"last_test_code"`
}

type cookieAdminPageData struct {
	Token    string
	Valid    bool
	Message  string
	Error    string
	Accounts []cookieAdminAccount
	Slots    []string
}

func CookieAdmin(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" {
		token = strings.TrimSpace(r.FormValue("token"))
	}
	configured := strings.TrimSpace(os.Getenv("COOKIE_ADMIN_TOKEN"))
	data := cookieAdminPageData{Token: token}
	if configured == "" || token == "" || token != configured {
		if token != "" {
			data.Error = "Invalid admin token"
		}
		renderCookieAdmin(w, http.StatusUnauthorized, data)
		return
	}
	data.Valid = true
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, cookieAdminMaxCookieBytes+8192)
		if err := r.ParseForm(); err != nil {
			data.Error = "Invalid form body"
		} else {
			slot := strings.TrimSpace(r.FormValue("slot"))
			action := strings.TrimSpace(r.FormValue("action"))
			if action == "test" {
				if err := testCookieViaHelper(token, slot); err != nil {
					data.Error = err.Error()
				} else {
					data.Message = "Homepage test completed for slot " + slot
				}
			} else {
				cookie := strings.TrimSpace(r.FormValue("cookie"))
				if err := updateCookieViaHelper(token, slot, cookie); err != nil {
					data.Error = err.Error()
				} else {
					data.Message = "Cookie saved and cooldown reset for slot " + slot
				}
			}
		}
	}
	accounts, err := loadCookieAccountsFromHelper()
	if err != nil {
		if data.Error == "" {
			data.Error = err.Error()
		}
	} else {
		data.Accounts = accounts
		for _, account := range accounts {
			if account.Slot != "" {
				data.Slots = append(data.Slots, account.Slot)
			}
		}
	}
	renderCookieAdmin(w, http.StatusOK, data)
}

func testCookieViaHelper(token, slot string) error {
	base := cookieAdminHelperBase()
	if base == "" {
		return &cookieAdminError{"auth helper is not configured"}
	}
	if !cookieSlotNameRE.MatchString(slot) {
		return &cookieAdminError{"invalid slot name"}
	}
	body, _ := json.Marshal(map[string]string{"slot": slot})
	req, err := http.NewRequest(http.MethodPost, base+"/admin/cookies/test", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Token", token)
	client := http.Client{Timeout: 15 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	resp, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	var payload struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(resp, &payload)
	if res.StatusCode >= 400 || !payload.OK {
		if payload.Error == "" {
			payload.Error = res.Status
		}
		slog.Warn("cookie admin helper test failed", "status", res.StatusCode, "error", payload.Error)
		return &cookieAdminError{payload.Error}
	}
	return nil
}

func loadCookieAccountsFromHelper() ([]cookieAdminAccount, error) {
	base := cookieAdminHelperBase()
	if base == "" {
		return nil, http.ErrServerClosed
	}
	client := http.Client{Timeout: 5 * time.Second}
	res, err := client.Get(base + "/accounts")
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 256<<10))
	if err != nil {
		return nil, err
	}
	var payload struct {
		OK       bool                 `json:"ok"`
		Accounts []cookieAdminAccount `json:"accounts"`
		Error    string               `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 || !payload.OK {
		if payload.Error == "" {
			payload.Error = res.Status
		}
		return nil, &cookieAdminError{payload.Error}
	}
	return payload.Accounts, nil
}

func updateCookieViaHelper(token, slot, cookie string) error {
	base := cookieAdminHelperBase()
	if base == "" {
		return &cookieAdminError{"auth helper is not configured"}
	}
	if !cookieSlotNameRE.MatchString(slot) {
		return &cookieAdminError{"invalid slot name"}
	}
	if cookie == "" || !strings.Contains(cookie, "sessionid=") {
		return &cookieAdminError{"cookie must contain sessionid="}
	}
	if len([]byte(cookie)) > cookieAdminMaxCookieBytes {
		return &cookieAdminError{"cookie is too large"}
	}
	body, _ := json.Marshal(map[string]string{"slot": slot, "cookie": cookie})
	req, err := http.NewRequest(http.MethodPost, base+"/admin/cookies/update", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Token", token)
	client := http.Client{Timeout: 10 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	resp, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	var payload struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(resp, &payload)
	if res.StatusCode >= 400 || !payload.OK {
		if payload.Error == "" {
			payload.Error = res.Status
		}
		slog.Warn("cookie admin helper update failed", "status", res.StatusCode, "error", payload.Error)
		return &cookieAdminError{payload.Error}
	}
	return nil
}

func cookieAdminHelperBase() string {
	base := strings.TrimSpace(os.Getenv("AUTH_HELPER_ADMIN_URL"))
	if base == "" {
		base = strings.TrimSpace(os.Getenv("AUTH_HELPER_URL"))
	}
	return strings.TrimRight(base, "/")
}

type cookieAdminError struct{ msg string }

func (e *cookieAdminError) Error() string { return e.msg }

func renderCookieAdmin(w http.ResponseWriter, status int, data cookieAdminPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := cookieAdminTemplate.Execute(w, data); err != nil {
		slog.Warn("cookie admin template failed", "err", err)
	}
}

var cookieAdminTemplate = template.Must(template.New("cookie-admin").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Instagram7 cookie admin</title>
<style>body{font-family:system-ui,-apple-system,Segoe UI,sans-serif;background:#12081f;color:#fff;margin:0;padding:32px}main{max-width:1180px;margin:auto}.card{background:#211033;border:1px solid #3d2459;border-radius:16px;padding:22px;margin:16px 0}input,select,textarea,button{font:inherit;border-radius:10px;border:1px solid #5b3b80;padding:10px;background:#12081f;color:#fff}textarea{width:100%;min-height:170px;box-sizing:border-box}button{background:#d62976;border:0;font-weight:700;cursor:pointer}.secondary{background:#4b2a6f}.muted{color:#b9a7cc}.small{font-size:12px}.ok{background:#153d27;border-color:#2b7a4b}.err{background:#4a1622;border-color:#b93255}table{width:100%;border-collapse:collapse;font-size:14px}td,th{border-bottom:1px solid #3d2459;padding:8px;text-align:left;vertical-align:top}.pill{display:inline-block;padding:3px 8px;border-radius:999px;background:#3d2459}.yes{background:#126b3d}.no{background:#7c2738}.warn{background:#8a5a14}.row{display:flex;gap:14px;flex-wrap:wrap}.row>*{flex:1;min-width:240px}</style>
</head><body><main><h1>Instagram7 cookie admin</h1>
{{if .Message}}<div class="card ok">{{.Message}}</div>{{end}}{{if .Error}}<div class="card err">{{.Error}}</div>{{end}}
{{if not .Valid}}
<form class="card" method="get"><p class="muted">Enter admin token to manage Instagram cookie slots.</p><input name="token" type="password" placeholder="Admin token" style="width:100%;box-sizing:border-box"><p><button>Open admin</button></p></form>
{{else}}
<div class="card"><h2>Cookie pool</h2><p class="muted">Available means the session passed validation and is not cooling down. A needs-login slot stays quarantined until a validated replacement is saved.</p><table><tr><th>Slot</th><th>State</th><th>Username</th><th>Last success</th><th>Last failure</th><th>Last error</th><th>Test</th><th>File</th></tr>{{range .Accounts}}<tr><td>{{.Slot}}</td><td>{{if .NeedsLogin}}<span class="pill no">needs login</span>{{else if .Available}}<span class="pill yes">available</span>{{else}}<span class="pill warn">cooling {{.CooldownRemainingSeconds}}s</span>{{end}}</td><td>{{if .Username}}@{{.Username}}{{else}}<span class="muted">unknown</span>{{end}}</td><td>{{if .LastSuccessAt}}{{.LastSuccessAt}}{{else}}<span class="muted">never</span>{{end}}</td><td>{{if .LastFailureAt}}{{.LastFailureAt}}<br><span class="small muted">failures: {{.ConsecutiveFailures}}</span>{{else}}<span class="muted">none</span>{{end}}</td><td>{{if .LastErrorCode}}<span class="pill warn">{{.LastErrorCode}}</span><br><span class="small muted">{{.LastErrorMessage}}</span>{{else}}<span class="pill yes">ok/unknown</span>{{end}}</td><td>{{if .LastTestAt}}{{.LastTestAt}}<br><span class="small muted">{{.LastTestCode}}</span>{{else}}<span class="muted">not tested</span>{{end}}</td><td>{{.File}}</td></tr>{{else}}<tr><td colspan="8" class="muted">No accounts loaded</td></tr>{{end}}</table></div>
<div class="row"><form class="card" method="post"><input type="hidden" name="token" value="{{.Token}}"><input type="hidden" name="action" value="save"><h2>Update cookie</h2><p><label>Slot<br><select name="slot">{{range .Slots}}<option value="{{.}}">{{.}}</option>{{end}}</select></label></p><p class="muted">Paste exported Instagram cookies. Cookie is never shown back and is not logged.</p><p><textarea name="cookie" placeholder="sessionid=...; ds_user_id=...;"></textarea></p><p><button>Save cookie and reset cooldown</button></p></form>
<form class="card" method="post"><input type="hidden" name="token" value="{{.Token}}"><input type="hidden" name="action" value="test"><h2>Manual homepage test</h2><p class="muted">Makes one authenticated GET to instagram.com homepage with selected cookie. It may extract username if Instagram exposes it.</p><p><label>Slot<br><select name="slot">{{range .Slots}}<option value="{{.}}">{{.}}</option>{{end}}</select></label></p><p><button class="secondary">Test selected cookie</button></p></form></div>
{{end}}
</main></body></html>`))
