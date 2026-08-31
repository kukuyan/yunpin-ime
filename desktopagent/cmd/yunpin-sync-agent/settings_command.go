// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/kukuyan/yunpin-ime/desktopagent"
)

const (
	settingsIdleTimeout = 30 * time.Minute
	settingsMaxFormBody = 16 << 10
)

type settingsOperations interface {
	LoadGuards(context.Context) (desktopagent.GuardSettings, error)
	ApplyGuards(context.Context, desktopagent.GuardSettings) error
	Status(context.Context) (desktopagent.Status, error)
	SyncNow(context.Context) (desktopagent.SyncSummary, error)
	ListVocabulary(context.Context) (desktopagent.VocabularySummary, error)
	AddPhrase(context.Context, string, string, bool) error
	SetPhrasePinned(context.Context, string, string, bool) error
	RemovePhrase(context.Context, string, string) error
}

type localSettingsOperations struct {
	defaults    desktopagent.Paths
	agent       desktopagent.Agent
	overlayPath string
	secrets     *settingsSecretStore
}

// settingsSecretStore keeps one encoded credential copy for the bounded local
// settings process. A page render asks for status and vocabulary separately;
// without this cache those reads can present the same Keychain authorization
// twice. The cache is process-local, invalidated by mutations, and wiped when
// the settings server exits.
type settingsSecretStore struct {
	mu         sync.Mutex
	underlying desktopagent.SecretStore
	cached     []byte
	profile    string
	closed     bool
}

func zeroSettingsBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func (store *settingsSecretStore) load(ctx context.Context, profile string, noUI bool) ([]byte, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return nil, errors.New("settings credential session is closed")
	}
	if store.cached != nil && store.profile == profile {
		return append([]byte(nil), store.cached...), nil
	}
	store.invalidateLocked()
	loader := store.underlying.Load
	if noUI {
		background, ok := store.underlying.(interface {
			LoadWithoutUserInteraction(context.Context, string) ([]byte, error)
		})
		if !ok {
			return nil, errors.New("non-interactive credential access is unavailable")
		}
		loader = background.LoadWithoutUserInteraction
	}
	value, err := loader(ctx, profile)
	if err != nil {
		return nil, err
	}
	store.cached = append([]byte(nil), value...)
	store.profile = profile
	return value, nil
}

func (store *settingsSecretStore) Load(ctx context.Context, profile string) ([]byte, error) {
	return store.load(ctx, profile, false)
}

func (store *settingsSecretStore) LoadWithoutUserInteraction(ctx context.Context, profile string) ([]byte, error) {
	return store.load(ctx, profile, true)
}

func (store *settingsSecretStore) invalidateLocked() {
	zeroSettingsBytes(store.cached)
	store.cached = nil
	store.profile = ""
}

func (store *settingsSecretStore) Save(ctx context.Context, profile string, value []byte) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.invalidateLocked()
	return store.underlying.Save(ctx, profile, value)
}

func (store *settingsSecretStore) Delete(ctx context.Context, profile string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.invalidateLocked()
	return store.underlying.Delete(ctx, profile)
}

func (store *settingsSecretStore) Close() {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.invalidateLocked()
	store.closed = true
}

func newLocalSettingsOperations(defaults desktopagent.Paths) (*localSettingsOperations, error) {
	common := commonFlags{
		profile: desktopagent.DefaultProfile, state: defaults.StateDirectory,
		endpoint: defaults.EndpointConfigPath, database: defaults.DatabasePath,
		lock: defaults.LockPath, service: defaults.CredentialService,
		nativeEvents: defaults.NativeEventsPath, baseline: defaults.BaselinePath,
		snapshot: defaults.SnapshotPath, snapshotState: defaults.SnapshotStatePath,
	}
	_, agent, err := common.components()
	if err != nil {
		return nil, err
	}
	secrets := &settingsSecretStore{underlying: agent.Secrets}
	agent.Secrets = secrets
	overlayPath, err := desktopagent.RimeSettingsPath(defaults)
	if err != nil {
		secrets.Close()
		return nil, err
	}
	return &localSettingsOperations{defaults: defaults, agent: agent, overlayPath: overlayPath, secrets: secrets}, nil
}

func (operations *localSettingsOperations) LoadGuards(context.Context) (desktopagent.GuardSettings, error) {
	return desktopagent.LoadGuardSettings(operations.overlayPath)
}

func (operations *localSettingsOperations) ApplyGuards(ctx context.Context, settings desktopagent.GuardSettings) error {
	return desktopagent.WithProcessLock(operations.defaults.LockPath, func() error {
		_, err := desktopagent.ApplyGuardSettings(ctx, operations.overlayPath, settings, operations.agent.Reload)
		return err
	})
}

func (operations *localSettingsOperations) Status(ctx context.Context) (desktopagent.Status, error) {
	return operations.agent.Status(ctx)
}

func (operations *localSettingsOperations) SyncNow(ctx context.Context) (desktopagent.SyncSummary, error) {
	var summary desktopagent.SyncSummary
	err := desktopagent.WithProcessLock(operations.defaults.LockPath, func() error {
		var syncErr error
		summary, syncErr = operations.agent.SyncOnce(ctx)
		return syncErr
	})
	return summary, err
}

func (operations *localSettingsOperations) ListVocabulary(ctx context.Context) (desktopagent.VocabularySummary, error) {
	return operations.agent.ListVocabulary(ctx, desktopagent.VocabularyQuery{Limit: 50, IncludeText: true})
}

func (operations *localSettingsOperations) AddPhrase(ctx context.Context, text, pinyin string, pinned bool) error {
	return desktopagent.WithProcessLock(operations.defaults.LockPath, func() error {
		_, err := operations.agent.AddPhrase(ctx, text, pinyin, pinned)
		return err
	})
}

func (operations *localSettingsOperations) SetPhrasePinned(ctx context.Context, text, pinyin string, pinned bool) error {
	return desktopagent.WithProcessLock(operations.defaults.LockPath, func() error {
		_, err := operations.agent.SetPhrasePinned(ctx, text, pinyin, pinned)
		return err
	})
}

func (operations *localSettingsOperations) RemovePhrase(ctx context.Context, text, pinyin string) error {
	return desktopagent.WithProcessLock(operations.defaults.LockPath, func() error {
		_, err := operations.agent.RemovePhrase(ctx, text, pinyin)
		return err
	})
}

type settingsPageData struct {
	Base                string
	Notice              string
	Problem             string
	Guards              desktopagent.GuardSettings
	GuardsAvailable     bool
	HealthAvailable     bool
	HealthSummary       string
	LastSuccess         string
	PendingUploads      int64
	EventLogAvailable   bool
	VocabularyAvailable bool
	Vocabulary          desktopagent.VocabularySummary
}

var settingsPageTemplate = template.Must(template.New("settings").Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>云拼设置</title><style>
:root{color-scheme:light dark;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;background:#f4f5f7;color:#202124}body{margin:0;padding:32px 16px}.wrap{max-width:820px;margin:auto}.card{background:#fff;border:1px solid #ddd;border-radius:14px;padding:22px;margin:16px 0;box-shadow:0 2px 10px #0000000d}h1{margin:0 0 6px}h2{font-size:18px;margin:0 0 16px}.muted{color:#666}.row{display:flex;gap:12px;align-items:center;justify-content:space-between;margin:12px 0}.notice{padding:12px;border-radius:9px;background:#e8f5e9;color:#176b2c}.problem{padding:12px;border-radius:9px;background:#fff3e0;color:#8a4b00}button{border:0;border-radius:8px;padding:9px 15px;background:#f47b42;color:#fff;font-weight:600;cursor:pointer}button.secondary{background:#586174}button.danger{background:#b3261e}button:disabled{opacity:.45;cursor:not-allowed}input[type=text]{box-sizing:border-box;width:100%;padding:9px;border:1px solid #bbb;border-radius:8px}.grid{display:grid;grid-template-columns:1fr 1fr;gap:10px}.phrase{border-top:1px solid #eee;padding:12px 0}.phrase-actions{display:flex;gap:8px;flex-wrap:wrap}.inline{display:inline}.value{font-variant-numeric:tabular-nums}@media(prefers-color-scheme:dark){:root{background:#17181a;color:#eee}.card{background:#222428;border-color:#3b3d42}.muted{color:#aaa}.phrase{border-color:#3b3d42}input[type=text]{background:#17181a;color:#eee;border-color:#555}}
</style></head><body><main class="wrap"><h1>云拼设置</h1><div class="muted">本页面只在当前电脑的 127.0.0.1 临时开放，不显示账户、设备或恢复材料。</div>
{{if .Notice}}<p class="notice">{{.Notice}}</p>{{end}}{{if .Problem}}<p class="problem">{{.Problem}}</p>{{end}}
<section class="card"><h2>候选保护与纠错</h2><form method="post" action="{{.Base}}/guards">
<label class="row"><span><strong>短输入保护</strong><br><span class="muted">短拼音优先保留普通候选</span></span><input type="checkbox" name="short_input_guard" {{if .Guards.ShortInputGuard}}checked{{end}} {{if not .GuardsAvailable}}disabled{{end}}></label>
<label class="row"><span><strong>长纠错保护</strong><br><span class="muted">长输入不让自动纠错抢占第一候选</span></span><input type="checkbox" name="long_correction_guard" {{if .Guards.LongCorrectionGuard}}checked{{end}} {{if not .GuardsAvailable}}disabled{{end}}></label>
<label class="row"><span><strong>拼写纠错实验</strong><br><span class="muted">开启 typo correction；可随时关闭</span></span><input type="checkbox" name="typo_correction" {{if .Guards.TypoCorrection}}checked{{end}} {{if not .GuardsAvailable}}disabled{{end}}></label>
<button type="submit" {{if not .GuardsAvailable}}disabled{{end}}>保存并部署</button></form></section>
<section class="card"><h2>同步状态</h2><div class="row"><span>当前状态</span><span class="value">{{.HealthSummary}}</span></div><div class="row"><span>最近成功</span><span class="value">{{.LastSuccess}}</span></div><div class="row"><span>待上传变更</span><span class="value">{{.PendingUploads}}</span></div><div class="row"><span>诊断日志</span><span>{{if .EventLogAvailable}}可用{{else}}不可用（不阻断同步）{{end}}</span></div>
<form method="post" action="{{.Base}}/sync"><button type="submit">立即同步</button></form></section>
<section class="card"><h2>个人词库</h2><p class="muted">这里显示本机最多 50 条个人词语；修改会进入同一双向同步队列。</p>
<form method="post" action="{{.Base}}/phrases/add"><div class="grid"><input type="text" name="text" maxlength="32" placeholder="词语，例如：办公室" required><input type="text" name="pinyin" maxlength="96" placeholder="拼音，例如：ban gong shi" pattern="[a-z ']+" required></div><label class="row"><span>加入后置顶</span><input type="checkbox" name="pinned"></label><button type="submit">添加词语</button></form>
{{if .VocabularyAvailable}}{{range .Vocabulary.Entries}}<div class="phrase"><div><strong>{{.Text}}</strong> <span class="muted">{{.Pinyin}} · 使用 {{.UseCount}} 次{{if .Pinned}} · 已置顶{{end}}</span></div><div class="phrase-actions">
<form class="inline" method="post" action="{{$.Base}}/phrases/pin"><input type="hidden" name="text" value="{{.Text}}"><input type="hidden" name="pinyin" value="{{.Pinyin}}"><input type="hidden" name="pinned" value="{{if .Pinned}}false{{else}}true{{end}}"><button class="secondary" type="submit">{{if .Pinned}}取消置顶{{else}}置顶{{end}}</button></form>
<form class="inline" method="post" action="{{$.Base}}/phrases/remove"><input type="hidden" name="text" value="{{.Text}}"><input type="hidden" name="pinyin" value="{{.Pinyin}}"><button class="danger" type="submit">删除</button></form></div></div>{{else}}<p class="muted">词库暂无条目。</p>{{end}}{{else}}<p class="problem">个人词库当前不可读取。</p>{{end}}
</section></main></body></html>`))

type settingsHandler struct {
	base        string
	allowedHost string
	operations  settingsOperations
}

func newSettingsHandler(token, allowedHost string, operations settingsOperations) (http.Handler, error) {
	if token == "" || strings.ContainsAny(token, "/?#") || allowedHost == "" || operations == nil {
		return nil, errors.New("settings handler configuration is invalid")
	}
	return &settingsHandler{base: "/" + token, allowedHost: allowedHost, operations: operations}, nil
}

func (handler *settingsHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	response.Header().Set("Referrer-Policy", "no-referrer")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.Header().Set("X-Frame-Options", "DENY")
	if request.Host != handler.allowedHost || !strings.HasPrefix(request.URL.Path, handler.base+"/") {
		http.NotFound(response, request)
		return
	}
	switch {
	case request.Method == http.MethodGet && request.URL.Path == handler.base+"/":
		handler.render(response, request)
	case request.Method == http.MethodPost && request.URL.Path == handler.base+"/guards":
		handler.postGuards(response, request)
	case request.Method == http.MethodPost && request.URL.Path == handler.base+"/sync":
		handler.postSync(response, request)
	case request.Method == http.MethodPost && request.URL.Path == handler.base+"/phrases/add":
		handler.postPhraseAdd(response, request)
	case request.Method == http.MethodPost && request.URL.Path == handler.base+"/phrases/pin":
		handler.postPhrasePin(response, request)
	case request.Method == http.MethodPost && request.URL.Path == handler.base+"/phrases/remove":
		handler.postPhraseRemove(response, request)
	default:
		http.NotFound(response, request)
	}
}

func settingsNotice(code string) string {
	switch code {
	case "guards-saved":
		return "候选设置已保存并完成部署。"
	case "sync-complete":
		return "本轮同步已完成。"
	case "phrase-added":
		return "词语已加入个人词库。"
	case "phrase-updated":
		return "词语置顶状态已更新。"
	case "phrase-removed":
		return "词语已从个人词库移除。"
	default:
		return ""
	}
}

func settingsProblem(code string) string {
	switch code {
	case "busy":
		return "另一轮同步或部署正在进行，请稍后再试。"
	case "guards-unavailable":
		return "设置未保存：Rime 配置当前不可读取或部署未完成。"
	case "sync-failed":
		return "本轮同步未完成，请查看下方健康状态后重试。"
	case "phrase-not-found":
		return "该词语已不存在，请刷新页面。"
	case "phrase-failed":
		return "词库操作未完成，请检查词语、拼音或本地同步状态。"
	default:
		return ""
	}
}

func formatSettingsTime(milliseconds int64) string {
	if milliseconds <= 0 {
		return "尚无"
	}
	return time.UnixMilli(milliseconds).Local().Format("2006-01-02 15:04:05")
}

func healthSummary(status desktopagent.Status, err error) (bool, string, string, int64, bool) {
	if err != nil || !status.HealthAvailable {
		return false, "健康记录不可用", "未知", 0, status.EventLogAvailable
	}
	if status.Health.LastEventAt == 0 {
		return true, "尚未完成过同步", "尚无", status.Health.PendingUploads, status.EventLogAvailable
	}
	description := map[string]string{
		"sync_complete":      "同步正常",
		"sync_deferred_busy": "输入法忙，已自动延后",
		"sync_failed":        "同步失败（" + status.Health.LastFailureClass + "）",
	}[status.Health.LastEventCode]
	if description == "" {
		description = "状态未知"
	}
	return true, description, formatSettingsTime(status.Health.LastSuccessAt), status.Health.PendingUploads, status.EventLogAvailable
}

func (handler *settingsHandler) render(response http.ResponseWriter, request *http.Request) {
	guards, guardErr := handler.operations.LoadGuards(request.Context())
	status, statusErr := handler.operations.Status(request.Context())
	healthAvailable, summary, lastSuccess, pending, logAvailable := healthSummary(status, statusErr)
	var vocabulary desktopagent.VocabularySummary
	vocabularyErr := statusErr
	if statusErr == nil {
		vocabulary, vocabularyErr = handler.operations.ListVocabulary(request.Context())
	}
	data := settingsPageData{
		Base: handler.base, Notice: settingsNotice(request.URL.Query().Get("notice")),
		Problem: settingsProblem(request.URL.Query().Get("error")), Guards: guards,
		GuardsAvailable: guardErr == nil, HealthAvailable: healthAvailable,
		HealthSummary: summary, LastSuccess: lastSuccess, PendingUploads: pending,
		EventLogAvailable: logAvailable, VocabularyAvailable: vocabularyErr == nil,
		Vocabulary: vocabulary,
	}
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := settingsPageTemplate.Execute(response, data); err != nil {
		http.Error(response, "settings page unavailable", http.StatusInternalServerError)
	}
}

func (handler *settingsHandler) parseForm(response http.ResponseWriter, request *http.Request) bool {
	if !strings.HasPrefix(strings.ToLower(request.Header.Get("Content-Type")), "application/x-www-form-urlencoded") {
		http.Error(response, "unsupported form", http.StatusUnsupportedMediaType)
		return false
	}
	request.Body = http.MaxBytesReader(response, request.Body, settingsMaxFormBody)
	if err := request.ParseForm(); err != nil {
		http.Error(response, "invalid form", http.StatusBadRequest)
		return false
	}
	return true
}

func (handler *settingsHandler) redirect(response http.ResponseWriter, request *http.Request, key, value string) {
	destination := handler.base + "/?" + url.Values{key: []string{value}}.Encode()
	http.Redirect(response, request, destination, http.StatusSeeOther)
}

func (handler *settingsHandler) redirectError(response http.ResponseWriter, request *http.Request, operation string, err error) {
	if errors.Is(err, desktopagent.ErrAlreadyRunning) {
		handler.redirect(response, request, "error", "busy")
		return
	}
	if errors.Is(err, desktopagent.ErrPhraseNotFound) {
		handler.redirect(response, request, "error", "phrase-not-found")
		return
	}
	handler.redirect(response, request, "error", operation)
}

func (handler *settingsHandler) postGuards(response http.ResponseWriter, request *http.Request) {
	if !handler.parseForm(response, request) {
		return
	}
	err := handler.operations.ApplyGuards(request.Context(), desktopagent.GuardSettings{
		ShortInputGuard:     request.PostForm.Has("short_input_guard"),
		LongCorrectionGuard: request.PostForm.Has("long_correction_guard"),
		TypoCorrection:      request.PostForm.Has("typo_correction"),
	})
	if err != nil {
		handler.redirectError(response, request, "guards-unavailable", err)
		return
	}
	handler.redirect(response, request, "notice", "guards-saved")
}

func (handler *settingsHandler) postSync(response http.ResponseWriter, request *http.Request) {
	if !handler.parseForm(response, request) {
		return
	}
	if _, err := handler.operations.SyncNow(request.Context()); err != nil {
		handler.redirectError(response, request, "sync-failed", err)
		return
	}
	handler.redirect(response, request, "notice", "sync-complete")
}

func (handler *settingsHandler) postPhraseAdd(response http.ResponseWriter, request *http.Request) {
	if !handler.parseForm(response, request) {
		return
	}
	if err := handler.operations.AddPhrase(request.Context(), request.PostForm.Get("text"),
		request.PostForm.Get("pinyin"), request.PostForm.Has("pinned")); err != nil {
		handler.redirectError(response, request, "phrase-failed", err)
		return
	}
	handler.redirect(response, request, "notice", "phrase-added")
}

func (handler *settingsHandler) postPhrasePin(response http.ResponseWriter, request *http.Request) {
	if !handler.parseForm(response, request) {
		return
	}
	pinned := request.PostForm.Get("pinned") == "true"
	if err := handler.operations.SetPhrasePinned(request.Context(), request.PostForm.Get("text"),
		request.PostForm.Get("pinyin"), pinned); err != nil {
		handler.redirectError(response, request, "phrase-failed", err)
		return
	}
	handler.redirect(response, request, "notice", "phrase-updated")
}

func (handler *settingsHandler) postPhraseRemove(response http.ResponseWriter, request *http.Request) {
	if !handler.parseForm(response, request) {
		return
	}
	if err := handler.operations.RemovePhrase(request.Context(), request.PostForm.Get("text"),
		request.PostForm.Get("pinyin")); err != nil {
		handler.redirectError(response, request, "phrase-failed", err)
		return
	}
	handler.redirect(response, request, "notice", "phrase-removed")
}

func newSettingsSessionToken() (string, error) {
	value := make([]byte, 24)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		return "", errors.New("could not initialize local settings session")
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func serveSettings(ctx context.Context, listener net.Listener, operations settingsOperations, opener func(string) error, idle time.Duration) error {
	if listener == nil || operations == nil || opener == nil || idle <= 0 {
		return errors.New("settings server configuration is invalid")
	}
	address := listener.Addr().String()
	token, err := newSettingsSessionToken()
	if err != nil {
		listener.Close()
		return err
	}
	handler, err := newSettingsHandler(token, address, operations)
	if err != nil {
		listener.Close()
		return err
	}
	server := &http.Server{
		Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: time.Minute,
		ErrorLog: log.New(io.Discard, "", 0),
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(listener) }()
	pageURL := "http://" + address + "/" + token + "/"
	if err := opener(pageURL); err != nil {
		_ = server.Close()
		<-serveResult
		return fmt.Errorf("open local settings page: %w", err)
	}
	timer := time.NewTimer(idle)
	defer timer.Stop()
	var result error
	select {
	case <-ctx.Done():
		result = nil
	case <-timer.C:
		result = nil
	case result = <-serveResult:
		if errors.Is(result, http.ErrServerClosed) {
			result = nil
		}
		return result
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownContext)
	serveErr := <-serveResult
	if result == nil && !errors.Is(serveErr, http.ErrServerClosed) {
		result = serveErr
	}
	return result
}

func commandSettings(ctx context.Context, defaults desktopagent.Paths, arguments []string) error {
	set := flag.NewFlagSet("settings", flag.ContinueOnError)
	if err := parse(set, arguments); err != nil {
		return err
	}
	operations, err := newLocalSettingsOperations(defaults)
	if err != nil {
		return err
	}
	defer operations.secrets.Close()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return errors.New("could not start the local settings page")
	}
	return serveSettings(ctx, listener, operations, openLocalSettingsURL, settingsIdleTimeout)
}
