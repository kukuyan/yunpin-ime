// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kukuyan/yunpin-ime/desktopagent"
)

type fakeOnboardingUI struct {
	fakeSettingsOperations
	state                                                                settingsOnboardingState
	activation                                                           settingsActivationState
	baseline                                                             desktopagent.BaselineImportStatus
	stateErr, actionErr, baselineErr                                     error
	result                                                               settingsOnboardingResult
	actions                                                              []settingsOnboardingAction
	imported                                                             []byte
	importOptions                                                        desktopagent.BaselineImportOptions
	importCalls, exportCalls, legacyCalls, prepareCalls, backgroundCalls int
	legacyName                                                           string
	deadline                                                             time.Duration
}

func (fake *fakeOnboardingUI) OnboardingStatus(context.Context) (settingsOnboardingState, error) {
	return fake.state, fake.stateErr
}
func (fake *fakeOnboardingUI) ApplyOnboarding(ctx context.Context, action settingsOnboardingAction) (settingsOnboardingResult, error) {
	fake.actions = append(fake.actions, action)
	if deadline, ok := ctx.Deadline(); ok {
		fake.deadline = time.Until(deadline)
	}
	return fake.result, fake.actionErr
}
func (fake *fakeOnboardingUI) ActivationStatus(context.Context) (settingsActivationState, error) {
	return fake.activation, nil
}
func (fake *fakeOnboardingUI) PrepareVocabulary(context.Context) error {
	fake.prepareCalls++
	return fake.actionErr
}
func (fake *fakeOnboardingUI) EnableBackground(context.Context) error {
	fake.backgroundCalls++
	return fake.actionErr
}
func (fake *fakeOnboardingUI) BaselineStatus(context.Context) (desktopagent.BaselineImportStatus, error) {
	return fake.baseline, fake.baselineErr
}
func (fake *fakeOnboardingUI) ExportBaseline(context.Context) ([]byte, desktopagent.BaselineExportResult, error) {
	fake.exportCalls++
	return []byte("phrase\tpinyin\tsource\tuse_count\tpinned\n"), desktopagent.BaselineExportResult{}, fake.baselineErr
}
func (fake *fakeOnboardingUI) ImportBaseline(_ context.Context, contents []byte, options desktopagent.BaselineImportOptions) (desktopagent.BaselineImportResult, error) {
	fake.importCalls++
	fake.imported = append([]byte(nil), contents...)
	fake.importOptions = options
	return desktopagent.BaselineImportResult{}, fake.baselineErr
}

func (fake *fakeOnboardingUI) InitializeEmptyBaseline(context.Context) (desktopagent.BaselineImportResult, error) {
	fake.importCalls++
	fake.importOptions = desktopagent.BaselineImportOptions{Mode: "explicit-empty"}
	return desktopagent.BaselineImportResult{}, fake.baselineErr
}
func (fake *fakeOnboardingUI) ImportLegacy(_ context.Context, name string) (desktopagent.LegacyImportResult, error) {
	fake.legacyCalls++
	fake.legacyName = name
	return desktopagent.LegacyImportResult{}, fake.baselineErr
}

func onboardingUIHandler(t *testing.T, fake *fakeOnboardingUI) http.Handler {
	t.Helper()
	handler, err := newSettingsHandler("fixed-token", "127.0.0.1:43210", fake)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func uiResponse(handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func connectedOnboardingUI() *fakeOnboardingUI {
	return &fakeOnboardingUI{
		state:      settingsOnboardingState{ServerConfigured: true, Server: "https://sync.example.test", CredentialPresent: true, Paired: true, PairingState: "ready", BridgeConfigured: true},
		activation: settingsActivationState{BaselinePresent: true, SnapshotPresent: true, SnapshotApplied: true, PrivateCandidatesEnabled: true, BackgroundAvailable: true, BackgroundEnabled: true, BackgroundRunning: true, LastSuccessAt: time.Now().UnixMilli(), LastEventCode: "sync_complete"},
		baseline:   desktopagent.BaselineImportStatus{Exists: true, Rows: 94, SHA256: strings.Repeat("a", 64)},
	}
}

func TestSettingsOnboardingNewDeviceAndExistingIdentity(t *testing.T) {
	newDevice := &fakeOnboardingUI{}
	response := uiResponse(onboardingUIHandler(t, newDevice), settingsRequest(http.MethodGet, "/fixed-token/", nil))
	body := response.Body.String()
	for _, wanted := range []string{"同步接入", "保存服务器", "测试连接", "加入现有同步账号", "导入基础词库", "创建空白基础词库并保留旧词备份", "准备本机词库", "首次同步并加载", "启用后台同步"} {
		if !strings.Contains(body, wanted) {
			t.Errorf("missing %q", wanted)
		}
	}
	if strings.Contains(body, "本机接入完成") || newDevice.listCalls != 0 {
		t.Fatal("unpaired device was presented as complete or read vocabulary")
	}
	paired := connectedOnboardingUI()
	response = uiResponse(onboardingUIHandler(t, paired), settingsRequest(http.MethodGet, "/fixed-token/", nil))
	body = response.Body.String()
	if !strings.Contains(body, "本机接入完成") || !strings.Contains(body, "此设备已接入，无需重新配对") || strings.Contains(body, "name=\"invitation\"") {
		t.Fatal("existing device was not reused")
	}
	paired.state.SessionValid, paired.state.Username = true, "existing-user"
	body = uiResponse(onboardingUIHandler(t, paired), settingsRequest(http.MethodGet, "/fixed-token/", nil)).Body.String()
	if !strings.Contains(body, "existing-user") || strings.Contains(body, "type=\"password\"") {
		t.Fatal("valid existing login asked for password again")
	}
}

func TestSettingsOnboardingCompletionUsesLiveActivationAndFreshSuccess(t *testing.T) {
	tests := []struct {
		name, want string
		change     func(*fakeOnboardingUI)
	}{
		{"disabled", "后台同步已停用", func(f *fakeOnboardingUI) { f.activation.BackgroundEnabled = false }},
		{"stopped", "后台同步未运行", func(f *fakeOnboardingUI) { f.activation.BackgroundRunning = false }},
		{"stale", "最近成功记录已过期", func(f *fakeOnboardingUI) { f.activation.LastSuccessAt = time.Now().Add(-16 * time.Minute).UnixMilli() }},
		{"private-disabled", "等待准备本机词库", func(f *fakeOnboardingUI) { f.activation.PrivateCandidatesEnabled = false }},
		{"not-applied", "词库尚未由输入法确认加载", func(f *fakeOnboardingUI) { f.activation.SnapshotApplied = false }},
		{"missing-baseline", "等待导入基础词库", func(f *fakeOnboardingUI) { f.activation.BaselinePresent = false }},
		{"pending", "仍有变更等待上传", func(f *fakeOnboardingUI) { f.activation.PendingUploads = 2 }},
		{"failed-after-success", "最近一轮同步失败", func(f *fakeOnboardingUI) { f.activation.LastEventCode = "sync_failed" }},
		{"busy-after-success", "输入法忙", func(f *fakeOnboardingUI) { f.activation.LastEventCode = "sync_deferred_busy" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := connectedOnboardingUI()
			test.change(fake)
			body := uiResponse(onboardingUIHandler(t, fake), settingsRequest(http.MethodGet, "/fixed-token/", nil)).Body.String()
			if !strings.Contains(body, test.want) || strings.Contains(body, "本机接入完成") {
				t.Fatalf("incorrect live state for %s", test.name)
			}
		})
	}
}

func TestSettingsOnboardingFinalizationCannotOfferCancel(t *testing.T) {
	fake := connectedOnboardingUI()
	fake.state.PairingPending, fake.state.PairingRole, fake.state.PairingState = true, "creator", "finalize_pending"
	body := uiResponse(onboardingUIHandler(t, fake), settingsRequest(http.MethodGet, "/fixed-token/", nil)).Body.String()
	if !strings.Contains(body, "/setup/pairing-continue") || strings.Contains(body, "/setup/pairing-cancel") || strings.Contains(body, "本机接入完成") {
		t.Fatal("finalization offered unsafe cancellation or complete status")
	}
}

func TestSettingsAllPOSTsRequireExactOriginAndNoQuery(t *testing.T) {
	for _, path := range []string{"/sync", "/guards", "/setup/login", "/setup/pairing-join", "/setup/baseline-import", "/setup/baseline-export"} {
		for _, origin := range []string{"", "null", "http://evil.test", "http://127.0.0.1:43211"} {
			fake := &fakeOnboardingUI{}
			request := settingsRequest(http.MethodPost, "/fixed-token"+path, url.Values{})
			request.Header.Set("Origin", origin)
			response := uiResponse(onboardingUIHandler(t, fake), request)
			if response.Code != http.StatusForbidden || len(fake.actions) != 0 || fake.syncCalls != 0 || fake.exportCalls != 0 {
				t.Fatalf("%s accepted origin %q", path, origin)
			}
		}
	}
	fake := &fakeOnboardingUI{}
	response := uiResponse(onboardingUIHandler(t, fake), settingsRequest(http.MethodPost, "/fixed-token/setup/login?password=secret", url.Values{}))
	if response.Code != http.StatusForbidden || len(fake.actions) != 0 {
		t.Fatal("mutation accepted query credentials")
	}
}

func TestSettingsOnboardingSecretsOnlyInExplicitInvitationResponse(t *testing.T) {
	secret := "secret-</textarea><script>danger</script>"
	fake := &fakeOnboardingUI{actionErr: errors.New(secret)}
	handler := onboardingUIHandler(t, fake)
	response := uiResponse(handler, settingsRequest(http.MethodPost, "/fixed-token/setup/login", url.Values{"username": {"u"}, "password": {secret}}))
	if response.Code != http.StatusSeeOther || strings.Contains(response.Body.String()+response.Header().Get("Location"), secret) {
		t.Fatal("failed login leaked secret")
	}
	if len(fake.actions) != 1 || fake.actions[0].Password != secret || fake.deadline <= 0 || fake.deadline > 180*time.Second {
		t.Fatal("bounded login was not delivered correctly")
	}
	fake.actionErr, fake.result.Invitation = nil, secret
	response = uiResponse(handler, settingsRequest(http.MethodPost, "/fixed-token/setup/pairing-invite", url.Values{}))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "secret-&lt;/textarea&gt;") || strings.Contains(response.Body.String(), "<script>danger") || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("invitation is not escaped or private")
	}
	if response.Header().Get("Location") != "" {
		t.Fatal("invitation was redirected")
	}
	body := uiResponse(handler, settingsRequest(http.MethodGet, "/fixed-token/", nil)).Body.String()
	if strings.Contains(body, "secret-") {
		t.Fatal("GET replayed invitation")
	}
}

func TestSettingsServerCheckDraftAndSameServiceConfirmation(t *testing.T) {
	fake := connectedOnboardingUI()
	handler := onboardingUIHandler(t, fake)
	response := uiResponse(handler, settingsRequest(http.MethodPost, "/fixed-token/setup/server-check", url.Values{"server": {"https://other.example.test"}, "confirm_server_change": {"on"}}))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `value="https://other.example.test"`) || !strings.Contains(response.Body.String(), "地址尚未保存") {
		t.Fatal("server check lost unsaved draft")
	}
	if fake.actions[0].Kind != "server-check" || !fake.actions[0].ConfirmServerChange || fake.actions[0].Password != "" {
		t.Fatal("incorrect server action")
	}
	response = uiResponse(handler, settingsRequest(http.MethodPost, "/fixed-token/setup/server-check", url.Values{"server": {"https://user:secret@other.example.test?token=secret"}}))
	if response.Code != http.StatusSeeOther || strings.Contains(response.Body.String(), "secret") || strings.Contains(response.Header().Get("Location"), "secret") {
		t.Fatal("malformed server leaked embedded credentials")
	}
}

func baselineUploadRequest(t *testing.T, fields map[string]string, files ...[]byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	for _, contents := range files {
		part, err := writer.CreateFormFile("file", "../../private-secret.tsv")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:43210/fixed-token/setup/baseline-import", &body)
	request.Header.Set("Origin", "http://127.0.0.1:43210")
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func TestSettingsBaselineUploadPreservesCASAndRejectsAmbiguousFields(t *testing.T) {
	fake := connectedOnboardingUI()
	handler := onboardingUIHandler(t, fake)
	contents := []byte("phrase\tpinyin\tsource\tuse_count\tpinned\n")
	response := uiResponse(handler, baselineUploadRequest(t, map[string]string{"mode": "merge", "expected_sha256": strings.Repeat("a", 64)}, contents))
	if response.Code != http.StatusSeeOther || fake.importCalls != 1 || !bytes.Equal(fake.imported, contents) || fake.importOptions.Mode != "merge" || fake.importOptions.ExpectedSHA256 != strings.Repeat("a", 64) {
		t.Fatal("upload did not preserve contents and confirmation")
	}
	if strings.Contains(response.Body.String()+response.Header().Get("Location"), "private-secret") {
		t.Fatal("upload filename was reflected")
	}
	for _, request := range []*http.Request{
		baselineUploadRequest(t, map[string]string{"mode": "replace", "extra": "secret"}, contents),
		baselineUploadRequest(t, map[string]string{}, contents, contents),
		baselineUploadRequest(t, map[string]string{"mode": strings.Repeat("x", 257)}, contents),
	} {
		before := fake.importCalls
		response := uiResponse(handler, request)
		if response.Code < 400 || fake.importCalls != before {
			t.Fatal("ambiguous or oversized multipart field reached importer")
		}
	}
	fake.baselineErr = desktopagent.ErrGeneratedSnapshotImport
	response = uiResponse(handler, baselineUploadRequest(t, map[string]string{}, contents))
	if response.Header().Get("Location") != "/fixed-token/?error=generated_snapshot" {
		t.Fatal("import failure was not safely classified")
	}
}

func TestSettingsBaselineExportEmptyAndLegacyRoutes(t *testing.T) {
	fake := connectedOnboardingUI()
	handler := onboardingUIHandler(t, fake)
	response := uiResponse(handler, settingsRequest(http.MethodGet, "/fixed-token/setup/baseline-export", nil))
	if response.Code != http.StatusNotFound || fake.exportCalls != 0 {
		t.Fatal("export accepted GET")
	}
	response = uiResponse(handler, settingsRequest(http.MethodPost, "/fixed-token/setup/baseline-export", url.Values{}))
	if response.Code != http.StatusOK || response.Header().Get("Content-Disposition") != `attachment; filename="yunpin-baseline.tsv"` || !strings.HasPrefix(response.Body.String(), "phrase\tpinyin") {
		t.Fatal("invalid protected download")
	}
	response = uiResponse(handler, settingsRequest(http.MethodPost, "/fixed-token/setup/baseline-empty", url.Values{}))
	if response.Code != http.StatusBadRequest || fake.importCalls != 0 {
		t.Fatal("empty baseline missing confirmation was accepted")
	}
	response = uiResponse(handler, settingsRequest(http.MethodPost, "/fixed-token/setup/baseline-empty", url.Values{"confirm_empty": {"on"}}))
	if response.Code != http.StatusSeeOther || fake.importOptions.Mode != "explicit-empty" {
		t.Fatal("empty baseline did not use protected create flow")
	}
	backup := "legacy-private-" + strings.Repeat("b", 64) + ".tsv"
	fake.baseline.PendingLegacy = []desktopagent.LegacyBackupInfo{{Name: backup, Rows: 4}}
	body := uiResponse(handler, settingsRequest(http.MethodGet, "/fixed-token/", nil)).Body.String()
	if !strings.Contains(body, backup) || !strings.Contains(body, "恢复这份本机旧词") || strings.Contains(body, "本机接入完成") {
		t.Fatal("durable pending legacy restore was not offered")
	}
	fake.baselineErr = desktopagent.ErrLegacyInitialSyncRequired
	response = uiResponse(handler, settingsRequest(http.MethodPost, "/fixed-token/setup/legacy-import", url.Values{"backup": {backup}}))
	if fake.legacyName != backup || response.Header().Get("Location") != "/fixed-token/?error=initial_sync_required" {
		t.Fatal("legacy initial sync gate was lost")
	}
}

func TestSettingsNonceAndDuplicateSubmitProtection(t *testing.T) {
	fake := &fakeOnboardingUI{}
	handler := onboardingUIHandler(t, fake)
	first := uiResponse(handler, settingsRequest(http.MethodGet, "/fixed-token/", nil))
	second := uiResponse(handler, settingsRequest(http.MethodGet, "/fixed-token/", nil))
	csp := first.Header().Get("Content-Security-Policy")
	if first.Header().Get("Referrer-Policy") != "same-origin" {
		t.Fatal("form navigation must preserve exact same-origin Origin without exposing the token cross-origin")
	}
	_, suffix, ok := strings.Cut(csp, "script-src 'nonce-")
	if !ok {
		t.Fatal("missing script nonce policy")
	}
	nonce, _, _ := strings.Cut(suffix, "'")
	if nonce == "" || !strings.Contains(first.Body.String(), `nonce="`+nonce+`"`) || csp == second.Header().Get("Content-Security-Policy") {
		t.Fatal("script nonce missing or reused")
	}
	if !strings.Contains(first.Body.String(), "form.dataset.submitting==='true'") || !strings.Contains(first.Body.String(), "submitter.formAction") {
		t.Fatal("duplicate submit or server-check submit action not protected")
	}
}

func TestSettingsSyncRefusesImplicitBaselineFromLegacySnapshot(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	baseline := filepath.Join(directory, "baseline.tsv")
	snapshot := filepath.Join(directory, "private.tsv")
	original := []byte("phrase\tpinyin\tsource\tuse_count\tpinned\n旧词\tjiu ci\tmanual\t3\ttrue\n")
	if err := os.WriteFile(snapshot, original, 0600); err != nil {
		t.Fatal(err)
	}
	operations := &localSettingsOperations{defaults: desktopagent.Paths{BaselinePath: baseline, SnapshotPath: snapshot, SnapshotStatePath: filepath.Join(directory, "snapshot-state"), LockPath: filepath.Join(directory, "agent.lock")}}
	_, err := operations.SyncNow(context.Background())
	var safe *settingsOnboardingError
	if !errors.As(err, &safe) || safe.Code != "baseline-missing" {
		t.Fatalf("missing baseline did not stop before sync: %v", err)
	}
	if _, err := os.Stat(baseline); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("sync silently created baseline")
	}
	contents, err := os.ReadFile(snapshot)
	if err != nil || !bytes.Equal(contents, original) {
		t.Fatal("legacy snapshot changed")
	}
}

// An opt-in, synthetic-only browser fixture. It never reads platform paths,
// credentials, the native input method, or the production sync service.
type settingsPreviewResponse struct {
	http.ResponseWriter
	status, bytes int
}

func (response *settingsPreviewResponse) WriteHeader(status int) {
	if response.status == 0 {
		response.status = status
	}
	response.ResponseWriter.WriteHeader(status)
}

func (response *settingsPreviewResponse) Write(contents []byte) (int, error) {
	if response.status == 0 {
		response.WriteHeader(http.StatusOK)
	}
	n, err := response.ResponseWriter.Write(contents)
	response.bytes += n
	return n, err
}

func TestSettingsOnboardingPreview(t *testing.T) {
	if os.Getenv("YUNPIN_SETTINGS_PREVIEW") != "1" {
		t.Skip("opt-in synthetic browser preview")
	}
	fake := &fakeOnboardingUI{}
	if os.Getenv("YUNPIN_SETTINGS_PREVIEW_STATE") == "ready" {
		fake = connectedOnboardingUI()
	}
	if os.Getenv("YUNPIN_SETTINGS_PREVIEW_STATE") == "pending" {
		fake = connectedOnboardingUI()
		fake.state.PairingPending, fake.state.PairingRole, fake.state.PairingState = true, "creator", "invited"
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := newSettingsHandler("synthetic-browser-fixture", listener.Addr().String(), fake)
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	previewHandler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		origin := request.Header.Get("Origin")
		if len(origin) > 256 {
			origin = origin[:256]
		}
		t.Logf("SYNTHETIC_REQUEST %s %s origin=%q", request.Method, request.URL.Path, origin)
		tracked := &settingsPreviewResponse{ResponseWriter: response}
		handler.ServeHTTP(tracked, request)
		t.Logf("SYNTHETIC_RESPONSE status=%d bytes=%d", tracked.status, tracked.bytes)
	})
	server := &http.Server{Handler: previewHandler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Logf("SYNTHETIC_PREVIEW_URL=http://%s/synthetic-browser-fixture/ (expires in 120 seconds)", listener.Addr().String())
	timer := time.NewTimer(120 * time.Second)
	defer timer.Stop()
	select {
	case <-timer.C:
	case err := <-done:
		t.Fatalf("preview stopped: %v", err)
	}
	_ = server.Close()
	if err := <-done; !errors.Is(err, http.ErrServerClosed) {
		t.Fatal(err)
	}
}
