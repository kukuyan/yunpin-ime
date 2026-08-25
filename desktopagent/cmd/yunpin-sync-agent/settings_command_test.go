// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kukuyan/yunpin-ime/desktopagent"
	"github.com/kukuyan/yunpin-ime/localstore"
)

type fakeSettingsOperations struct {
	guards       desktopagent.GuardSettings
	applied      desktopagent.GuardSettings
	status       desktopagent.Status
	vocabulary   desktopagent.VocabularySummary
	operationErr error
	syncCalls    int
	addCalls     int
	pinCalls     int
	removeCalls  int
	lastText     string
	lastPinyin   string
	lastPinned   bool
}

func (operations *fakeSettingsOperations) LoadGuards(context.Context) (desktopagent.GuardSettings, error) {
	return operations.guards, nil
}

func (operations *fakeSettingsOperations) ApplyGuards(_ context.Context, settings desktopagent.GuardSettings) error {
	operations.applied = settings
	return operations.operationErr
}

func (operations *fakeSettingsOperations) Status(context.Context) (desktopagent.Status, error) {
	return operations.status, nil
}

func (operations *fakeSettingsOperations) SyncNow(context.Context) (desktopagent.SyncSummary, error) {
	operations.syncCalls++
	return desktopagent.SyncSummary{Rounds: 1}, operations.operationErr
}

func (operations *fakeSettingsOperations) ListVocabulary(context.Context) (desktopagent.VocabularySummary, error) {
	return operations.vocabulary, nil
}

func (operations *fakeSettingsOperations) AddPhrase(_ context.Context, text, pinyin string, pinned bool) error {
	operations.addCalls++
	operations.lastText, operations.lastPinyin, operations.lastPinned = text, pinyin, pinned
	return operations.operationErr
}

func (operations *fakeSettingsOperations) SetPhrasePinned(_ context.Context, text, pinyin string, pinned bool) error {
	operations.pinCalls++
	operations.lastText, operations.lastPinyin, operations.lastPinned = text, pinyin, pinned
	return operations.operationErr
}

func (operations *fakeSettingsOperations) RemovePhrase(_ context.Context, text, pinyin string) error {
	operations.removeCalls++
	operations.lastText, operations.lastPinyin = text, pinyin
	return operations.operationErr
}

func testSettingsHandler(t *testing.T, operations *fakeSettingsOperations) http.Handler {
	t.Helper()
	handler, err := newSettingsHandler("fixed-token", "127.0.0.1:43210", operations)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func settingsRequest(method, path string, form url.Values) *http.Request {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else if method == http.MethodPost {
		body = strings.NewReader("")
	}
	request := httptest.NewRequest(method, "http://127.0.0.1:43210"+path, body)
	request.Host = "127.0.0.1:43210"
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	return request
}

func TestSettingsPageShowsFourFunctionsWithoutSensitiveFields(t *testing.T) {
	operations := &fakeSettingsOperations{
		guards: desktopagent.GuardSettings{ShortInputGuard: true, LongCorrectionGuard: true},
		status: desktopagent.Status{
			HealthAvailable: true, EventLogAvailable: true,
			Health: localstore.SyncHealth{
				LastSuccessAt: 1000, LastEventAt: 1000, LastEventCode: "sync_complete",
				LastFailureClass: localstore.SyncFailureNone, PendingUploads: 2,
			},
		},
		vocabulary: desktopagent.VocabularySummary{TextIncluded: true, Entries: []desktopagent.VocabularyEntry{
			{Text: "办公室", Pinyin: "ban gong shi", UseCount: 5, Pinned: true},
		}},
	}
	recorder := httptest.NewRecorder()
	testSettingsHandler(t, operations).ServeHTTP(recorder,
		settingsRequest(http.MethodGet, "/fixed-token/", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, wanted := range []string{"短输入保护", "长纠错保护", "拼写纠错实验", "立即同步", "个人词库", "办公室", "/fixed-token/phrases/pin"} {
		if !strings.Contains(body, wanted) {
			t.Fatalf("settings page is missing %q", wanted)
		}
	}
	for _, forbidden := range []string{"account_id", "device_id", "recovery_key", "endpoint-config", "prepare-account", "pairing-"} {
		if strings.Contains(strings.ToLower(body), forbidden) {
			t.Fatalf("settings page exposed forbidden field %q", forbidden)
		}
	}
	for _, header := range []string{"Content-Security-Policy", "Cache-Control", "X-Frame-Options"} {
		if recorder.Header().Get(header) == "" {
			t.Fatalf("settings page omitted %s", header)
		}
	}
}

func TestSettingsRoutesApplyGuardsSyncAndVocabularyActions(t *testing.T) {
	operations := &fakeSettingsOperations{}
	handler := testSettingsHandler(t, operations)
	tests := []struct {
		path  string
		form  url.Values
		want  string
		check func()
	}{
		{
			path: "/fixed-token/guards",
			form: url.Values{"short_input_guard": {"on"}, "typo_correction": {"on"}},
			want: "notice=guards-saved",
			check: func() {
				if !operations.applied.ShortInputGuard || operations.applied.LongCorrectionGuard || !operations.applied.TypoCorrection {
					t.Fatalf("applied guards=%#v", operations.applied)
				}
			},
		},
		{path: "/fixed-token/sync", form: url.Values{}, want: "notice=sync-complete", check: func() {
			if operations.syncCalls != 1 {
				t.Fatalf("sync calls=%d", operations.syncCalls)
			}
		}},
		{path: "/fixed-token/phrases/add", form: url.Values{"text": {"办公室"}, "pinyin": {"ban gong shi"}, "pinned": {"on"}}, want: "notice=phrase-added", check: func() {
			if operations.addCalls != 1 || operations.lastText != "办公室" || operations.lastPinyin != "ban gong shi" || !operations.lastPinned {
				t.Fatalf("add operation=%#v", operations)
			}
		}},
		{path: "/fixed-token/phrases/pin", form: url.Values{"text": {"办公室"}, "pinyin": {"ban gong shi"}, "pinned": {"false"}}, want: "notice=phrase-updated", check: func() {
			if operations.pinCalls != 1 || operations.lastPinned {
				t.Fatalf("pin operation=%#v", operations)
			}
		}},
		{path: "/fixed-token/phrases/remove", form: url.Values{"text": {"办公室"}, "pinyin": {"ban gong shi"}}, want: "notice=phrase-removed", check: func() {
			if operations.removeCalls != 1 {
				t.Fatalf("remove calls=%d", operations.removeCalls)
			}
		}},
	}
	for _, test := range tests {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, settingsRequest(http.MethodPost, test.path, test.form))
		if recorder.Code != http.StatusSeeOther || !strings.Contains(recorder.Header().Get("Location"), test.want) {
			t.Fatalf("path=%s status=%d location=%q", test.path, recorder.Code, recorder.Header().Get("Location"))
		}
		test.check()
	}
}

func TestSettingsHandlerRejectsWrongHostPathAndMethod(t *testing.T) {
	handler := testSettingsHandler(t, &fakeSettingsOperations{})
	wrongHost := settingsRequest(http.MethodGet, "/fixed-token/", nil)
	wrongHost.Host = "localhost:43210"
	for _, request := range []*http.Request{
		wrongHost,
		settingsRequest(http.MethodGet, "/wrong-token/", nil),
		settingsRequest(http.MethodGet, "/fixed-token/sync", nil),
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("unexpected request accepted: status=%d", recorder.Code)
		}
	}
}

func TestSettingsBusyErrorStaysRedacted(t *testing.T) {
	operations := &fakeSettingsOperations{operationErr: desktopagent.ErrAlreadyRunning}
	recorder := httptest.NewRecorder()
	testSettingsHandler(t, operations).ServeHTTP(recorder,
		settingsRequest(http.MethodPost, "/fixed-token/sync", url.Values{}))
	if recorder.Code != http.StatusSeeOther || !strings.Contains(recorder.Header().Get("Location"), "error=busy") {
		t.Fatalf("status=%d location=%q", recorder.Code, recorder.Header().Get("Location"))
	}
	operations.operationErr = errors.New("private endpoint and keychain detail")
	recorder = httptest.NewRecorder()
	testSettingsHandler(t, operations).ServeHTTP(recorder,
		settingsRequest(http.MethodPost, "/fixed-token/sync", url.Values{}))
	if strings.Contains(recorder.Header().Get("Location"), "private") || !strings.Contains(recorder.Header().Get("Location"), "error=sync-failed") {
		t.Fatalf("private error leaked into redirect: %q", recorder.Header().Get("Location"))
	}
}

func TestSettingsSessionTokenIsRandomAndURLSafe(t *testing.T) {
	first, err := newSettingsSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	second, err := newSettingsSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) != 32 || strings.ContainsAny(first+second, "/+=?#") {
		t.Fatalf("unsafe session tokens: %q %q", first, second)
	}
}

func TestServeSettingsUsesOnlyLoopbackAndStopsWithContext(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opened := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- serveSettings(ctx, listener, &fakeSettingsOperations{}, func(pageURL string) error {
			opened <- pageURL
			return nil
		}, time.Minute)
	}()
	var pageURL string
	select {
	case pageURL = <-opened:
	case <-time.After(3 * time.Second):
		t.Fatal("settings server did not publish its local page")
	}
	parsed, err := url.Parse(pageURL)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" {
		t.Fatalf("settings URL=%q err=%v", pageURL, err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	response, err := client.Get(pageURL)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("settings GET status=%d", response.StatusCode)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("settings server did not stop with its context")
	}
}
