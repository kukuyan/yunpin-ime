// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"errors"
	"html/template"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kukuyan/yunpin-ime/desktopagent"
	"github.com/kukuyan/yunpin-ime/syncclient"
)

// Optional capabilities keep the existing guards/vocabulary integration usable.
type settingsActivationOperations interface {
	ActivationStatus(context.Context) (settingsActivationState, error)
	PrepareVocabulary(context.Context) error
	EnableBackground(context.Context) error
}

type settingsActivationState struct {
	LastEventCode, LastFailureClass                           string
	BaselinePresent, SnapshotPresent, SnapshotApplied         bool
	PrivateCandidatesEnabled                                  bool
	BackgroundAvailable, BackgroundEnabled, BackgroundRunning bool
	LastSuccessAt, PendingUploads                             int64
	ProblemCode                                               string
}

type settingsBaselineOperations interface {
	BaselineStatus(context.Context) (desktopagent.BaselineImportStatus, error)
	ExportBaseline(context.Context) ([]byte, desktopagent.BaselineExportResult, error)
	ImportBaseline(context.Context, []byte, desktopagent.BaselineImportOptions) (desktopagent.BaselineImportResult, error)
	InitializeEmptyBaseline(context.Context) (desktopagent.BaselineImportResult, error)
	ImportLegacy(context.Context, string) (desktopagent.LegacyImportResult, error)
}

func (operations *localSettingsOperations) BaselineStatus(context.Context) (desktopagent.BaselineImportStatus, error) {
	return desktopagent.InspectBaselineImport(operations.defaults)
}

func (operations *localSettingsOperations) ExportBaseline(context.Context) ([]byte, desktopagent.BaselineExportResult, error) {
	return desktopagent.ExportBaseline(operations.defaults)
}

func (operations *localSettingsOperations) ImportBaseline(_ context.Context, contents []byte, options desktopagent.BaselineImportOptions) (desktopagent.BaselineImportResult, error) {
	return desktopagent.ImportBaseline(operations.defaults, contents, options)
}

func (operations *localSettingsOperations) InitializeEmptyBaseline(context.Context) (desktopagent.BaselineImportResult, error) {
	return desktopagent.InitializeOnboardingEmptyBaseline(operations.defaults)
}

func (operations *localSettingsOperations) ImportLegacy(ctx context.Context, name string) (desktopagent.LegacyImportResult, error) {
	return operations.agent.ImportSavedLegacyVocabulary(ctx, operations.defaults, name)
}

type settingsNonceKey struct{}

func settingsNonce(ctx context.Context) string {
	nonce, _ := ctx.Value(settingsNonceKey{}).(string)
	return nonce
}

type settingsRenderOptions struct {
	Notice, ServerDraft string
	AllowPrivateHTTP    bool
}

type settingsOnboardingView struct {
	Available, ActivationAvailable, BaselineAvailable bool
	State                                             settingsOnboardingState
	Activation                                        settingsActivationState
	Baseline                                          desktopagent.BaselineImportStatus
	Complete                                          bool
	CanCancel                                         bool
	HealthSummary, PairingDescription, Problem        string
}

func (handler *settingsHandler) onboardingView(ctx context.Context) *settingsOnboardingView {
	operations, ok := handler.operations.(settingsOnboardingOperations)
	if !ok {
		return nil
	}
	view := &settingsOnboardingView{}
	var err error
	view.State, err = operations.OnboardingStatus(ctx)
	view.Available = err == nil
	if err != nil {
		view.Problem = onboardingProblem("local-state")
	}
	if baseline, ok := handler.operations.(settingsBaselineOperations); ok {
		view.Baseline, err = baseline.BaselineStatus(ctx)
		view.BaselineAvailable = err == nil
		if err != nil {
			view.Problem = onboardingProblem(desktopagent.OnboardingImportErrorCode(err))
		}
	}
	if activation, ok := handler.operations.(settingsActivationOperations); ok {
		view.Activation, err = activation.ActivationStatus(ctx)
		view.ActivationAvailable = err == nil
		if err != nil {
			view.Problem = onboardingProblem("local-state")
		} else if view.Problem == "" && view.Activation.ProblemCode != "" {
			view.Problem = onboardingProblem(view.Activation.ProblemCode)
		}
	}
	view.PairingDescription = map[string]string{
		"invited":          "邀请已创建。请在新设备提交邀请，再回到这里批准。",
		"awaiting_claim":   "已批准新设备。请在新设备继续接入，再回到这里完成确认。",
		"finalize_pending": "正在等待完成配对确认，请继续原有流程。",
		"cancel_pending":   "取消尚未完成，继续操作会完成取消。",
		"joined":           "邀请已提交。请先在原设备批准，然后在本机继续。",
		"rollback_pending": "退出尚未完成，继续操作会完成退出。",
		"provisioning":     "已有接入状态尚未完成，请继续原有接入流程。",
		"ready":            "设备已接入。",
	}[view.State.PairingState]
	view.CanCancel = view.State.PairingPending && !(view.State.PairingRole == "creator" && view.State.PairingState == "finalize_pending")
	view.HealthSummary = onboardingHealth(view, time.Now())
	activation := view.Activation
	view.Complete = view.Available && view.State.ServerConfigured && view.State.Paired && !view.State.PairingPending &&
		activation.LastEventCode == "sync_complete" && view.State.BridgeConfigured &&
		view.ActivationAvailable && activation.BaselinePresent && activation.PrivateCandidatesEnabled &&
		activation.SnapshotPresent && activation.SnapshotApplied && activation.BackgroundAvailable &&
		activation.BackgroundEnabled && activation.BackgroundRunning && activation.LastSuccessAt > 0 &&
		time.Since(time.UnixMilli(activation.LastSuccessAt)) <= 15*time.Minute &&
		len(view.Baseline.PendingLegacy) == 0 && view.BaselineAvailable && activation.PendingUploads == 0 && view.Problem == ""
	return view
}

func onboardingHealth(view *settingsOnboardingView, now time.Time) string {
	s, a := view.State, view.Activation
	switch {
	case !view.Available:
		return "接入状态暂不可读"
	case !s.ServerConfigured:
		return "等待配置同步服务器"
	case s.PairingPending:
		return "设备接入尚待确认"
	case !s.Paired:
		return "尚未接入同步账号"
	case !view.ActivationAvailable:
		return "本机加载与后台状态暂不可读"
	case !a.BaselinePresent:
		return "等待导入基础词库"
	case !a.PrivateCandidatesEnabled || !s.BridgeConfigured:
		return "等待准备本机词库"
	case !a.SnapshotPresent:
		return "等待首次同步生成词库"
	case !a.SnapshotApplied:
		return "词库尚未由输入法确认加载"
	case !a.BackgroundAvailable:
		return "无法确认后台同步状态"
	case !a.BackgroundEnabled:
		return "后台同步已停用"
	case !a.BackgroundRunning:
		return "后台同步未运行"
	case a.LastEventCode == "sync_failed":
		return "最近一轮同步失败，请检查网络或稍后重试"
	case a.LastEventCode == "sync_deferred_busy":
		return "输入法忙，请结束当前输入后等待同步或刷新"
	case a.LastSuccessAt <= 0:
		return "尚未完成过同步"
	case now.Sub(time.UnixMilli(a.LastSuccessAt)) > 15*time.Minute:
		return "最近成功记录已过期，请检查后台同步"
	case a.PendingUploads > 0:
		return "同步已接通，仍有变更等待上传"
	default:
		return "本机同步正在运行"
	}
}

func onboardingNotice(code string) string {
	return map[string]string{
		"server-saved":        "服务器地址已保存，继续使用本机现有身份。",
		"logged-in":           "登录会话已保存。接下来使用已有设备邀请接入；已配对设备无需重配。",
		"pairing-progress":    "接入进度已保存，请按下方提示完成两台设备的确认。",
		"pairing-cancelled":   "本次设备接入已取消。",
		"baseline-imported":   "基础词库已导入。请准备本机词库并同步；如有旧词备份，首次同步后可恢复。",
		"legacy-imported":     "本机旧词已写入同步队列。请再同步一次以加载并上传。",
		"vocabulary-prepared": "本机词库功能已启用并配置，请同步以生成和加载词库。",
		"background-enabled":  "已请求启用后台同步，下方显示实际运行状态。",
	}[code]
}

func onboardingProblem(code string) string {
	return map[string]string{
		"server-invalid":                 "服务器地址无效。请填写完整 HTTPS 地址，不要在地址中填写密码、查询参数或邀请。",
		"server-unreachable":             "服务器暂不可连接。请检查地址与网络，再测试连接；原配置保留。",
		"server-confirm-required":        "已有设备身份。仅修正同一服务的地址时，请勾选确认后保存。",
		"server-identity-mismatch":       "无法确认这是原同步服务。原配置已保留，请核对地址；切换其他服务不能使用此入口。",
		"login-failed":                   "登录未完成。请检查现有用户名和密码后重试。",
		"session-required":               "此账号管理操作需要登录。设备日常同步继续使用已有配对身份。",
		"device-required":                "请在一台已经接入的设备上创建邀请，再在本机加入。",
		"pairing-pending":                "已有未完成的设备接入，请继续或取消下方流程，不要另建邀请。",
		"pairing-failed":                 "此次接入未完成。请检查两台设备的进度，使用继续或取消恢复原流程。",
		"pairing-waiting":                "正在等待另一台设备确认。请到另一台设备继续接入，再回到这里继续。",
		"pairing-expired":                "邀请或确认窗口已过期。请先取消原流程，再从已接入设备创建新邀请。",
		"busy":                           "另一轮同步或部署正在进行。请等待完成后刷新页面，再继续。",
		"local-state":                    "本机状态暂不可读取或更新。请确认当前用户可访问云拼配置，稍后刷新；现有词库与凭据保留。",
		"baseline-missing":               "请先从已接入设备导出并导入基础词库。同步不会自动取回另一台设备的静态词库。",
		"private-candidates-disabled":    "私人词库候选尚未启用，请点击“准备本机词库”，然后同步加载。",
		"baseline_confirmation_required": "本机已有不同的基础词库。请重新选择文件，并明确选择合并或替换；现有文件已保留。",
		"baseline_changed":               "基础词库已在其他操作中改变。请刷新、核对当前词库后重新选择文件。",
		"generated_snapshot":             "这是同步生成的词库，不能作为基础词库导入。请在原设备使用“导出基础词库”取得文件。",
		"import_validation_failed":       "词库文件未通过检查。请使用原设备导出的 UTF-8 五列 TSV 文件（不超过 64 MiB）。现有词库保留。",
		"initial_sync_required":          "请先完成一次账号同步，再恢复旧词，以保留已有删除记录。",
		"deleted_entry_conflict":         "旧词与账号中已有删除记录冲突，未自动恢复。删除继续生效；如确需恢复某词，请在个人词库中手动添加。",
		"legacy_import_changed":          "这份旧词备份已恢复过，之后又有修改。为保留这些修改，不再自动重放；请使用个人词库管理。",
		"legacy_baseline_conflict":       "旧词与基础词库的拼音不一致。请保留备份，先核对基础词库后再恢复。",
		"invalid_backup":                 "旧词备份未通过完整性检查。请保留原文件，暂不恢复。",
		"snapshot-missing":               "尚未生成同步词库，请先完成基础词库导入并同步。",
		"snapshot-not-applied":           "输入法尚未确认加载词库。请结束当前拼音输入，再点击同步；无需重新配对。",
		"background-unavailable":         "后台同步组件尚未安装或状态不可读。请使用当前版本安装器修复，再回来启用。",
		"background-disabled":            "后台同步已停用。完成本机词库加载后点击启用后台同步。",
		"background-not-running":         "后台同步尚未运行。请点击启用后台同步，然后刷新检查。",
		"local-permissions":              "当前用户无法更新词库或后台设置。请修复本机云拼安装权限后重试，无需更换同步账号。",
	}[code]
}

func (handler *settingsHandler) postOnboarding(response http.ResponseWriter, request *http.Request) {
	kind := strings.TrimPrefix(request.URL.Path, handler.base+"/setup/")
	if kind == "baseline-import" {
		handler.postBaselineUpload(response, request)
		return
	}
	if !handler.parseForm(response, request) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 180*time.Second)
	defer cancel()
	if kind == "baseline-export" || kind == "baseline-empty" || kind == "legacy-import" {
		handler.postBaselineAction(response, request.WithContext(ctx), kind)
		return
	}
	if kind == "prepare-vocabulary" || kind == "enable-background" {
		operations, ok := handler.operations.(settingsActivationOperations)
		if !ok {
			http.NotFound(response, request)
			return
		}
		var err error
		notice := "vocabulary-prepared"
		if kind == "prepare-vocabulary" {
			err = operations.PrepareVocabulary(ctx)
		} else {
			err = operations.EnableBackground(ctx)
			notice = "background-enabled"
		}
		if err != nil {
			handler.redirectError(response, request, "local-state", err)
			return
		}
		handler.redirect(response, request, "notice", notice)
		return
	}
	// Only the actions presented by the wizard are reachable over HTTP.
	switch kind {
	case "server-check", "server-save", "login", "register", "pairing-invite", "pairing-join", "pairing-continue", "pairing-cancel":
	default:
		http.NotFound(response, request)
		return
	}
	operations, ok := handler.operations.(settingsOnboardingOperations)
	if !ok {
		http.NotFound(response, request)
		return
	}
	action := settingsOnboardingAction{Kind: kind, Server: request.PostForm.Get("server"),
		AllowPrivateHTTP: request.PostForm.Get("allow_private_http") == "on", ConfirmServerChange: request.PostForm.Get("confirm_server_change") == "on",
		Username: request.PostForm.Get("username"), Password: request.PostForm.Get("password"), Invitation: request.PostForm.Get("invitation")}
	result, err := operations.ApplyOnboarding(ctx, action)
	// Do not retain sensitive form values in request maps or the render model.
	request.PostForm.Del("password")
	request.Form.Del("password")
	request.PostForm.Del("invitation")
	request.Form.Del("invitation")
	action.Password, action.Invitation = "", ""
	if err != nil {
		handler.redirectError(response, request, "pairing-failed", err)
		return
	}
	if kind == "pairing-invite" {
		if result.Invitation == "" {
			handler.redirect(response, request, "error", "pairing-failed")
			return
		}
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = settingsInvitationTemplate.Execute(response, struct{ Base, Invitation string }{handler.base, result.Invitation})
		return
	}
	if kind == "server-check" {
		endpoint, err := syncclient.ParseEndpoint(action.Server, syncclient.EndpointPolicy{AllowPrivateHTTP: action.AllowPrivateHTTP})
		if err != nil {
			handler.redirect(response, request, "error", "server-invalid")
			return
		}
		handler.renderOnboarding(response, request, settingsRenderOptions{Notice: "服务器可以连接；地址尚未保存。请点击保存服务器继续。", ServerDraft: endpoint.String(), AllowPrivateHTTP: action.AllowPrivateHTTP})
		return
	}
	notice := "pairing-progress"
	switch kind {
	case "server-save":
		notice = "server-saved"
	case "login", "register":
		notice = "logged-in"
	case "pairing-cancel":
		notice = "pairing-cancelled"
	}
	handler.redirect(response, request, "notice", notice)
}

func (handler *settingsHandler) postBaselineAction(response http.ResponseWriter, request *http.Request, kind string) {
	operations, ok := handler.operations.(settingsBaselineOperations)
	if !ok {
		http.NotFound(response, request)
		return
	}
	var err error
	notice := "baseline-imported"
	switch kind {
	case "baseline-export":
		var contents []byte
		contents, _, err = operations.ExportBaseline(request.Context())
		defer zeroSettingsBytes(contents)
		if err == nil {
			response.Header().Set("Content-Type", "text/tab-separated-values; charset=utf-8")
			response.Header().Set("Content-Disposition", `attachment; filename="yunpin-baseline.tsv"`)
			_, _ = response.Write(contents)
			return
		}
	case "baseline-empty":
		if request.PostForm.Get("confirm_empty") != "on" {
			http.Error(response, "confirmation required", http.StatusBadRequest)
			return
		}
		_, err = operations.InitializeEmptyBaseline(request.Context())
	case "legacy-import":
		_, err = operations.ImportLegacy(request.Context(), request.PostForm.Get("backup"))
		notice = "legacy-imported"
	}
	if err != nil {
		handler.redirect(response, request, "error", desktopagent.OnboardingImportErrorCode(err))
		return
	}
	handler.redirect(response, request, "notice", notice)
}

func (handler *settingsHandler) postBaselineUpload(response http.ResponseWriter, request *http.Request) {
	operations, ok := handler.operations.(settingsBaselineOperations)
	if !ok {
		http.NotFound(response, request)
		return
	}
	// Stream multipart parts into bounded memory; uploaded filenames never become paths.
	request.Body = http.MaxBytesReader(response, request.Body, desktopagent.MaxBaselineImportBytes+settingsMaxFormBody)
	reader, err := request.MultipartReader()
	if err != nil {
		http.Error(response, "invalid upload", http.StatusBadRequest)
		return
	}
	fields := make(map[string]string)
	var contents []byte
	defer func() { zeroSettingsBytes(contents) }()
	seen := make(map[string]bool)
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			http.Error(response, "invalid upload", http.StatusBadRequest)
			return
		}
		name := part.FormName()
		if seen[name] || (name != "file" && name != "mode" && name != "expected_sha256") {
			_ = part.Close()
			http.Error(response, "invalid upload fields", http.StatusBadRequest)
			return
		}
		seen[name] = true
		limit := int64(256)
		if name == "file" {
			limit = desktopagent.MaxBaselineImportBytes
		}
		data, readErr := io.ReadAll(io.LimitReader(part, limit+1))
		_ = part.Close()
		if readErr != nil || int64(len(data)) > limit {
			zeroSettingsBytes(data)
			http.Error(response, "upload too large", http.StatusRequestEntityTooLarge)
			return
		}
		if name == "file" {
			contents = data
		} else {
			fields[name] = string(data)
			zeroSettingsBytes(data)
		}
	}
	if !seen["file"] || len(contents) == 0 {
		http.Error(response, "file required", http.StatusBadRequest)
		return
	}
	mode := fields["mode"]
	if mode != "" && mode != "create" && mode != "merge" && mode != "replace" {
		http.Error(response, "invalid import mode", http.StatusBadRequest)
		return
	}
	_, err = operations.ImportBaseline(request.Context(), contents, desktopagent.BaselineImportOptions{Mode: mode, ExpectedSHA256: fields["expected_sha256"]})
	if err != nil {
		handler.redirect(response, request, "error", desktopagent.OnboardingImportErrorCode(err))
		return
	}
	handler.redirect(response, request, "notice", "baseline-imported")
}

var settingsInvitationTemplate = template.Must(template.New("invitation").Parse(`<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>添加设备 · 云拼</title><style>body{font-family:system-ui;max-width:700px;margin:40px auto;padding:20px}textarea{box-sizing:border-box;width:100%;min-height:180px}p{line-height:1.7}</style><h1>在新设备上提交邀请</h1><p>请复制下面的邀请，在新设备的云拼设置中粘贴。提交后，回到本机继续接入并批准，再在新设备继续，最后回本机完成确认。</p><p>邀请仅在本次页面显示，请只交给你要添加的设备。不要刷新此页面；关闭后仍可继续或取消原接入流程。</p><label for="invitation">设备邀请</label><textarea id="invitation" readonly spellcheck="false" autocomplete="off">{{.Invitation}}</textarea><p><a href="{{.Base}}/">返回设置，继续设备接入</a></p></html>`))

const settingsOnboardingStyle = `<style>.steps{counter-reset:step}.step{border-top:1px solid #ddd;margin-top:20px;padding-top:18px}.step h3{font-size:16px}.step h3:before{counter-increment:step;content:counter(step) " · ";color:#ce5424}.stack{display:grid;gap:12px}.stack label{display:grid;gap:6px}.stack label.check{display:flex;align-items:flex-start;gap:8px}.stack input:not([type=checkbox]),textarea,select{box-sizing:border-box;width:100%;padding:9px;border:1px solid #aaa;border-radius:8px;font:inherit}textarea{min-height:110px;resize:vertical}.actions{display:flex;gap:9px;flex-wrap:wrap;align-items:center}details{margin:12px 0}summary{cursor:pointer;padding:6px 0}.status-list{display:grid;grid-template-columns:1fr auto;gap:10px}.status-list dt,.status-list dd{margin:0}.step p{line-height:1.6}button{min-height:38px}.submit-progress{margin-left:8px}a{color:#d35520}@media(max-width:560px){body{padding:20px 10px}.card{padding:16px}.grid{grid-template-columns:1fr}.row{align-items:flex-start}}@media(prefers-color-scheme:dark){.step{border-color:#444}.stack input:not([type=checkbox]),textarea,select{background:#17181a;color:#eee;border-color:#555}}</style>`

const settingsSubmitScript = `<script nonce="{{.Nonce}}">document.addEventListener('submit',function(event){var form=event.target;if(form.dataset.download==='true')return;event.preventDefault();if(form.dataset.submitting==='true')return;form.dataset.submitting='true';var submitter=event.submitter;if(submitter&&submitter.hasAttribute('formaction'))form.action=submitter.formAction;if(submitter&&submitter.name){var field=document.createElement('input');field.type='hidden';field.name=submitter.name;field.value=submitter.value;form.appendChild(field);}form.querySelectorAll('button[type=submit]').forEach(function(button){button.disabled=true;});var status=document.createElement('span');status.className='submit-progress';status.setAttribute('role','status');status.textContent='正在处理，请等待完成…';form.appendChild(status);HTMLFormElement.prototype.submit.call(form);});</script>`

const settingsOnboardingTemplate = `{{define "onboarding"}}{{$base := .Base}}{{with .Onboarding}}<section class="card steps" aria-labelledby="setup-title"><h2 id="setup-title">同步接入</h2>
{{if .Complete}}<p class="notice">本机接入完成。已复用原有身份，无需重新登录或配对。</p><p class="muted">请在实际输入框试用一个已同步词语，确认候选符合预期。</p>{{else}}<p>按下面的进度继续，关闭页面后也可恢复。已保存的账号、设备身份和词库会继续使用。</p>{{end}}
{{if .Problem}}<p class="problem">{{.Problem}}</p>{{end}}
{{if .Available}}
<div class="step"><h3>同步服务器</h3><details {{if not .State.ServerConfigured}}open{{end}}><summary>{{if .State.ServerConfigured}}已配置 · {{.State.Server}}{{else}}填写服务器地址{{end}}</summary><form class="stack" method="post" action="{{$base}}/setup/server-save"><label>服务器地址<input type="url" name="server" value="{{.State.Server}}" placeholder="https://sync.example.com" maxlength="2048" required autocomplete="url"></label><label class="check"><input type="checkbox" name="allow_private_http" {{if .State.AllowPrivateHTTP}}checked{{end}}>允许受信任的局域网 HTTP 地址</label>{{if .State.CredentialPresent}}<label class="check"><input type="checkbox" name="confirm_server_change">这是同一同步服务的地址修正</label><p class="muted">保存地址修正会使用现有身份验证原服务；不能用此入口迁移到其他同步服务。测试连接不会发送设备凭据。</p>{{end}}<div class="actions"><button type="submit" class="secondary" formaction="{{$base}}/setup/server-check">测试连接</button><button type="submit">保存服务器</button></div></form></details></div>
<div class="step"><h3>账号与设备</h3>
{{if .State.SessionValid}}<p>已登录 {{.State.Username}}，继续使用现有会话。</p>{{else}}<details><summary>使用已有账号登录（账号管理）</summary><p class="muted">已配对设备的日常同步无需再次登录。仅登录账号不会替代设备邀请确认。</p><form class="stack" method="post" action="{{$base}}/setup/login"><label>用户名<input type="text" name="username" autocomplete="username" maxlength="64" required></label><label>密码<input type="password" name="password" autocomplete="current-password" maxlength="4096" required></label><button type="submit" {{if not .State.ServerConfigured}}disabled{{end}}>登录并保存会话</button></form></details>{{end}}
{{if .State.PairingPending}}<p class="notice">{{.PairingDescription}}</p><div class="actions"><form method="post" action="{{$base}}/setup/pairing-continue"><button type="submit">继续设备接入</button></form>{{if .CanCancel}}<form method="post" action="{{$base}}/setup/pairing-cancel"><button class="secondary" type="submit">取消本次接入</button></form>{{end}}</div>
{{else if .State.Paired}}<p>此设备已接入，无需重新配对。</p><details><summary>添加另一台设备</summary><p>在本机创建邀请后，交给新设备，再按两边的提示完成确认。</p><form method="post" action="{{$base}}/setup/pairing-invite"><button type="submit">创建设备邀请</button></form></details>
{{else}}<p>请在一台已经接入的设备打开云拼设置，选择“添加另一台设备”，将邀请粘贴到这里。</p><form class="stack" method="post" action="{{$base}}/setup/pairing-join"><label>来自已有设备的邀请<textarea name="invitation" maxlength="12288" autocomplete="off" spellcheck="false" required></textarea></label><button type="submit" {{if not .State.ServerConfigured}}disabled{{end}}>加入现有同步账号</button></form>{{end}}</div>
<div class="step"><h3>基础词库</h3><p>基础词库是静态词条；同步服务传递个人词语与学习变更。新设备请先从原设备导出基础词库，再在这里导入。</p>
{{if .BaselineAvailable}}{{if .Baseline.Exists}}<p>已导入 {{.Baseline.Rows}} 条基础词条。</p><form data-download="true" method="post" action="{{$base}}/setup/baseline-export"><button class="secondary" type="submit">导出基础词库</button></form>{{else}}<p class="problem">尚未导入基础词库，首次同步暂不可执行。</p>{{end}}
<details {{if not .Baseline.Exists}}open{{end}}><summary>{{if .Baseline.Exists}}导入或更新基础词库{{else}}选择原设备导出的文件{{end}}</summary><form class="stack" method="post" enctype="multipart/form-data" action="{{$base}}/setup/baseline-import"><label>基础词库文件（UTF-8 TSV，最多 64 MiB）<input type="file" name="file" accept=".tsv,text/tab-separated-values" required></label>{{if .Baseline.Exists}}<label>已有词库的处理方式<select name="mode"><option value="create">仅检查；不同内容先提示</option><option value="merge">合并，保留现有词条</option><option value="replace">替换基础词库，保留备份</option></select></label><input type="hidden" name="expected_sha256" value="{{.Baseline.SHA256}}">{{else}}<input type="hidden" name="mode" value="create">{{end}}<button type="submit">导入基础词库</button></form></details>
{{if not .Baseline.Exists}}<details><summary>我明确不需要原设备的静态词库</summary><p>空白基础词库只保留账号同步的个人词语，不会自动取回原设备的静态词条。现有本机旧词会先备份，首次同步后可恢复。</p><form class="stack" method="post" action="{{$base}}/setup/baseline-empty"><label class="check"><input type="checkbox" name="confirm_empty" required>我确认使用空白基础词库</label><button class="secondary" type="submit">创建空白基础词库并保留旧词备份</button></form></details>{{end}}
{{range .Baseline.PendingLegacy}}<div class="phrase"><p>有一份本机旧词备份（{{.Rows}} 条）。完成首次同步后恢复，保留使用次数与置顶，并遵守账号已有删除记录。</p><form method="post" action="{{$base}}/setup/legacy-import"><input type="hidden" name="backup" value="{{.Name}}"><button class="secondary" type="submit">恢复这份本机旧词</button></form></div>{{end}}
{{else}}<p class="problem">基础词库状态暂不可读，请稍后刷新。</p>{{end}}</div>
<div class="step"><h3>在本机加载并持续同步</h3>{{if .ActivationAvailable}}<dl class="status-list"><dt>本机词库功能</dt><dd>{{if .Activation.PrivateCandidatesEnabled}}已启用{{else}}尚未启用{{end}}</dd><dt>同步词库</dt><dd>{{if .Activation.SnapshotPresent}}已生成{{else}}尚未生成{{end}}</dd><dt>输入法加载确认</dt><dd>{{if .Activation.SnapshotApplied}}已确认{{else}}尚未确认{{end}}</dd><dt>后台同步</dt><dd>{{if not .Activation.BackgroundAvailable}}状态不可读{{else if not .Activation.BackgroundEnabled}}已停用{{else if .Activation.BackgroundRunning}}正在运行{{else}}未运行{{end}}</dd></dl><p class="muted">第一次同步可能需要约 1–3 分钟。正在输入拼音时，词库加载会等输入结束后再完成。</p><div class="actions"><form method="post" action="{{$base}}/setup/prepare-vocabulary"><button class="secondary" type="submit">准备本机词库</button></form><form method="post" action="{{$base}}/sync"><button type="submit" {{if not .Activation.BaselinePresent}}disabled{{end}} {{if not .State.Paired}}disabled{{end}}>{{if .Activation.LastSuccessAt}}同步并加载词库{{else}}首次同步并加载{{end}}</button></form><form method="post" action="{{$base}}/setup/enable-background"><button type="submit" {{if not .State.Paired}}disabled{{end}}>启用后台同步</button></form></div>{{else}}<p class="problem">本机加载与后台组件状态暂不可读，请检查安装后刷新。</p>{{end}}</div>
{{end}}<p><a href="{{$base}}/">刷新接入进度</a></p></section>{{end}}{{end}}`
