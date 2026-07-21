// 本文件实现管理中心客户端 API Key 用量接口封装。
// 具体内容：
// 1. 请求后端 /client-key-usage 汇总接口，为仪表盘和用户统计页提供每个 Key 的累计数据。
// 2. 请求 /client-key-usage/open-folder 接口，由服务端打开指定使用人的本地日志文件夹。
// 3. 读取和更新 /client-key-usage/conversation-saving，控制是否继续保存所有个人对话。
// 4. 将汇总接口的 snake_case 字段转换为前端 camelCase，并为缺失字段提供稳定默认值。
import { isRecord } from '@/utils/helpers';
import { apiClient } from './client';

const CLIENT_KEY_USAGE_TIMEOUT_MS = 15 * 1000;

export interface ClientKeyUsageTokenTotals {
  inputTokens: number;
  outputTokens: number;
  reasoningTokens: number;
  cachedTokens: number;
  cacheReadTokens: number;
  cacheCreationTokens: number;
  totalTokens: number;
}

export interface ClientKeyUsageActiveSession {
  terminal: string;
  lastSeenAt: string | null;
  model: string;
  requestedModel: string;
  provider: string;
  account: string;
  authId: string;
  authIndex: string;
  authType: string;
}

export interface ClientKeyUsageKeyReport {
  key: string;
  keyId: string;
  maskedKey: string;
  ownerName: string;
  historyTokens: ClientKeyUsageTokenTotals;
  todayTokens: ClientKeyUsageTokenTotals;
  historyRequests: number;
  todayRequests: number;
  activeTerminals: number;
  activeSessions: ClientKeyUsageActiveSession[];
  lastUsedAt: string | null;
}

export interface ClientKeyUsageResponse {
  date: string;
  activeWindowSeconds: number;
  keys: ClientKeyUsageKeyReport[];
}

const emptyTokenTotals = (): ClientKeyUsageTokenTotals => ({
  inputTokens: 0,
  outputTokens: 0,
  reasoningTokens: 0,
  cachedTokens: 0,
  cacheReadTokens: 0,
  cacheCreationTokens: 0,
  totalTokens: 0,
});

const numericField = (record: Record<string, unknown>, key: string): number => {
  const value = record[key];
  return typeof value === 'number' && Number.isFinite(value) ? value : 0;
};

const stringField = (record: Record<string, unknown>, key: string): string => {
  const value = record[key];
  return typeof value === 'string' ? value : '';
};

const normalizeTokens = (value: unknown): ClientKeyUsageTokenTotals => {
  if (!isRecord(value)) return emptyTokenTotals();

  return {
    inputTokens: numericField(value, 'input_tokens'),
    outputTokens: numericField(value, 'output_tokens'),
    reasoningTokens: numericField(value, 'reasoning_tokens'),
    cachedTokens: numericField(value, 'cached_tokens'),
    cacheReadTokens: numericField(value, 'cache_read_tokens'),
    cacheCreationTokens: numericField(value, 'cache_creation_tokens'),
    totalTokens: numericField(value, 'total_tokens'),
  };
};

const normalizeActiveSession = (value: unknown): ClientKeyUsageActiveSession | null => {
  if (!isRecord(value)) return null;

  return {
    terminal: stringField(value, 'terminal'),
    lastSeenAt: stringField(value, 'last_seen_at') || null,
    model: stringField(value, 'model'),
    requestedModel: stringField(value, 'requested_model'),
    provider: stringField(value, 'provider'),
    account: stringField(value, 'account'),
    authId: stringField(value, 'auth_id'),
    authIndex: stringField(value, 'auth_index'),
    authType: stringField(value, 'auth_type'),
  };
};

const normalizeKeyReport = (value: unknown): ClientKeyUsageKeyReport | null => {
  if (!isRecord(value)) return null;

  const rawActiveSessions = Array.isArray(value.active_sessions) ? value.active_sessions : [];
  return {
    key: stringField(value, 'key'),
    keyId: stringField(value, 'key_id'),
    maskedKey: stringField(value, 'masked_key'),
    ownerName: stringField(value, 'owner_name'),
    historyTokens: normalizeTokens(value.history_tokens),
    todayTokens: normalizeTokens(value.today_tokens),
    historyRequests: numericField(value, 'history_requests'),
    todayRequests: numericField(value, 'today_requests'),
    activeTerminals: numericField(value, 'active_terminals'),
    activeSessions: rawActiveSessions
      .map(normalizeActiveSession)
      .filter((item): item is ClientKeyUsageActiveSession => item !== null),
    lastUsedAt: stringField(value, 'last_used_at') || null,
  };
};

const normalizeResponse = (value: unknown): ClientKeyUsageResponse => {
  if (!isRecord(value)) {
    return { date: '', activeWindowSeconds: 0, keys: [] };
  }

  const rawKeys = Array.isArray(value.keys) ? value.keys : [];
  return {
    date: stringField(value, 'date'),
    activeWindowSeconds: numericField(value, 'active_window_seconds'),
    keys: rawKeys
      .map(normalizeKeyReport)
      .filter((item): item is ClientKeyUsageKeyReport => item !== null),
  };
};

const normalizeConversationSaving = (value: unknown): boolean =>
  isRecord(value) && value.save_all_conversations !== false;

export const clientKeyUsageApi = {
  getUsage: async () => {
    const data = await apiClient.get<unknown>('/client-key-usage', {
      timeout: CLIENT_KEY_USAGE_TIMEOUT_MS,
    });
    return normalizeResponse(data);
  },

  openFolder: async (keyId: string) => {
    await apiClient.post(
      '/client-key-usage/open-folder',
      { key_id: keyId },
      {
        timeout: CLIENT_KEY_USAGE_TIMEOUT_MS,
      }
    );
  },

  getConversationSaving: async () => {
    const data = await apiClient.get<unknown>('/client-key-usage/conversation-saving', {
      timeout: CLIENT_KEY_USAGE_TIMEOUT_MS,
    });
    return normalizeConversationSaving(data);
  },

  setConversationSaving: async (enabled: boolean) => {
    const data = await apiClient.put<unknown>(
      '/client-key-usage/conversation-saving',
      { save_all_conversations: enabled },
      { timeout: CLIENT_KEY_USAGE_TIMEOUT_MS }
    );
    return normalizeConversationSaving(data);
  },
};
