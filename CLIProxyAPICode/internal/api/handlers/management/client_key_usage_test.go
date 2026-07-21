// 本测试文件验证管理仪表盘客户端 Key 用量统计。
// 覆盖内容：
// 1. 按下游 api-keys 聚合历史 token 和当天 token。
// 2. 同一个 Key 下不同终端的活跃数量统计。
// 3. 跨日期时当天统计自动清零但历史统计保留。
// 4. api-key-names 中配置的使用人名字会附加到用量结果。
// 5. user-usages/cpa-user-usage-events.jsonl 汇总文件持久化后可重新加载。
// 6. 每次请求变化的 request/session/thread/conversation ID 不会把同一设备拆成多个终端。
// 7. 每位使用人拥有独立目录，并按日期生成一个只含 Input/Output 的 .log 文件。
// 8. 个人日志不写入模型、账户、Token、终端、耗时、状态或完整客户端 Key。
// 9. 旧版根目录单文件和分人每日 JSONL 都能转换成 .log，并移动到备份位置。
// 10. 管理接口只能打开配置中存在的 Key 对应目录，不能提交任意文件系统路径。
// 11. “是否保存所有对话”开关能够持久化，并且关闭时只停止个人日志、不停止汇总统计。
package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestGetClientKeyUsageAggregatesTokensAndActiveTerminals(t *testing.T) {
	resetClientKeyUsageCollectorForTest()
	now := time.Date(2026, time.July, 15, 10, 30, 0, 0, time.Local)
	globalClientKeyUsageCollector.now = func() time.Time { return now }

	handler := NewHandlerWithoutConfigFilePath(&config.Config{SDKConfig: config.SDKConfig{
		APIKeys:     []string{"client-key-b", "client-key-a"},
		APIKeyNames: map[string]string{"client-key-a": "张三"},
	}}, nil)
	globalClientKeyUsageCollector.HandleUsage(
		clientKeyUsageTestContext("terminal-a"),
		coreusage.Record{
			APIKey:      "client-key-a",
			Provider:    "codex",
			Model:       "gpt-5.1-codex",
			Alias:       "codex-max",
			AuthID:      "codex-oauth-auth-1",
			AuthIndex:   "codex-auth-01",
			AuthType:    "oauth",
			Source:      "coder@example.com",
			RequestedAt: now,
			Detail:      coreusage.Detail{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
		},
	)
	globalClientKeyUsageCollector.HandleUsage(
		clientKeyUsageTestContext("terminal-a"),
		coreusage.Record{
			APIKey:      "client-key-a",
			RequestedAt: now.Add(time.Minute),
			Detail:      coreusage.Detail{InputTokens: 3, OutputTokens: 2},
		},
	)
	globalClientKeyUsageCollector.HandleUsage(
		clientKeyUsageTestContext("terminal-b"),
		coreusage.Record{
			APIKey:      "client-key-a",
			Provider:    "codex",
			Model:       "gpt-5.1-codex",
			Alias:       "codex-mini",
			AuthID:      "codex-oauth-auth-2",
			AuthIndex:   "codex-auth-02",
			AuthType:    "oauth",
			Source:      "second@example.com",
			RequestedAt: now.Add(2 * time.Minute),
			Detail:      coreusage.Detail{InputTokens: 7, OutputTokens: 1, TotalTokens: 8},
		},
	)

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/client-key-usage", nil)
	handler.GetClientKeyUsage(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload clientKeyUsageResponse
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("decode payload: %v", errUnmarshal)
	}
	if payload.Date != "2026-07-15" {
		t.Fatalf("date = %q, want 2026-07-15", payload.Date)
	}
	if len(payload.Keys) != 2 {
		t.Fatalf("keys len = %d, want 2", len(payload.Keys))
	}

	entryA := clientUsageReportByKey(t, payload, "client-key-a")
	if entryA.HistoryTokens.TotalTokens != 28 || entryA.TodayTokens.TotalTokens != 28 {
		t.Fatalf("client-key-a totals = history %d today %d, want 28/28", entryA.HistoryTokens.TotalTokens, entryA.TodayTokens.TotalTokens)
	}
	if entryA.OwnerName != "张三" {
		t.Fatalf("client-key-a owner name = %q, want 张三", entryA.OwnerName)
	}
	if entryA.HistoryRequests != 3 || entryA.TodayRequests != 3 {
		t.Fatalf("client-key-a requests = history %d today %d, want 3/3", entryA.HistoryRequests, entryA.TodayRequests)
	}
	if entryA.ActiveTerminals != 2 {
		t.Fatalf("client-key-a active terminals = %d, want 2", entryA.ActiveTerminals)
	}
	if len(entryA.ActiveSessions) != 2 {
		t.Fatalf("client-key-a active sessions = %d, want 2", len(entryA.ActiveSessions))
	}
	latestSession := entryA.ActiveSessions[0]
	if latestSession.RequestedModel != "codex-mini" || latestSession.Model != "gpt-5.1-codex" {
		t.Fatalf("latest session model = %q/%q, want codex-mini/gpt-5.1-codex", latestSession.RequestedModel, latestSession.Model)
	}
	if latestSession.Account != "second@example.com" || latestSession.AuthIndex != "codex-auth-02" || latestSession.Provider != "codex" {
		t.Fatalf("latest session account/provider = %+v, want second@example.com/codex-auth-02/codex", latestSession)
	}

	entryB := clientUsageReportByKey(t, payload, "client-key-b")
	if entryB.HistoryTokens.TotalTokens != 0 || entryB.TodayTokens.TotalTokens != 0 || entryB.ActiveTerminals != 0 {
		t.Fatalf("client-key-b should be empty, got %+v", entryB)
	}
}

func TestClientKeyUsageTodayRollsOverButHistoryRemains(t *testing.T) {
	resetClientKeyUsageCollectorForTest()
	today := time.Date(2026, time.July, 15, 8, 0, 0, 0, time.Local)
	yesterday := today.Add(-24 * time.Hour)
	globalClientKeyUsageCollector.now = func() time.Time { return today }

	handler := NewHandlerWithoutConfigFilePath(&config.Config{SDKConfig: config.SDKConfig{APIKeys: []string{"client-key"}}}, nil)
	globalClientKeyUsageCollector.HandleUsage(
		clientKeyUsageTestContext("terminal-a"),
		coreusage.Record{
			APIKey:      "client-key",
			RequestedAt: yesterday,
			Detail:      coreusage.Detail{InputTokens: 10, OutputTokens: 10, TotalTokens: 20},
		},
	)
	globalClientKeyUsageCollector.HandleUsage(
		clientKeyUsageTestContext("terminal-a"),
		coreusage.Record{
			APIKey:      "client-key",
			RequestedAt: today,
			Detail:      coreusage.Detail{InputTokens: 1, OutputTokens: 2, TotalTokens: 3},
		},
	)

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/client-key-usage", nil)
	handler.GetClientKeyUsage(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload clientKeyUsageResponse
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("decode payload: %v", errUnmarshal)
	}
	entry := clientUsageReportByKey(t, payload, "client-key")
	if entry.HistoryTokens.TotalTokens != 23 {
		t.Fatalf("history total tokens = %d, want 23", entry.HistoryTokens.TotalTokens)
	}
	if entry.TodayTokens.TotalTokens != 3 {
		t.Fatalf("today total tokens = %d, want 3", entry.TodayTokens.TotalTokens)
	}
	if entry.HistoryRequests != 2 || entry.TodayRequests != 1 {
		t.Fatalf("requests = history %d today %d, want 2/1", entry.HistoryRequests, entry.TodayRequests)
	}
}

func TestClientKeyUsagePersistsAndReloadsState(t *testing.T) {
	now := time.Date(2026, time.July, 15, 9, 0, 0, 0, time.Local)
	paths := clientKeyUsageTestStoragePaths(t.TempDir())

	collector := newClientKeyUsageCollector()
	collector.now = func() time.Time { return now }
	collector.SetStoragePaths(paths)
	collector.SetConfiguredKeys([]string{"persist-key"}, map[string]string{"persist-key": "张三"})
	collector.HandleUsage(
		clientKeyUsageTestContext("terminal-a"),
		coreusage.Record{
			APIKey:      "persist-key",
			RequestedAt: now,
			Detail:      coreusage.Detail{InputTokens: 4, OutputTokens: 6, TotalTokens: 10},
		},
	)

	reloaded := newClientKeyUsageCollector()
	reloaded.now = func() time.Time { return now }
	reloaded.SetStoragePaths(paths)
	payload := reloaded.Snapshot([]string{"persist-key"}, nil)
	entry := clientUsageReportByKey(t, payload, "persist-key")

	if entry.HistoryTokens.TotalTokens != 10 || entry.TodayTokens.TotalTokens != 10 {
		t.Fatalf("reloaded totals = history %d today %d, want 10/10", entry.HistoryTokens.TotalTokens, entry.TodayTokens.TotalTokens)
	}
	if entry.HistoryRequests != 1 || entry.TodayRequests != 1 {
		t.Fatalf("reloaded requests = history %d today %d, want 1/1", entry.HistoryRequests, entry.TodayRequests)
	}
	if entry.ActiveTerminals != 0 {
		t.Fatalf("active terminals after reload = %d, want 0 because terminals are runtime-only", entry.ActiveTerminals)
	}
	storedSummary, errRead := os.ReadFile(paths.Summary)
	if errRead != nil {
		t.Fatalf("read summary file: %v", errRead)
	}
	if !strings.Contains(string(storedSummary), `"owner_name":"张三"`) {
		t.Fatalf("summary file missing owner name: %s", storedSummary)
	}
}

func TestClientKeyUsagePersistsDailyInteractionLogs(t *testing.T) {
	now := time.Date(2026, time.July, 16, 9, 0, 0, 0, time.Local)
	paths := clientKeyUsageTestStoragePaths(t.TempDir())
	collector := newClientKeyUsageCollector()
	collector.SetStoragePaths(paths)
	collector.SetConfiguredKeys([]string{"detail-client-key"}, map[string]string{"detail-client-key": "张三"})

	for index, content := range []struct {
		input  string
		output string
	}{
		{input: "第一条提示词", output: "第一条回复正文"},
		{input: "第二条提示词", output: "第二条回复正文"},
		{input: "第三条提示词", output: "第三条回复正文"},
	} {
		requestedAt := now.Add(time.Duration(index) * time.Minute)
		if index == 2 {
			requestedAt = now.Add(24*time.Hour + 2*time.Minute)
		}
		record := coreusage.Record{
			APIKey:          "detail-client-key",
			Provider:        "codex",
			ExecutorType:    "CodexExecutor",
			Model:           "sensitive-model",
			Alias:           "sensitive-model-alias",
			AuthID:          "auth-secret-identifier",
			AuthIndex:       "codex-auth-01",
			AuthType:        "oauth",
			Source:          "owner@example.com",
			ReasoningEffort: "high",
			ServiceTier:     "default",
			RequestedAt:     requestedAt,
			Latency:         time.Duration(index+1) * time.Second,
			TTFT:            time.Duration(index+1) * 100 * time.Millisecond,
			Detail: coreusage.Detail{
				InputTokens:  int64(10 + index),
				OutputTokens: int64(5 + index),
				TotalTokens:  int64(15 + index*2),
			},
		}
		capture := coreusage.NewContentCapture([]byte(content.input))
		capture.Complete([]byte(content.output))
		ctx := coreusage.WithContentCapture(clientKeyUsageTestContext("terminal-a"), capture)
		collector.HandleUsage(ctx, record)
	}
	firstDayPath := filepath.Join(paths.Root, "张三", "2026-07-16.log")
	secondDayPath := filepath.Join(paths.Root, "张三", "2026-07-17.log")
	waitForClientKeyUsageLogEntries(t, firstDayPath, 2)
	waitForClientKeyUsageLogEntries(t, secondDayPath, 1)
	firstDayStored, errFirstDay := os.ReadFile(firstDayPath)
	if errFirstDay != nil {
		t.Fatalf("read first day detail file: %v", errFirstDay)
	}
	secondDayStored, errSecondDay := os.ReadFile(secondDayPath)
	if errSecondDay != nil {
		t.Fatalf("read second day detail file: %v", errSecondDay)
	}
	allStored := string(firstDayStored) + string(secondDayStored)
	for _, content := range []string{
		"第一条提示词",
		"第一条回复正文",
		"第二条提示词",
		"第二条回复正文",
		"第三条提示词",
		"第三条回复正文",
	} {
		if !strings.Contains(allStored, content) {
			t.Fatalf("interaction log missing %q: %s", content, allStored)
		}
	}
	for _, forbidden := range []string{
		"detail-client-key",
		"sensitive-model",
		"owner@example.com",
		"codex-auth-01",
		"terminal-a",
		"total_tokens",
		"requested_at",
		"status_code",
	} {
		if strings.Contains(allStored, forbidden) {
			t.Fatalf("interaction log must not contain metadata %q: %s", forbidden, allStored)
		}
	}
	if count := strings.Count(string(firstDayStored), clientKeyUsageLogSeparator); count != 2 {
		t.Fatalf("first day interaction count = %d, want 2", count)
	}
	if count := strings.Count(string(secondDayStored), clientKeyUsageLogSeparator); count != 1 {
		t.Fatalf("second day interaction count = %d, want 1", count)
	}
}

func TestClientKeyUsageConversationSavingSettingPersistsAndControlsLogs(t *testing.T) {
	now := time.Date(2026, time.July, 20, 16, 0, 0, 0, time.Local)
	paths := clientKeyUsageTestStoragePaths(t.TempDir())
	collector := newClientKeyUsageCollector()
	collector.now = func() time.Time { return now }
	collector.SetStoragePaths(paths)
	collector.SetConfiguredKeys([]string{"switch-key"}, map[string]string{"switch-key": "开关测试用户"})
	if !collector.SaveAllConversations() {
		t.Fatal("conversation saving must default to enabled")
	}
	if errDisable := collector.SetSaveAllConversations(false); errDisable != nil {
		t.Fatalf("disable conversation saving: %v", errDisable)
	}

	logPath := filepath.Join(paths.Root, "开关测试用户", "2026-07-20.log")
	disabledCapture := coreusage.NewContentCapture([]byte("关闭后的提示词"))
	disabledCapture.Complete([]byte("关闭后的回复"))
	collector.HandleUsage(
		coreusage.WithContentCapture(clientKeyUsageTestContext("switch-terminal"), disabledCapture),
		coreusage.Record{
			APIKey:      "switch-key",
			RequestedAt: now,
			Detail:      coreusage.Detail{TotalTokens: 21},
		},
	)
	if _, errStat := os.Stat(logPath); !os.IsNotExist(errStat) {
		t.Fatalf("disabled conversation saving unexpectedly created a log: %v", errStat)
	}
	report := clientUsageReportByKey(
		t,
		collector.Snapshot([]string{"switch-key"}, map[string]string{"switch-key": "开关测试用户"}),
		"switch-key",
	)
	if report.HistoryRequests != 1 || report.HistoryTokens.TotalTokens != 21 {
		t.Fatalf("summary must continue while conversation saving is disabled: %+v", report)
	}

	reloaded := newClientKeyUsageCollector()
	reloaded.SetStoragePaths(paths)
	if reloaded.SaveAllConversations() {
		t.Fatal("disabled conversation saving setting was not reloaded from disk")
	}
	if errEnable := reloaded.SetSaveAllConversations(true); errEnable != nil {
		t.Fatalf("enable conversation saving: %v", errEnable)
	}
	reloaded.SetConfiguredKeys([]string{"switch-key"}, map[string]string{"switch-key": "开关测试用户"})
	enabledCapture := coreusage.NewContentCapture([]byte("重新开启后的提示词"))
	enabledCapture.Complete([]byte("重新开启后的回复"))
	reloaded.HandleUsage(
		coreusage.WithContentCapture(clientKeyUsageTestContext("switch-terminal"), enabledCapture),
		coreusage.Record{
			APIKey:      "switch-key",
			RequestedAt: now.Add(time.Minute),
			Detail:      coreusage.Detail{TotalTokens: 34},
		},
	)
	waitForClientKeyUsageLogEntries(t, logPath, 1)
	stored, errRead := os.ReadFile(logPath)
	if errRead != nil {
		t.Fatalf("read enabled interaction log: %v", errRead)
	}
	if strings.Contains(string(stored), "关闭后的提示词") ||
		!strings.Contains(string(stored), "重新开启后的提示词") {
		t.Fatalf("unexpected enabled interaction log content: %s", stored)
	}
}

func TestOpenClientKeyUsageFolderValidatesConfiguredKey(t *testing.T) {
	resetClientKeyUsageCollectorForTest()
	handler := NewHandlerWithoutConfigFilePath(&config.Config{SDKConfig: config.SDKConfig{
		APIKeys:     []string{"configured-key"},
		APIKeyNames: map[string]string{"configured-key": "李四"},
	}}, nil)
	paths := clientKeyUsageTestStoragePaths(t.TempDir())
	globalClientKeyUsageCollector.SetStoragePaths(paths)
	globalClientKeyUsageCollector.SetConfiguredKeys([]string{"configured-key"}, map[string]string{"configured-key": "李四"})
	openedPath := ""
	openCalls := 0
	handler.openFolder = func(path string) error {
		openedPath = path
		openCalls++
		return nil
	}

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	requestBody := `{"key_id":"` + clientKeyUsageKeyID("configured-key") + `"}`
	ginCtx.Request = httptest.NewRequest(
		http.MethodPost,
		"/v0/management/client-key-usage/open-folder",
		strings.NewReader(requestBody),
	)
	ginCtx.Request.Header.Set("Content-Type", "application/json")
	handler.OpenClientKeyUsageFolder(ginCtx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	wantPath := filepath.Join(paths.Root, "李四")
	if openedPath != wantPath || openCalls != 1 {
		t.Fatalf("opened path/calls = %q/%d, want %q/1", openedPath, openCalls, wantPath)
	}
	if info, errStat := os.Stat(wantPath); errStat != nil || !info.IsDir() {
		t.Fatalf("usage folder was not created: info=%v err=%v", info, errStat)
	}

	notFoundRec := httptest.NewRecorder()
	notFoundCtx, _ := gin.CreateTestContext(notFoundRec)
	notFoundBody := `{"key_id":"` + clientKeyUsageKeyID("unknown-key") + `"}`
	notFoundCtx.Request = httptest.NewRequest(
		http.MethodPost,
		"/v0/management/client-key-usage/open-folder",
		strings.NewReader(notFoundBody),
	)
	notFoundCtx.Request.Header.Set("Content-Type", "application/json")
	handler.OpenClientKeyUsageFolder(notFoundCtx)
	if notFoundRec.Code != http.StatusNotFound {
		t.Fatalf("unknown key status = %d, want %d", notFoundRec.Code, http.StatusNotFound)
	}
	if openCalls != 1 {
		t.Fatalf("unknown key unexpectedly opened a folder, calls = %d", openCalls)
	}
}

func TestClientKeyUsageMigratesLegacyDetailFile(t *testing.T) {
	base := t.TempDir()
	paths := clientKeyUsageTestStoragePaths(base)
	paths.LegacyDetail = filepath.Join(base, clientKeyUsageLegacyDetailName)
	keyID := clientKeyUsageKeyID("legacy-key")
	for index, requestedAt := range []time.Time{
		time.Date(2026, time.July, 10, 8, 0, 0, 0, time.Local),
		time.Date(2026, time.July, 11, 9, 0, 0, 0, time.Local),
	} {
		event := clientKeyUsageLegacyEvent{
			KeyID:       keyID,
			RequestedAt: requestedAt,
			Prompt:      fmt.Sprintf("旧提示词-%d", index+1),
			Response:    fmt.Sprintf("旧回复-%d", index+1),
		}
		if errAppend := appendClientKeyUsageLegacyJSONLine(paths.LegacyDetail, event); errAppend != nil {
			t.Fatalf("append legacy event: %v", errAppend)
		}
	}

	collector := newClientKeyUsageCollector()
	collector.SetStoragePaths(paths)
	collector.SetConfiguredKeys([]string{"legacy-key"}, map[string]string{"legacy-key": "王永顺"})
	collector.MigrateLegacyDetails()

	if _, errStat := os.Stat(paths.LegacyDetail); !os.IsNotExist(errStat) {
		t.Fatalf("legacy detail file should be renamed, stat error = %v", errStat)
	}
	archives, errGlob := filepath.Glob(filepath.Join(base, "cpa-user-usage-events.legacy-*.jsonl"))
	if errGlob != nil || len(archives) != 1 {
		t.Fatalf("legacy archives = %v, err = %v, want one", archives, errGlob)
	}
	for _, date := range []string{"2026-07-10", "2026-07-11"} {
		path := filepath.Join(paths.Root, "王永顺", date+".log")
		stored, errRead := os.ReadFile(path)
		if errRead != nil {
			t.Fatalf("migrated daily file %s missing: %v", path, errRead)
		}
		if strings.Contains(string(stored), "legacy-key") || strings.Contains(string(stored), "requested_at") {
			t.Fatalf("migrated log contains legacy metadata: %s", stored)
		}
	}
}

func TestClientKeyUsageMigratesDailyJSONLToLogAndArchivesSource(t *testing.T) {
	paths := clientKeyUsageTestStoragePaths(t.TempDir())
	sourcePath := filepath.Join(paths.Root, "宋晨勇", "2026-07-12.jsonl")
	for index := 1; index <= 2; index++ {
		event := clientKeyUsageLegacyEvent{
			KeyID:       clientKeyUsageKeyID("daily-legacy-key"),
			OwnerName:   "宋晨勇",
			RequestedAt: time.Date(2026, time.July, 12, 10, index, 0, 0, time.Local),
			Prompt:      fmt.Sprintf("迁移提示词-%d", index),
			Response:    fmt.Sprintf("迁移回复-%d", index),
		}
		if errAppend := appendClientKeyUsageLegacyJSONLine(sourcePath, event); errAppend != nil {
			t.Fatalf("append daily legacy event: %v", errAppend)
		}
	}

	collector := newClientKeyUsageCollector()
	collector.SetStoragePaths(paths)
	collector.MigrateLegacyDetails()
	collector.MigrateLegacyDetails()

	targetPath := filepath.Join(paths.Root, "宋晨勇", "2026-07-12.log")
	stored, errRead := os.ReadFile(targetPath)
	if errRead != nil {
		t.Fatalf("read migrated log: %v", errRead)
	}
	if count := strings.Count(string(stored), clientKeyUsageLogSeparator); count != 2 {
		t.Fatalf("migrated interaction count = %d, want 2; content=%s", count, stored)
	}
	archivePath := filepath.Join(paths.Root, clientKeyUsageLegacyJSONLDirName, "宋晨勇", "2026-07-12.jsonl")
	if _, errStat := os.Stat(archivePath); errStat != nil {
		t.Fatalf("daily JSONL archive missing: %v", errStat)
	}
	if _, errStat := os.Stat(sourcePath); !os.IsNotExist(errStat) {
		t.Fatalf("daily JSONL source should be moved, stat error = %v", errStat)
	}
}

func TestClientKeyUsageTerminalIDIgnoresPerRequestAndConversationIDs(t *testing.T) {
	first := clientKeyUsageFingerprintTestContext(
		"192.168.0.44:51001",
		"codex-cli/1.2.3",
		map[string]string{
			"Session_id":          "session-a",
			"X-Client-Request-Id": "request-a",
			"Thread-Id":           "thread-a",
			"Conversation-Id":     "conversation-a",
		},
	)
	second := clientKeyUsageFingerprintTestContext(
		"192.168.0.44:51099",
		"codex-cli/1.2.3",
		map[string]string{
			"Session_id":          "session-b",
			"X-Client-Request-Id": "request-b",
			"Thread-Id":           "thread-b",
			"Conversation-Id":     "conversation-b",
		},
	)
	otherDevice := clientKeyUsageFingerprintTestContext(
		"192.168.0.45:51001",
		"codex-cli/1.2.3",
		nil,
	)

	firstID := clientKeyUsageTerminalID(first)
	secondID := clientKeyUsageTerminalID(second)
	otherDeviceID := clientKeyUsageTerminalID(otherDevice)
	if firstID == "" || secondID == "" || otherDeviceID == "" {
		t.Fatalf("terminal IDs must not be empty: first=%q second=%q other=%q", firstID, secondID, otherDeviceID)
	}
	if firstID != secondID {
		t.Fatalf("same IP and User-Agent produced different terminal IDs: %q != %q", firstID, secondID)
	}
	if firstID == otherDeviceID {
		t.Fatalf("different client IPs produced the same terminal ID: %q", firstID)
	}
}

func TestClientKeyUsageTerminalIDPrefersStableDeviceHeader(t *testing.T) {
	first := clientKeyUsageFingerprintTestContext(
		"192.168.0.44:51001",
		"codex-cli/1.2.3",
		map[string]string{"X-Client-Instance-Id": "device-a"},
	)
	second := clientKeyUsageFingerprintTestContext(
		"192.168.0.44:51002",
		"codex-cli/1.2.3",
		map[string]string{"X-Client-Instance-Id": "device-b"},
	)

	firstID := clientKeyUsageTerminalID(first)
	secondID := clientKeyUsageTerminalID(second)
	if firstID == "" || secondID == "" {
		t.Fatalf("terminal IDs must not be empty: first=%q second=%q", firstID, secondID)
	}
	if firstID == secondID {
		t.Fatalf("different stable device IDs produced the same terminal ID: %q", firstID)
	}
}

func clientKeyUsageTestContext(sessionID string) context.Context {
	return clientKeyUsageFingerprintTestContext(
		"127.0.0.1:12345",
		"codex-test",
		map[string]string{"X-Client-Instance-Id": sessionID},
	)
}

func clientKeyUsageFingerprintTestContext(remoteAddr string, userAgent string, headers map[string]string) context.Context {
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	req.Header.Set("User-Agent", userAgent)
	req.RemoteAddr = remoteAddr
	ginCtx.Request = req
	return context.WithValue(context.Background(), "gin", ginCtx)
}

func clientKeyUsageTestStoragePaths(base string) clientKeyUsageStoragePaths {
	root := filepath.Join(base, clientKeyUsageRootDirName)
	return clientKeyUsageStoragePaths{
		Root:         root,
		Summary:      filepath.Join(root, clientKeyUsageSummaryFileName),
		Settings:     filepath.Join(root, clientKeyUsageSettingsFileName),
		LegacyState:  filepath.Join(base, clientKeyUsageLegacyStateName),
		LegacyDetail: filepath.Join(base, "legacy-client-key-usage-events.jsonl"),
	}
}

func waitForClientKeyUsageLogEntries(t *testing.T, path string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stored, errRead := os.ReadFile(path)
		if errRead == nil && strings.Count(string(stored), clientKeyUsageLogSeparator) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	stored, errRead := os.ReadFile(path)
	t.Fatalf(
		"interaction log count = %d, read error = %v, want %d",
		strings.Count(string(stored), clientKeyUsageLogSeparator),
		errRead,
		want,
	)
}

func appendClientKeyUsageLegacyJSONLine(path string, event clientKeyUsageLegacyEvent) error {
	data, errMarshal := json.Marshal(event)
	if errMarshal != nil {
		return errMarshal
	}
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o755); errMkdir != nil {
		return errMkdir
	}
	file, errOpen := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if errOpen != nil {
		return errOpen
	}
	if _, errWrite := file.Write(append(data, '\n')); errWrite != nil {
		_ = file.Close()
		return errWrite
	}
	return file.Close()
}

func clientUsageReportByKey(t *testing.T, payload clientKeyUsageResponse, key string) clientKeyUsageKeyReport {
	t.Helper()
	for _, entry := range payload.Keys {
		if entry.Key == key {
			return entry
		}
	}
	t.Fatalf("missing key %q in payload %+v", key, payload.Keys)
	return clientKeyUsageKeyReport{}
}
