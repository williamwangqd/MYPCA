// 本文件实现管理仪表盘的“下游客户端 API Key 用量统计”能力。
// 具体内容：
// 1. 作为 usage 插件接收每次请求完成后的 token 统计记录。
// 2. 按客户端传入的 api-keys 分别累计历史 token、当天 token 和请求次数。
// 3. 通过稳定设备请求头或“客户端 IP + User-Agent”指纹估算每个 Key 最近仍活跃的终端数量。
// 4. 明确忽略每次请求或每段对话都会变化的 request/session/thread/conversation ID，避免同一个人被重复计算成多个终端。
// 5. 记录活跃终端最近使用的模型、上游账户/凭据标识和 auth index，帮助定位当前请求分布。
// 6. 在返回结果中附加 api-key-names 里的使用人名字，方便查看每个 Key 属于谁。
// 7. 在 user-usages 根目录的 cpa-user-usage-events.jsonl 中保存每个 Key 的汇总状态。
// 8. 每位使用人建立独立子目录，并按 YYYY-MM-DD.log 每天保存一个交互正文日志文件。
// 9. 个人日志只保存 Input 和 Output 正文，不重复保存模型、账户、Token、终端、耗时等汇总元数据。
// 10. 启动时兼容读取旧汇总文件，并把旧的明细 JSONL 转换成纯正文日志后移入 legacy-jsonl 备份目录。
// 11. 暴露汇总与打开使用人统计文件夹接口，管理页面不再加载和展示逐次明细弹窗。
// 12. 提供“是否保存所有对话”持久化开关；关闭后继续统计用量，但停止写入个人对话日志。
package management

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

const (
	clientKeyUsageRootDirName        = "user-usages"
	clientKeyUsageSummaryFileName    = "cpa-user-usage-events.jsonl"
	clientKeyUsageSettingsFileName   = "cpa-user-usage-settings.json"
	clientKeyUsageLegacyStateName    = "cpa-key-usage-state.json"
	clientKeyUsageLegacyDetailName   = "cpa-user-usage-events.jsonl"
	clientKeyUsageDailyFileLayout    = "2006-01-02.log"
	clientKeyUsageLegacyJSONLDirName = "legacy-jsonl"
	clientKeyUsageLogSeparator       = "============================================================"
	clientKeyUsageStateVersion       = 2
	clientKeyUsageActiveTerminalTTL  = 10 * time.Minute
	clientKeyUsagePluginName         = "management:client-key-usage"
	clientKeyUsageTerminalHashPrefix = "sha256:"
	clientKeyUsageUnassignedOwner    = "未分配使用人"
)

var globalClientKeyUsageCollector = newClientKeyUsageCollector()

func init() {
	coreusage.RegisterNamedPlugin(clientKeyUsagePluginName, globalClientKeyUsageCollector)
}

type clientKeyUsageCollector struct {
	mu                sync.Mutex
	detailMu          sync.Mutex
	storageRoot       string
	summaryPath       string
	settingsPath      string
	legacyStatePath   string
	legacyDetailPath  string
	owners            map[string]string
	maskedKeys        map[string]string
	loaded            bool
	saveConversations bool
	state             clientKeyUsageState
	now               func() time.Time
}

type clientKeyUsageState struct {
	Version   int                                  `json:"version"`
	Keys      map[string]*clientKeyUsageStateEntry `json:"keys"`
	UpdatedAt time.Time                            `json:"updated_at"`
}

// clientKeyUsageSettings 保存用户使用统计模块的独立运行设置。
// 该设置不写入主 config.yaml，避免一次简单的日志开关触发整个代理配置热重载。
type clientKeyUsageSettings struct {
	SaveAllConversations bool      `json:"save_all_conversations"`
	UpdatedAt            time.Time `json:"updated_at"`
}

type clientKeyUsageStateEntry struct {
	HistoryTokens   clientKeyUsageTokenTotals `json:"history_tokens"`
	TodayDate       string                    `json:"today_date"`
	TodayTokens     clientKeyUsageTokenTotals `json:"today_tokens"`
	HistoryRequests int64                     `json:"history_requests"`
	TodayRequests   int64                     `json:"today_requests"`
	LastUsedAt      time.Time                 `json:"last_used_at,omitempty"`

	// terminals 只保存当前进程内最近活跃的终端，不写入磁盘，避免重启后把旧终端误判为在线。
	terminals map[string]clientKeyUsageTerminalState
}

// clientKeyUsageSummaryRecord 是汇总 JSONL 中的一行。
// 每个 Key 独占一行，文件更新时整体原子替换，避免服务异常退出留下半份汇总状态。
type clientKeyUsageSummaryRecord struct {
	Version         int                       `json:"version"`
	KeyID           string                    `json:"key_id"`
	MaskedKey       string                    `json:"masked_key"`
	OwnerName       string                    `json:"owner_name,omitempty"`
	HistoryTokens   clientKeyUsageTokenTotals `json:"history_tokens"`
	TodayDate       string                    `json:"today_date"`
	TodayTokens     clientKeyUsageTokenTotals `json:"today_tokens"`
	HistoryRequests int64                     `json:"history_requests"`
	TodayRequests   int64                     `json:"today_requests"`
	LastUsedAt      time.Time                 `json:"last_used_at,omitempty"`
	UpdatedAt       time.Time                 `json:"updated_at"`
}

type clientKeyUsageTokenTotals struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	ReasoningTokens     int64 `json:"reasoning_tokens"`
	CachedTokens        int64 `json:"cached_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
}

type clientKeyUsageResponse struct {
	Date                string                    `json:"date"`
	ActiveWindowSeconds int64                     `json:"active_window_seconds"`
	Keys                []clientKeyUsageKeyReport `json:"keys"`
}

type clientKeyUsageKeyReport struct {
	Key             string                    `json:"key"`
	KeyID           string                    `json:"key_id"`
	MaskedKey       string                    `json:"masked_key"`
	OwnerName       string                    `json:"owner_name,omitempty"`
	HistoryTokens   clientKeyUsageTokenTotals `json:"history_tokens"`
	TodayTokens     clientKeyUsageTokenTotals `json:"today_tokens"`
	HistoryRequests int64                     `json:"history_requests"`
	TodayRequests   int64                     `json:"today_requests"`
	ActiveTerminals int                       `json:"active_terminals"`
	ActiveSessions  []clientKeyUsageSession   `json:"active_sessions"`
	LastUsedAt      time.Time                 `json:"last_used_at,omitempty"`
}

type clientKeyUsageTerminalState struct {
	LastSeenAt     time.Time `json:"last_seen_at"`
	Model          string    `json:"model"`
	RequestedModel string    `json:"requested_model"`
	Provider       string    `json:"provider"`
	Account        string    `json:"account"`
	AuthID         string    `json:"auth_id"`
	AuthIndex      string    `json:"auth_index"`
	AuthType       string    `json:"auth_type"`
}

type clientKeyUsageSession struct {
	Terminal       string    `json:"terminal"`
	LastSeenAt     time.Time `json:"last_seen_at"`
	Model          string    `json:"model"`
	RequestedModel string    `json:"requested_model"`
	Provider       string    `json:"provider"`
	Account        string    `json:"account"`
	AuthID         string    `json:"auth_id"`
	AuthIndex      string    `json:"auth_index"`
	AuthType       string    `json:"auth_type"`
}

// clientKeyUsageInteraction 是写入个人日志前使用的最小交互结构。
// 它只携带定位日志文件所需的信息以及 Input/Output 正文，避免新代码误把汇总元数据写入个人文件。
type clientKeyUsageInteraction struct {
	KeyID       string
	OwnerName   string
	RequestedAt time.Time
	Prompt      string
	Response    string
}

// clientKeyUsageLegacyEvent 仅用于解析旧版 JSONL。
// JSON 中未声明的旧字段会被自动忽略，迁移时只提取目录、日期和交互正文所需字段。
type clientKeyUsageLegacyEvent struct {
	KeyID       string    `json:"key_id"`
	OwnerName   string    `json:"owner_name,omitempty"`
	RequestedAt time.Time `json:"requested_at"`
	Prompt      string    `json:"prompt,omitempty"`
	Response    string    `json:"response,omitempty"`
}

func newClientKeyUsageCollector() *clientKeyUsageCollector {
	return &clientKeyUsageCollector{
		state:             newClientKeyUsageState(),
		owners:            make(map[string]string),
		maskedKeys:        make(map[string]string),
		saveConversations: true,
		now:               time.Now,
	}
}

func newClientKeyUsageState() clientKeyUsageState {
	return clientKeyUsageState{
		Version: clientKeyUsageStateVersion,
		Keys:    make(map[string]*clientKeyUsageStateEntry),
	}
}

type clientKeyUsageStoragePaths struct {
	Root         string
	Summary      string
	Settings     string
	LegacyState  string
	LegacyDetail string
}

func configureClientKeyUsageState(configFilePath string, configuredKeys []string, configuredKeyNames map[string]string) {
	paths := resolveClientKeyUsageStoragePaths(configFilePath)
	globalClientKeyUsageCollector.SetStoragePaths(paths)
	globalClientKeyUsageCollector.SetConfiguredKeys(configuredKeys, configuredKeyNames)
	globalClientKeyUsageCollector.PersistSummary()
	globalClientKeyUsageCollector.MigrateLegacyDetails()
}

func resolveClientKeyUsageStoragePaths(configFilePath string) clientKeyUsageStoragePaths {
	base := strings.TrimSpace(util.WritablePath())
	if base == "" {
		configFilePath = strings.TrimSpace(configFilePath)
		if configFilePath == "" {
			return clientKeyUsageStoragePaths{}
		}
		base = filepath.Dir(configFilePath)
		if info, errStat := os.Stat(configFilePath); errStat == nil && info.IsDir() {
			base = configFilePath
		}
	}

	root := filepath.Join(base, clientKeyUsageRootDirName)
	return clientKeyUsageStoragePaths{
		Root:         root,
		Summary:      filepath.Join(root, clientKeyUsageSummaryFileName),
		Settings:     filepath.Join(root, clientKeyUsageSettingsFileName),
		LegacyState:  filepath.Join(base, clientKeyUsageLegacyStateName),
		LegacyDetail: filepath.Join(base, clientKeyUsageLegacyDetailName),
	}
}

func (c *clientKeyUsageCollector) SetStoragePaths(paths clientKeyUsageStoragePaths) {
	if c == nil {
		return
	}
	paths.Root = strings.TrimSpace(paths.Root)
	paths.Summary = strings.TrimSpace(paths.Summary)
	paths.Settings = strings.TrimSpace(paths.Settings)
	paths.LegacyState = strings.TrimSpace(paths.LegacyState)
	paths.LegacyDetail = strings.TrimSpace(paths.LegacyDetail)
	if paths.Root != "" && paths.Summary == "" {
		paths.Summary = filepath.Join(paths.Root, clientKeyUsageSummaryFileName)
	}
	if paths.Root != "" && paths.Settings == "" {
		paths.Settings = filepath.Join(paths.Root, clientKeyUsageSettingsFileName)
	}
	saveConversations := loadClientKeyUsageSettings(paths.Settings)

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.storageRoot == paths.Root && c.summaryPath == paths.Summary && c.settingsPath == paths.Settings && c.loaded {
		c.legacyStatePath = paths.LegacyState
		c.legacyDetailPath = paths.LegacyDetail
		c.saveConversations = saveConversations
		return
	}
	c.storageRoot = paths.Root
	c.summaryPath = paths.Summary
	c.settingsPath = paths.Settings
	c.legacyStatePath = paths.LegacyState
	c.legacyDetailPath = paths.LegacyDetail
	c.loaded = false
	c.saveConversations = saveConversations
	c.state = newClientKeyUsageState()
}

// SaveAllConversations 返回当前是否保存所有个人对话正文。
func (c *clientKeyUsageCollector) SaveAllConversations() bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	enabled := c.saveConversations
	c.mu.Unlock()
	return enabled
}

// SetSaveAllConversations 更新内存开关并立即持久化到 user-usages 根目录。
// 写盘失败时恢复原值，确保管理页面显示的状态与实际运行状态一致。
func (c *clientKeyUsageCollector) SetSaveAllConversations(enabled bool) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	previous := c.saveConversations
	path := c.settingsPath
	c.saveConversations = enabled
	c.mu.Unlock()
	if errSave := saveClientKeyUsageSettings(path, enabled, c.currentTime()); errSave != nil {
		c.mu.Lock()
		if c.saveConversations == enabled {
			c.saveConversations = previous
		}
		c.mu.Unlock()
		return errSave
	}
	return nil
}

func (c *clientKeyUsageCollector) SetConfiguredKeys(configuredKeys []string, configuredKeyNames map[string]string) {
	if c == nil {
		return
	}
	keys := normalizeClientUsageKeys(configuredKeys)
	names := normalizeAPIKeyNamesForKeys(keys, configuredKeyNames)
	owners := make(map[string]string, len(keys))
	maskedKeys := make(map[string]string, len(keys))
	for _, key := range keys {
		keyID := clientKeyUsageKeyID(key)
		owners[keyID] = strings.TrimSpace(names[key])
		maskedKeys[keyID] = maskClientUsageKey(key)
	}

	c.mu.Lock()
	c.owners = owners
	c.maskedKeys = maskedKeys
	c.mu.Unlock()
}

// PersistSummary 立即把当前内存汇总写入 user-usages 根目录。
// 服务启动时调用它可以把旧版 cpa-key-usage-state.json 平滑转换成新的汇总 JSONL。
func (c *clientKeyUsageCollector) PersistSummary() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.ensureLoadedLocked()
	path := c.summaryPath
	state := cloneClientKeyUsageStateForSave(c.state)
	owners := cloneClientKeyUsageStringMap(c.owners)
	maskedKeys := cloneClientKeyUsageStringMap(c.maskedKeys)
	c.mu.Unlock()
	if path != "" {
		saveClientKeyUsageState(path, state, owners, maskedKeys)
	}
}

func (c *clientKeyUsageCollector) HandleUsage(ctx context.Context, record coreusage.Record) {
	if c == nil {
		return
	}

	apiKey := strings.TrimSpace(record.APIKey)
	if apiKey == "" {
		return
	}

	now := record.RequestedAt
	if now.IsZero() {
		now = c.currentTime()
	}
	detail := clientKeyUsageTokenTotalsFromDetail(record.Detail)
	terminalID := clientKeyUsageTerminalID(ctx)
	keyID := clientKeyUsageKeyID(apiKey)
	today := clientKeyUsageDate(now)
	contentCapture := coreusage.ContentCaptureFromContext(ctx)

	c.mu.Lock()
	c.ensureLoadedLocked()
	ownerName := strings.TrimSpace(c.owners[keyID])
	if ownerName == "" {
		ownerName = clientKeyUsageUnassignedOwner
	}
	interaction := clientKeyUsageInteraction{
		KeyID:       keyID,
		OwnerName:   ownerName,
		RequestedAt: now,
	}
	entry := c.state.Keys[keyID]
	if entry == nil {
		entry = &clientKeyUsageStateEntry{}
		c.state.Keys[keyID] = entry
	}
	entry.ensureTodayDate(today)
	entry.HistoryTokens.add(detail)
	entry.TodayTokens.add(detail)
	entry.HistoryRequests++
	entry.TodayRequests++
	entry.LastUsedAt = now
	if terminalID != "" {
		if entry.terminals == nil {
			entry.terminals = make(map[string]clientKeyUsageTerminalState)
		}
		entry.terminals[terminalID] = clientKeyUsageTerminalStateFromRecord(now, record)
	}
	pruneClientKeyUsageTerminals(entry, now)
	c.state.UpdatedAt = now
	storageRoot := c.storageRoot
	summaryPath := c.summaryPath
	state := cloneClientKeyUsageStateForSave(c.state)
	owners := cloneClientKeyUsageStringMap(c.owners)
	maskedKeys := cloneClientKeyUsageStringMap(c.maskedKeys)
	saveConversations := c.saveConversations
	c.mu.Unlock()

	if summaryPath != "" {
		saveClientKeyUsageState(summaryPath, state, owners, maskedKeys)
	}
	if storageRoot == "" || !saveConversations {
		return
	}
	if contentCapture == nil {
		c.persistClientKeyUsageInteraction(storageRoot, interaction)
		return
	}
	go c.persistClientKeyUsageInteractionAfterCapture(storageRoot, interaction, contentCapture)
}

func (c *clientKeyUsageCollector) Snapshot(configuredKeys []string, configuredKeyNames map[string]string) clientKeyUsageResponse {
	if c == nil {
		return clientKeyUsageResponse{}
	}

	now := c.currentTime()
	today := clientKeyUsageDate(now)
	keys := normalizeClientUsageKeys(configuredKeys)
	keyNames := normalizeAPIKeyNamesForKeys(keys, configuredKeyNames)
	c.SetConfiguredKeys(keys, keyNames)

	c.mu.Lock()
	c.ensureLoadedLocked()
	reports := make([]clientKeyUsageKeyReport, 0, len(keys))
	for _, key := range keys {
		keyID := clientKeyUsageKeyID(key)
		entry := c.state.Keys[keyID]
		report := clientKeyUsageKeyReport{
			Key:       key,
			KeyID:     keyID,
			MaskedKey: maskClientUsageKey(key),
			OwnerName: keyNames[key],
		}
		if entry != nil {
			entry.ensureTodayDate(today)
			pruneClientKeyUsageTerminals(entry, now)
			activeSessions := clientKeyUsageActiveSessions(entry)
			report.HistoryTokens = entry.HistoryTokens
			report.TodayTokens = entry.TodayTokens
			report.HistoryRequests = entry.HistoryRequests
			report.TodayRequests = entry.TodayRequests
			report.ActiveTerminals = len(activeSessions)
			report.ActiveSessions = activeSessions
			report.LastUsedAt = entry.LastUsedAt
		}
		reports = append(reports, report)
	}
	c.mu.Unlock()

	return clientKeyUsageResponse{
		Date:                today,
		ActiveWindowSeconds: int64(clientKeyUsageActiveTerminalTTL.Seconds()),
		Keys:                reports,
	}
}

// UserFolderPath 返回指定使用人的本地统计目录。
// 文件夹名称仍使用统一的净化规则，确保管理接口无法通过使用人名称跳出 user-usages 根目录。
func (c *clientKeyUsageCollector) UserFolderPath(ownerName, keyID string) string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	storageRoot := c.storageRoot
	c.mu.Unlock()
	if strings.TrimSpace(storageRoot) == "" {
		return ""
	}
	return filepath.Join(storageRoot, clientKeyUsageOwnerFolderName(ownerName, keyID))
}
func (c *clientKeyUsageCollector) currentTime() time.Time {
	if c == nil || c.now == nil {
		return time.Now()
	}
	return c.now()
}

func (c *clientKeyUsageCollector) ensureLoadedLocked() {
	if c == nil || c.loaded {
		return
	}
	c.loaded = true
	if c.state.Keys == nil {
		c.state = newClientKeyUsageState()
	}
	if c.summaryPath == "" && c.legacyStatePath == "" {
		return
	}

	if state, loaded := loadClientKeyUsageSummary(c.summaryPath); loaded {
		c.state = state
		return
	}
	if state, loaded := loadLegacyClientKeyUsageState(c.legacyStatePath); loaded {
		c.state = state
	}
}

func cloneClientKeyUsageStateForSave(state clientKeyUsageState) clientKeyUsageState {
	out := clientKeyUsageState{
		Version:   state.Version,
		Keys:      make(map[string]*clientKeyUsageStateEntry, len(state.Keys)),
		UpdatedAt: state.UpdatedAt,
	}
	if out.Version <= 0 {
		out.Version = clientKeyUsageStateVersion
	}
	for key, entry := range state.Keys {
		if entry == nil {
			continue
		}
		copied := *entry
		copied.terminals = nil
		out.Keys[key] = &copied
	}
	return out
}

func clientKeyUsageTerminalStateFromRecord(now time.Time, record coreusage.Record) clientKeyUsageTerminalState {
	model := strings.TrimSpace(record.Model)
	requestedModel := strings.TrimSpace(record.Alias)
	if requestedModel == "" {
		requestedModel = model
	}
	if model == "" {
		model = requestedModel
	}

	authIndex := strings.TrimSpace(record.AuthIndex)
	authID := strings.TrimSpace(record.AuthID)
	account := clientKeyUsageAccountLabel(record)
	if account == "" {
		account = authIndex
	}
	if account == "" {
		account = maskClientUsageKey(authID)
	}

	return clientKeyUsageTerminalState{
		LastSeenAt:     now,
		Model:          model,
		RequestedModel: requestedModel,
		Provider:       strings.TrimSpace(record.Provider),
		Account:        account,
		AuthID:         maskClientUsageKey(authID),
		AuthIndex:      authIndex,
		AuthType:       strings.TrimSpace(record.AuthType),
	}
}

func clientKeyUsageAccountLabel(record coreusage.Record) string {
	source := strings.TrimSpace(record.Source)
	if source == "" {
		return ""
	}

	authType := strings.ToLower(strings.TrimSpace(record.AuthType))
	if authType == "apikey" || authType == "api_key" || authType == "api-key" {
		return maskClientUsageKey(source)
	}
	if strings.Contains(source, "@") {
		return source
	}
	if len(source) > 24 {
		return maskClientUsageKey(source)
	}
	return source
}

func cloneClientKeyUsageStringMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

// saveClientKeyUsageState 将每个 Key 的汇总状态写成独立 JSONL 行。
// 文件采用临时文件加原子替换，避免高并发请求期间管理页面读到半行 JSON。
func saveClientKeyUsageState(
	path string,
	state clientKeyUsageState,
	owners map[string]string,
	maskedKeys map[string]string,
) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	keySet := make(map[string]struct{}, len(state.Keys)+len(owners))
	for keyID := range state.Keys {
		keySet[keyID] = struct{}{}
	}
	for keyID := range owners {
		keySet[keyID] = struct{}{}
	}
	keyIDs := make([]string, 0, len(keySet))
	for keyID := range keySet {
		keyIDs = append(keyIDs, keyID)
	}
	sort.Strings(keyIDs)

	dir := filepath.Dir(path)
	if errMkdir := os.MkdirAll(dir, 0o755); errMkdir != nil {
		return
	}
	tempFile, errCreate := os.CreateTemp(dir, ".cpa-user-usage-summary-*.tmp")
	if errCreate != nil {
		return
	}
	tempPath := tempFile.Name()
	writer := bufio.NewWriterSize(tempFile, 64*1024)
	for _, keyID := range keyIDs {
		entry := state.Keys[keyID]
		if entry == nil {
			entry = &clientKeyUsageStateEntry{}
		}
		record := clientKeyUsageSummaryRecord{
			Version:         clientKeyUsageStateVersion,
			KeyID:           keyID,
			MaskedKey:       strings.TrimSpace(maskedKeys[keyID]),
			OwnerName:       strings.TrimSpace(owners[keyID]),
			HistoryTokens:   entry.HistoryTokens,
			TodayDate:       entry.TodayDate,
			TodayTokens:     entry.TodayTokens,
			HistoryRequests: entry.HistoryRequests,
			TodayRequests:   entry.TodayRequests,
			LastUsedAt:      entry.LastUsedAt,
			UpdatedAt:       state.UpdatedAt,
		}
		data, errMarshal := json.Marshal(record)
		if errMarshal != nil {
			_ = tempFile.Close()
			_ = os.Remove(tempPath)
			return
		}
		if _, errWrite := writer.Write(append(data, '\n')); errWrite != nil {
			_ = tempFile.Close()
			_ = os.Remove(tempPath)
			return
		}
	}
	if errFlush := writer.Flush(); errFlush != nil {
		_ = tempFile.Close()
		_ = os.Remove(tempPath)
		return
	}
	if errClose := tempFile.Close(); errClose != nil {
		_ = os.Remove(tempPath)
		return
	}
	_ = os.Remove(path)
	if errRename := os.Rename(tempPath, path); errRename != nil {
		_ = os.Remove(tempPath)
	}
}

func loadClientKeyUsageSummary(path string) (clientKeyUsageState, bool) {
	state := newClientKeyUsageState()
	path = strings.TrimSpace(path)
	if path == "" {
		return state, false
	}
	file, errOpen := os.Open(path)
	if errOpen != nil {
		return state, false
	}
	defer func() { _ = file.Close() }()

	loaded := false
	errRead := readClientKeyUsageJSONLines(file, func(line []byte) error {
		var record clientKeyUsageSummaryRecord
		if errUnmarshal := json.Unmarshal(line, &record); errUnmarshal != nil {
			return nil
		}
		record.KeyID = strings.TrimSpace(record.KeyID)
		if record.KeyID == "" {
			return nil
		}
		state.Keys[record.KeyID] = &clientKeyUsageStateEntry{
			HistoryTokens:   record.HistoryTokens,
			TodayDate:       record.TodayDate,
			TodayTokens:     record.TodayTokens,
			HistoryRequests: record.HistoryRequests,
			TodayRequests:   record.TodayRequests,
			LastUsedAt:      record.LastUsedAt,
		}
		if record.UpdatedAt.After(state.UpdatedAt) {
			state.UpdatedAt = record.UpdatedAt
		}
		loaded = true
		return nil
	})
	if errRead != nil || !loaded {
		return newClientKeyUsageState(), false
	}
	return state, true
}

// loadClientKeyUsageSettings 读取对话保存开关。
// 文件不存在或内容损坏时默认开启，以保持升级前“自动保存全部对话”的既有行为。
func loadClientKeyUsageSettings(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return true
	}
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		return true
	}
	var settings clientKeyUsageSettings
	if errUnmarshal := json.Unmarshal(data, &settings); errUnmarshal != nil {
		return true
	}
	return settings.SaveAllConversations
}

// saveClientKeyUsageSettings 通过临时文件和原子替换保存开关，避免进程异常时留下半份 JSON。
func saveClientKeyUsageSettings(path string, enabled bool, updatedAt time.Time) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("client key usage settings path is empty")
	}
	settings := clientKeyUsageSettings{
		SaveAllConversations: enabled,
		UpdatedAt:            updatedAt,
	}
	data, errMarshal := json.MarshalIndent(settings, "", "  ")
	if errMarshal != nil {
		return fmt.Errorf("marshal client key usage settings: %w", errMarshal)
	}
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o755); errMkdir != nil {
		return fmt.Errorf("create client key usage settings directory: %w", errMkdir)
	}
	temporaryPath := path + ".tmp"
	if errWrite := os.WriteFile(temporaryPath, append(data, '\n'), 0o644); errWrite != nil {
		return fmt.Errorf("write client key usage settings: %w", errWrite)
	}
	_ = os.Remove(path)
	if errRename := os.Rename(temporaryPath, path); errRename != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("replace client key usage settings: %w", errRename)
	}
	return nil
}

func loadLegacyClientKeyUsageState(path string) (clientKeyUsageState, bool) {
	state := newClientKeyUsageState()
	path = strings.TrimSpace(path)
	if path == "" {
		return state, false
	}
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		return state, false
	}
	if errUnmarshal := json.Unmarshal(data, &state); errUnmarshal != nil {
		return newClientKeyUsageState(), false
	}
	if state.Keys == nil {
		state.Keys = make(map[string]*clientKeyUsageStateEntry)
	}
	state.Version = clientKeyUsageStateVersion
	for _, entry := range state.Keys {
		if entry != nil {
			entry.terminals = nil
		}
	}
	return state, true
}

func (c *clientKeyUsageCollector) persistClientKeyUsageInteractionAfterCapture(
	storageRoot string,
	interaction clientKeyUsageInteraction,
	capture *coreusage.ContentCapture,
) {
	requestBody, responseBody := capture.Wait()
	interaction.Prompt = string(requestBody)
	interaction.Response = string(responseBody)
	c.persistClientKeyUsageInteraction(storageRoot, interaction)
}

func (c *clientKeyUsageCollector) persistClientKeyUsageInteraction(
	storageRoot string,
	interaction clientKeyUsageInteraction,
) {
	path := clientKeyUsageDailyDetailPath(
		storageRoot,
		interaction.OwnerName,
		interaction.KeyID,
		interaction.RequestedAt,
	)
	if path == "" {
		return
	}
	c.detailMu.Lock()
	errAppend := appendClientKeyUsageInteraction(path, interaction.Prompt, interaction.Response)
	c.detailMu.Unlock()
	if errAppend != nil {
		log.WithError(errAppend).Warn("failed to append client key interaction log")
	}
}

func clientKeyUsageDailyDetailPath(storageRoot, ownerName, keyID string, requestedAt time.Time) string {
	storageRoot = strings.TrimSpace(storageRoot)
	if storageRoot == "" {
		return ""
	}
	if requestedAt.IsZero() {
		requestedAt = time.Now()
	}
	fileName := requestedAt.In(time.Local).Format(clientKeyUsageDailyFileLayout)
	return filepath.Join(storageRoot, clientKeyUsageOwnerFolderName(ownerName, keyID), fileName)
}

func clientKeyUsageOwnerFolderName(ownerName, keyID string) string {
	ownerName = strings.TrimSpace(ownerName)
	if ownerName == "" {
		ownerName = clientKeyUsageUnassignedOwner
	}
	sanitized := strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\\|?*`, r) {
			return '_'
		}
		return r
	}, ownerName)
	sanitized = strings.Trim(sanitized, " .")
	if sanitized == "" {
		sanitized = clientKeyUsageUnassignedOwner
	}
	runes := []rune(sanitized)
	if len(runes) > 64 {
		sanitized = string(runes[:64])
	}
	if sanitized == clientKeyUsageUnassignedOwner {
		shortKeyID := strings.TrimSpace(keyID)
		if len(shortKeyID) > 8 {
			shortKeyID = shortKeyID[:8]
		}
		if shortKeyID != "" {
			sanitized += "_" + shortKeyID
		}
	}
	return sanitized
}

// appendClientKeyUsageInteraction 以人类可读格式追加一组 Input/Output。
// 个人日志中不写时间、模型、账户、Token、终端或状态，这些数据统一由根目录汇总文件负责。
func appendClientKeyUsageInteraction(path, input, output string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o755); errMkdir != nil {
		return errMkdir
	}
	file, errOpen := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if errOpen != nil {
		return errOpen
	}
	if errWrite := writeClientKeyUsageInteraction(file, input, output); errWrite != nil {
		_ = file.Close()
		return errWrite
	}
	return file.Close()
}

func writeClientKeyUsageInteraction(writer io.Writer, input, output string) error {
	if writer == nil {
		return nil
	}
	parts := []string{"Input:\n", input}
	if input != "" && !strings.HasSuffix(input, "\n") {
		parts = append(parts, "\n")
	}
	parts = append(parts, "\nOutput:\n", output)
	if output != "" && !strings.HasSuffix(output, "\n") {
		parts = append(parts, "\n")
	}
	parts = append(parts, "\n", clientKeyUsageLogSeparator, "\n\n")
	_, errWrite := io.WriteString(writer, strings.Join(parts, ""))
	return errWrite
}

// MigrateLegacyDetails 把旧版根目录单文件和分人每日 JSONL 转换成纯正文 .log。
// 每个旧 JSONL 转换成功后都会移入 user-usages/legacy-jsonl，既保留原始数据也避免下次启动重复转换。
func (c *clientKeyUsageCollector) MigrateLegacyDetails() {
	if c == nil {
		return
	}
	c.mu.Lock()
	storageRoot := c.storageRoot
	legacyPath := c.legacyDetailPath
	owners := cloneClientKeyUsageStringMap(c.owners)
	c.mu.Unlock()
	if storageRoot == "" {
		return
	}

	c.detailMu.Lock()
	defer c.detailMu.Unlock()
	if errMigrateRoot := migrateLegacyClientKeyUsageRootFile(
		storageRoot,
		legacyPath,
		owners,
		c.currentTime(),
	); errMigrateRoot != nil {
		log.WithError(errMigrateRoot).Warn("failed to migrate legacy client key usage detail file")
	}
	if errMigrateDaily := migrateClientKeyUsageDailyJSONLFiles(storageRoot); errMigrateDaily != nil {
		log.WithError(errMigrateDaily).Warn("failed to migrate daily client key usage JSONL files")
	}
}

func migrateLegacyClientKeyUsageRootFile(
	storageRoot string,
	legacyPath string,
	owners map[string]string,
	now time.Time,
) error {
	legacyPath = strings.TrimSpace(legacyPath)
	if legacyPath == "" {
		return nil
	}
	if _, errStat := os.Stat(legacyPath); errStat != nil {
		if os.IsNotExist(errStat) {
			return nil
		}
		return errStat
	}
	file, errOpen := os.Open(legacyPath)
	if errOpen != nil {
		return errOpen
	}
	errRead := readClientKeyUsageJSONLines(file, func(line []byte) error {
		var event clientKeyUsageLegacyEvent
		if errUnmarshal := json.Unmarshal(line, &event); errUnmarshal != nil {
			return nil
		}
		if strings.TrimSpace(event.KeyID) == "" {
			return nil
		}
		if strings.TrimSpace(event.OwnerName) == "" {
			event.OwnerName = strings.TrimSpace(owners[event.KeyID])
		}
		if strings.TrimSpace(event.OwnerName) == "" {
			event.OwnerName = clientKeyUsageUnassignedOwner
		}
		path := clientKeyUsageDailyDetailPath(storageRoot, event.OwnerName, event.KeyID, event.RequestedAt)
		return appendClientKeyUsageInteraction(path, event.Prompt, event.Response)
	})
	errClose := file.Close()
	if errRead != nil {
		return errRead
	}
	if errClose != nil {
		return errClose
	}
	archiveName := fmt.Sprintf(
		"cpa-user-usage-events.legacy-%s.jsonl",
		now.Format("20060102-150405"),
	)
	archivePath := filepath.Join(filepath.Dir(legacyPath), archiveName)
	if _, errStat := os.Stat(archivePath); errStat == nil {
		archivePath = filepath.Join(
			filepath.Dir(legacyPath),
			fmt.Sprintf("cpa-user-usage-events.legacy-%d.jsonl", now.UnixNano()),
		)
	}
	return os.Rename(legacyPath, archivePath)
}

func migrateClientKeyUsageDailyJSONLFiles(storageRoot string) error {
	files := make([]string, 0)
	errWalk := filepath.WalkDir(storageRoot, func(path string, entry fs.DirEntry, errWalk error) error {
		if errWalk != nil {
			if os.IsNotExist(errWalk) {
				return nil
			}
			return errWalk
		}
		if entry.IsDir() {
			if path != storageRoot && entry.Name() == clientKeyUsageLegacyJSONLDirName {
				return filepath.SkipDir
			}
			return nil
		}
		relative, errRelative := filepath.Rel(storageRoot, path)
		if errRelative != nil {
			return errRelative
		}
		if filepath.Dir(relative) == "." || !strings.HasSuffix(strings.ToLower(entry.Name()), ".jsonl") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if errWalk != nil {
		if os.IsNotExist(errWalk) {
			return nil
		}
		return errWalk
	}
	sort.Slice(files, func(left, right int) bool {
		return files[left] < files[right]
	})
	for _, path := range files {
		if errConvert := migrateClientKeyUsageDailyJSONLFile(storageRoot, path); errConvert != nil {
			return fmt.Errorf("migrate %s: %w", path, errConvert)
		}
	}
	return nil
}

func migrateClientKeyUsageDailyJSONLFile(storageRoot, sourcePath string) error {
	relative, errRelative := filepath.Rel(storageRoot, sourcePath)
	if errRelative != nil {
		return errRelative
	}
	targetPath := strings.TrimSuffix(sourcePath, filepath.Ext(sourcePath)) + ".log"
	if _, errStat := os.Stat(targetPath); os.IsNotExist(errStat) {
		if errConvert := convertClientKeyUsageJSONLToLog(sourcePath, targetPath); errConvert != nil {
			return errConvert
		}
	} else if errStat != nil {
		return errStat
	}

	archivePath := filepath.Join(storageRoot, clientKeyUsageLegacyJSONLDirName, relative)
	if errMkdir := os.MkdirAll(filepath.Dir(archivePath), 0o755); errMkdir != nil {
		return errMkdir
	}
	archivePath = nextAvailableClientKeyUsageArchivePath(archivePath)
	return os.Rename(sourcePath, archivePath)
}

func convertClientKeyUsageJSONLToLog(sourcePath, targetPath string) error {
	file, errOpen := os.Open(sourcePath)
	if errOpen != nil {
		return errOpen
	}
	temporaryPath := targetPath + ".migrating"
	if errMkdir := os.MkdirAll(filepath.Dir(targetPath), 0o755); errMkdir != nil {
		_ = file.Close()
		return errMkdir
	}
	output, errCreate := os.Create(temporaryPath)
	if errCreate != nil {
		_ = file.Close()
		return errCreate
	}
	buffered := bufio.NewWriterSize(output, 128*1024)
	errRead := readClientKeyUsageJSONLines(file, func(line []byte) error {
		var event clientKeyUsageLegacyEvent
		if errUnmarshal := json.Unmarshal(line, &event); errUnmarshal != nil {
			return nil
		}
		return writeClientKeyUsageInteraction(buffered, event.Prompt, event.Response)
	})
	errFlush := buffered.Flush()
	errOutputClose := output.Close()
	errInputClose := file.Close()
	if errRead != nil || errFlush != nil || errOutputClose != nil || errInputClose != nil {
		_ = os.Remove(temporaryPath)
		if errRead != nil {
			return errRead
		}
		if errFlush != nil {
			return errFlush
		}
		if errOutputClose != nil {
			return errOutputClose
		}
		return errInputClose
	}
	if errRename := os.Rename(temporaryPath, targetPath); errRename != nil {
		_ = os.Remove(temporaryPath)
		return errRename
	}
	return nil
}

func nextAvailableClientKeyUsageArchivePath(path string) string {
	if _, errStat := os.Stat(path); os.IsNotExist(errStat) {
		return path
	}
	extension := filepath.Ext(path)
	base := strings.TrimSuffix(path, extension)
	for index := 1; ; index++ {
		candidate := fmt.Sprintf("%s.%d%s", base, index, extension)
		if _, errStat := os.Stat(candidate); os.IsNotExist(errStat) {
			return candidate
		}
	}
}

func readClientKeyUsageJSONLines(reader io.Reader, visit func([]byte) error) error {
	if reader == nil || visit == nil {
		return nil
	}
	buffered := bufio.NewReaderSize(reader, 128*1024)
	for {
		line, errRead := buffered.ReadBytes('\n')
		line = []byte(strings.TrimSpace(string(line)))
		if len(line) > 0 {
			if errVisit := visit(line); errVisit != nil {
				return errVisit
			}
		}
		if errRead == io.EOF {
			return nil
		}
		if errRead != nil {
			return errRead
		}
	}
}
func clientKeyUsageTokenTotalsFromDetail(detail coreusage.Detail) clientKeyUsageTokenTotals {
	totals := clientKeyUsageTokenTotals{
		InputTokens:         detail.InputTokens,
		OutputTokens:        detail.OutputTokens,
		ReasoningTokens:     detail.ReasoningTokens,
		CachedTokens:        detail.CachedTokens,
		CacheReadTokens:     detail.CacheReadTokens,
		CacheCreationTokens: detail.CacheCreationTokens,
		TotalTokens:         detail.TotalTokens,
	}
	if totals.TotalTokens == 0 {
		totals.TotalTokens = totals.InputTokens + totals.OutputTokens + totals.ReasoningTokens
	}
	if totals.TotalTokens == 0 {
		totals.TotalTokens = totals.InputTokens + totals.OutputTokens + totals.ReasoningTokens + totals.CachedTokens
	}
	return totals
}

func (t *clientKeyUsageTokenTotals) add(other clientKeyUsageTokenTotals) {
	if t == nil {
		return
	}
	t.InputTokens += other.InputTokens
	t.OutputTokens += other.OutputTokens
	t.ReasoningTokens += other.ReasoningTokens
	t.CachedTokens += other.CachedTokens
	t.CacheReadTokens += other.CacheReadTokens
	t.CacheCreationTokens += other.CacheCreationTokens
	t.TotalTokens += other.TotalTokens
}

func (e *clientKeyUsageStateEntry) ensureTodayDate(today string) {
	if e == nil || today == "" || e.TodayDate == today {
		return
	}
	e.TodayDate = today
	e.TodayTokens = clientKeyUsageTokenTotals{}
	e.TodayRequests = 0
}

func pruneClientKeyUsageTerminals(entry *clientKeyUsageStateEntry, now time.Time) {
	if entry == nil || len(entry.terminals) == 0 {
		return
	}
	cutoff := now.Add(-clientKeyUsageActiveTerminalTTL)
	for terminalID, terminal := range entry.terminals {
		if terminal.LastSeenAt.Before(cutoff) {
			delete(entry.terminals, terminalID)
		}
	}
}

func clientKeyUsageActiveSessions(entry *clientKeyUsageStateEntry) []clientKeyUsageSession {
	if entry == nil || len(entry.terminals) == 0 {
		return nil
	}

	out := make([]clientKeyUsageSession, 0, len(entry.terminals))
	for terminalID, terminal := range entry.terminals {
		out = append(out, clientKeyUsageSession{
			Terminal:       clientKeyUsageTerminalDisplayID(terminalID),
			LastSeenAt:     terminal.LastSeenAt,
			Model:          terminal.Model,
			RequestedModel: terminal.RequestedModel,
			Provider:       terminal.Provider,
			Account:        terminal.Account,
			AuthID:         terminal.AuthID,
			AuthIndex:      terminal.AuthIndex,
			AuthType:       terminal.AuthType,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastSeenAt.After(out[j].LastSeenAt)
	})
	return out
}

func clientKeyUsageDate(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	return t.In(time.Local).Format("2006-01-02")
}

func clientKeyUsageKeyID(apiKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(apiKey)))
	return hex.EncodeToString(sum[:])
}

func clientKeyUsageTerminalID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil || ginCtx.Request == nil {
		return ""
	}

	headers := ginCtx.Request.Header
	for _, name := range []string{
		"X-Client-Instance-Id",
		"X-Device-Id",
		"X-Terminal-Id",
	} {
		if value := strings.TrimSpace(headers.Get(name)); value != "" {
			return clientKeyUsageTerminalHash(name + ":" + value)
		}
	}

	// Session、Thread、Conversation 和 Request ID 通常只代表一次请求或一段对话，
	// 同一台设备会持续产生不同值，因此不能用于统计物理终端数量。
	userAgent := strings.TrimSpace(headers.Get("User-Agent"))
	clientIP := strings.TrimSpace(ginCtx.ClientIP())
	if clientIP == "" {
		clientIP = strings.TrimSpace(ginCtx.Request.RemoteAddr)
	}
	if userAgent == "" && clientIP == "" {
		return ""
	}
	return clientKeyUsageTerminalHash(clientIP + "|" + userAgent)
}

func clientKeyUsageTerminalHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return clientKeyUsageTerminalHashPrefix + hex.EncodeToString(sum[:])
}

func clientKeyUsageTerminalDisplayID(terminalID string) string {
	terminalID = strings.TrimSpace(terminalID)
	if terminalID == "" {
		return ""
	}
	terminalID = strings.TrimPrefix(terminalID, clientKeyUsageTerminalHashPrefix)
	if len(terminalID) > 12 {
		return terminalID[:12]
	}
	return terminalID
}

func normalizeClientUsageKeys(keys []string) []string {
	out := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		trimmed := strings.TrimSpace(key)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	sort.Strings(out)
	return out
}

func maskClientUsageKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return strings.Repeat("*", len(key))
	}
	return key[:4] + "..." + key[len(key)-4:]
}

// GetClientKeyUsage returns token totals and recent terminal counts for configured client api-keys.
func (h *Handler) GetClientKeyUsage(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler not initialized"})
		return
	}

	h.mu.Lock()
	keys := []string(nil)
	keyNames := map[string]string(nil)
	if h.cfg != nil {
		keys = append(keys, h.cfg.APIKeys...)
		keyNames = normalizeAPIKeyNamesForKeys(keys, h.cfg.APIKeyNames)
	}
	h.mu.Unlock()

	c.JSON(http.StatusOK, globalClientKeyUsageCollector.Snapshot(keys, keyNames))
}

type clientKeyUsageConversationSavingRequest struct {
	SaveAllConversations *bool `json:"save_all_conversations"`
}

// GetClientKeyUsageConversationSaving 返回是否保存所有对话正文。
func (h *Handler) GetClientKeyUsageConversationSaving(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler not initialized"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"save_all_conversations": globalClientKeyUsageCollector.SaveAllConversations(),
	})
}

// PutClientKeyUsageConversationSaving 更新对话保存开关。
// 关闭后仅停止新增个人日志，历史日志和根目录汇总统计都保持不变。
func (h *Handler) PutClientKeyUsageConversationSaving(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler not initialized"})
		return
	}
	var request clientKeyUsageConversationSavingRequest
	if errBind := c.ShouldBindJSON(&request); errBind != nil || request.SaveAllConversations == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if errSave := globalClientKeyUsageCollector.SetSaveAllConversations(*request.SaveAllConversations); errSave != nil {
		log.WithError(errSave).Warn("failed to save client key conversation setting")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save conversation setting"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":                 "ok",
		"save_all_conversations": *request.SaveAllConversations,
	})
}

type clientKeyUsageOpenFolderRequest struct {
	KeyID string `json:"key_id"`
}

// OpenClientKeyUsageFolder 校验 Key 后打开对应使用人的本地统计文件夹。
// 接口只接受当前配置中存在的 KeyID，目录由服务端计算，客户端不能提交任意文件系统路径。
func (h *Handler) OpenClientKeyUsageFolder(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler not initialized"})
		return
	}

	var request clientKeyUsageOpenFolderRequest
	if errBind := c.ShouldBindJSON(&request); errBind != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	keyID := strings.TrimSpace(request.KeyID)
	if len(keyID) != sha256.Size*2 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid key_id"})
		return
	}

	h.mu.Lock()
	keys := []string(nil)
	keyNames := map[string]string(nil)
	openFolder := h.openFolder
	if h.cfg != nil {
		keys = normalizeClientUsageKeys(h.cfg.APIKeys)
		keyNames = normalizeAPIKeyNamesForKeys(keys, h.cfg.APIKeyNames)
	}
	h.mu.Unlock()

	selectedKey := ""
	for _, key := range keys {
		if clientKeyUsageKeyID(key) == keyID {
			selectedKey = key
			break
		}
	}
	if selectedKey == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "client key not found"})
		return
	}

	folderPath := globalClientKeyUsageCollector.UserFolderPath(keyNames[selectedKey], keyID)
	if folderPath == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "usage storage path unavailable"})
		return
	}
	if errMkdir := os.MkdirAll(folderPath, 0o755); errMkdir != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create usage folder"})
		return
	}
	if openFolder == nil {
		openFolder = openLocalFolder
	}
	if errOpen := openFolder(folderPath); errOpen != nil {
		log.WithError(errOpen).WithField("path", folderPath).Warn("failed to open client key usage folder")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to open usage folder"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
func resetClientKeyUsageCollectorForTest() {
	globalClientKeyUsageCollector = newClientKeyUsageCollector()
	coreusage.RegisterNamedPlugin(clientKeyUsagePluginName, globalClientKeyUsageCollector)
}
