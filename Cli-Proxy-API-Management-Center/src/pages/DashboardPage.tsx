// 本文件实现管理中心仪表盘页面。
// 具体内容：
// 1. 展示系统概览、客户端 Key 用量、当前配置和服务连接状态。
// 2. 读取认证文件、模型列表和后端基础配置，组成首页运行总览。
// 3. 新增客户端 API Key 用量面板，按每个下游 Key 展示历史 token、当天 token、请求次数和活跃终端数。
// 4. 新增当前活跃会话明细，展示正在使用的 Key、使用人、模型、上游账户和终端。
// 5. 所有用量数字按当前界面语言格式化，Key 只显示后端返回的脱敏值，避免在页面泄露完整密钥。
import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import {
  IconKey,
  IconBot,
  IconFileText,
  IconSatellite,
  IconSidebarQuickStart,
} from '@/components/ui/icons';
import { useAuthStore, useConfigStore, useModelsStore } from '@/stores';
import {
  authFilesApi,
  clientKeyUsageApi,
  type ClientKeyUsageActiveSession,
  type ClientKeyUsageResponse,
} from '@/services/api';
import { useApiKeysForModels } from '@/hooks/useApiKeysForModels';
import { hasApiKeyFunConfig } from '@/features/providers/sponsor';
import { formatDateTimeValue } from '@/utils/format';
import { getDashboardModelsStatValue } from '@/utils/dashboard';
import styles from './DashboardPage.module.scss';

interface QuickStat {
  label: string;
  value: number | string;
  icon: React.ReactNode;
  path: string;
  loading?: boolean;
  sublabel?: string;
}

interface ClientKeyUsageActiveRow {
  key: string;
  maskedKey: string;
  ownerName: string;
  session: ClientKeyUsageActiveSession;
}

export function DashboardPage() {
  const { t, i18n } = useTranslation();
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const apiBase = useAuthStore((state) => state.apiBase);
  const config = useConfigStore((state) => state.config);
  const fetchConfig = useConfigStore((state) => state.fetchConfig);

  const models = useModelsStore((state) => state.models);
  const modelsLoading = useModelsStore((state) => state.loading);
  const modelsError = useModelsStore((state) => state.error);
  const fetchModelsFromStore = useModelsStore((state) => state.fetchModels);

  const [authFilesCount, setAuthFilesCount] = useState<number | null>(null);
  const [authFilesLoading, setAuthFilesLoading] = useState(false);
  const [clientKeyUsageData, setClientKeyUsageData] = useState<ClientKeyUsageResponse | null>(null);
  const [clientKeyUsageLoading, setClientKeyUsageLoading] = useState(false);
  const [clientKeyUsageError, setClientKeyUsageError] = useState(false);

  const resolveApiKeysForModels = useApiKeysForModels();

  const fetchModels = useCallback(async () => {
    if (connectionStatus !== 'connected' || !apiBase) {
      return;
    }

    try {
      const apiKeys = await resolveApiKeysForModels();
      const primaryKey = apiKeys[0];
      await fetchModelsFromStore(apiBase, primaryKey);
    } catch {
      // 仪表盘模型数只作为概览展示，失败时保留 store 内已有状态。
    }
  }, [connectionStatus, apiBase, resolveApiKeysForModels, fetchModelsFromStore]);

  useEffect(() => {
    if (connectionStatus !== 'connected') {
      setClientKeyUsageData(null);
      setClientKeyUsageError(false);
      setClientKeyUsageLoading(false);
      return;
    }

    let cancelled = false;

    const loadAuthFiles = async () => {
      setAuthFilesLoading(true);
      try {
        const res = await authFilesApi.list();
        if (!cancelled) setAuthFilesCount(res.files.length);
      } catch {
        if (!cancelled) setAuthFilesCount(null);
      } finally {
        if (!cancelled) setAuthFilesLoading(false);
      }
    };

    // 客户端 Key 用量来自后端持久化统计，页面只负责读取和展示。
    const loadClientKeyUsage = async () => {
      setClientKeyUsageLoading(true);
      setClientKeyUsageError(false);
      try {
        const res = await clientKeyUsageApi.getUsage();
        if (!cancelled) setClientKeyUsageData(res);
      } catch {
        if (!cancelled) {
          setClientKeyUsageError(true);
          setClientKeyUsageData(null);
        }
      } finally {
        if (!cancelled) setClientKeyUsageLoading(false);
      }
    };

    // 提供商/密钥统计直接来自 config store；这里只需保证配置已加载并取认证文件数。
    fetchConfig().catch(() => undefined);
    fetchModels();
    void loadAuthFiles();
    void loadClientKeyUsage();

    return () => {
      cancelled = true;
    };
  }, [connectionStatus, fetchConfig, fetchModels]);

  const configLoading = !config;
  const providerStats = config
    ? {
        gemini: config.geminiApiKeys?.length ?? 0,
        codex: config.codexApiKeys?.length ?? 0,
        claude: config.claudeApiKeys?.length ?? 0,
        vertex: config.vertexApiKeys?.length ?? 0,
        openai: config.openaiCompatibility?.length ?? 0,
      }
    : null;
  const totalProviderKeys = providerStats
    ? Object.values(providerStats).reduce((sum, count) => sum + count, 0)
    : 0;
  const isApiKeyFunConfigured = hasApiKeyFunConfig(config);

  const quickStats: QuickStat[] = [
    {
      label: t('dashboard.management_keys'),
      value: config ? (config.apiKeys?.length ?? 0) : '-',
      icon: <IconKey size={24} />,
      path: '/config',
      loading: configLoading,
      sublabel: t('nav.config_management'),
    },
    {
      label: t('nav.ai_providers'),
      value: providerStats ? totalProviderKeys : '-',
      icon: <IconBot size={24} />,
      path: '/ai-providers',
      loading: configLoading,
      sublabel: providerStats
        ? t('dashboard.provider_keys_detail', {
            gemini: providerStats.gemini,
            codex: providerStats.codex,
            claude: providerStats.claude,
            vertex: providerStats.vertex,
            openai: providerStats.openai,
          })
        : undefined,
    },
    {
      label: t('nav.auth_files'),
      value: authFilesCount ?? '-',
      icon: <IconFileText size={24} />,
      path: '/auth-files',
      loading: authFilesLoading && authFilesCount === null,
      sublabel: t('dashboard.oauth_credentials'),
    },
    {
      label: t('dashboard.available_models'),
      value: getDashboardModelsStatValue(models.length, modelsLoading, modelsError),
      icon: <IconSatellite size={24} />,
      path: '/system',
      loading: modelsLoading,
      sublabel: t('dashboard.available_models_desc'),
    },
    ...(!isApiKeyFunConfigured
      ? [
          {
            label: t('dashboard.quick_start_card'),
            value: t('dashboard.quick_start_entry'),
            icon: <IconSidebarQuickStart size={24} />,
            path: '/quick-start',
            sublabel: t('dashboard.quick_start_entry_desc'),
          },
        ]
      : []),
  ];

  const routingStrategyRaw = config?.routingStrategy?.trim() || '';
  const routingStrategyDisplay = !routingStrategyRaw
    ? '-'
    : routingStrategyRaw === 'round-robin'
      ? t('basic_settings.routing_strategy_round_robin')
      : routingStrategyRaw === 'fill-first'
        ? t('basic_settings.routing_strategy_fill_first')
        : routingStrategyRaw;
  const routingStrategyBadgeClass = !routingStrategyRaw
    ? styles.configBadgeUnknown
    : routingStrategyRaw === 'round-robin'
      ? styles.configBadgeRoundRobin
      : routingStrategyRaw === 'fill-first'
        ? styles.configBadgeFillFirst
        : styles.configBadgeUnknown;

  const numberFormatter = useMemo(() => new Intl.NumberFormat(i18n.language), [i18n.language]);

  // 将用量行按“今日 token 优先、历史 token 兜底”排序，让最活跃的 Key 靠前。
  const clientKeyUsageRows = useMemo(() => {
    const rows = clientKeyUsageData?.keys ?? [];
    return [...rows].sort((left, right) => {
      const todayDiff = right.todayTokens.totalTokens - left.todayTokens.totalTokens;
      if (todayDiff !== 0) return todayDiff;

      const historyDiff = right.historyTokens.totalTokens - left.historyTokens.totalTokens;
      if (historyDiff !== 0) return historyDiff;

      return left.maskedKey.localeCompare(right.maskedKey);
    });
  }, [clientKeyUsageData]);

  // 当前使用明细按最近活动时间排序，一行代表一个 Key 的一个活跃终端。
  const activeUsageRows = useMemo<ClientKeyUsageActiveRow[]>(() => {
    const rows = clientKeyUsageRows.flatMap((item) =>
      item.activeSessions.map((session) => ({
        key: item.key,
        maskedKey: item.maskedKey,
        ownerName: item.ownerName,
        session,
      }))
    );

    return rows.sort((left, right) => {
      const leftTime = Date.parse(left.session.lastSeenAt ?? '');
      const rightTime = Date.parse(right.session.lastSeenAt ?? '');
      return (
        (Number.isFinite(rightTime) ? rightTime : 0) - (Number.isFinite(leftTime) ? leftTime : 0)
      );
    });
  }, [clientKeyUsageRows]);

  const formatNumber = useCallback(
    (value: number) => numberFormatter.format(Number.isFinite(value) ? value : 0),
    [numberFormatter]
  );

  const formatMillionTokens = useCallback(
    (value: number) =>
      new Intl.NumberFormat(i18n.language, {
        maximumFractionDigits: 2,
      }).format((Number.isFinite(value) ? value : 0) / 1_000_000),
    [i18n.language]
  );

  const formatLastUsed = useCallback(
    (value: string | null) =>
      value
        ? formatDateTimeValue(value, i18n.language) || t('dashboard.never_used')
        : t('dashboard.never_used'),
    [i18n.language, t]
  );

  const activeWindowMinutes = Math.max(
    1,
    Math.round((clientKeyUsageData?.activeWindowSeconds ?? 600) / 60)
  );

  const formatSessionModel = useCallback(
    (session: ClientKeyUsageActiveSession) =>
      session.requestedModel || session.model || t('common.not_set'),
    [t]
  );

  const formatSessionAccount = useCallback(
    (session: ClientKeyUsageActiveSession) =>
      session.account ||
      session.authIndex ||
      session.authId ||
      session.authType ||
      t('common.not_set'),
    [t]
  );

  return (
    <div className={styles.dashboard}>
      {/* 页面背景装饰层，保持在内容下方且不参与交互。 */}
      <div className={styles.backgroundOrbs} aria-hidden="true">
        <div className={styles.orb1} />
        <div className={styles.orb2} />
      </div>

      {/* 系统概览：快速入口与关键数量统计。 */}
      <section className={styles.statsSection}>
        <h2 className={styles.sectionHeading}>{t('dashboard.system_overview')}</h2>
        <div className={styles.bentoGrid}>
          {quickStats.map((stat, index) => (
            <Link
              key={stat.path}
              to={stat.path}
              className={`${styles.bentoCard} ${index === 0 ? styles.bentoLarge : ''}`}
              style={{ animationDelay: `${index * 80}ms` }}
            >
              <div className={styles.bentoIcon}>{stat.icon}</div>
              <div className={styles.bentoContent}>
                <span className={styles.bentoValue}>{stat.loading ? '...' : stat.value}</span>
                <span className={styles.bentoLabel}>{stat.label}</span>
                {stat.sublabel && !stat.loading && (
                  <span className={styles.bentoSublabel}>{stat.sublabel}</span>
                )}
              </div>
            </Link>
          ))}
        </div>
      </section>

      {/* 客户端 Key 用量：按下游管理密钥分别展示 token、请求和活跃终端。 */}
      <section className={styles.clientUsageSection}>
        <div className={styles.clientUsageHeader}>
          <div>
            <h2 className={styles.sectionHeading}>{t('dashboard.client_key_usage')}</h2>
            <p className={styles.clientUsageDesc}>
              {t('dashboard.client_key_usage_desc', { minutes: activeWindowMinutes })}
            </p>
          </div>
          {clientKeyUsageLoading && (
            <span className={styles.clientUsageLoading}>{t('common.loading')}</span>
          )}
        </div>

        <div className={styles.clientUsagePanel}>
          {clientKeyUsageError ? (
            <div className={styles.clientUsageState}>{t('dashboard.usage_load_failed')}</div>
          ) : clientKeyUsageRows.length === 0 && !clientKeyUsageLoading ? (
            <div className={styles.clientUsageState}>{t('dashboard.no_client_keys')}</div>
          ) : (
            <>
              <div className={styles.activeUsageBlock}>
                <div className={styles.activeUsageTitleRow}>
                  <div>
                    <h3 className={styles.activeUsageTitle}>
                      {t('dashboard.active_key_sessions')}
                    </h3>
                    <p className={styles.activeUsageDesc}>
                      {t('dashboard.active_key_sessions_desc', { count: activeUsageRows.length })}
                    </p>
                  </div>
                </div>

                {activeUsageRows.length === 0 ? (
                  <div className={styles.activeUsageEmpty}>
                    {t('dashboard.current_usage_empty')}
                  </div>
                ) : (
                  <div className={styles.activeUsageGrid}>
                    {activeUsageRows.map(({ key, maskedKey, ownerName, session }) => (
                      <div
                        className={styles.activeUsageCard}
                        key={`${key}-${session.terminal}-${session.lastSeenAt ?? ''}`}
                      >
                        <div className={styles.activeUsageCardTop}>
                          <code className={styles.clientUsageKey}>
                            {maskedKey || t('common.not_set')}
                          </code>
                          <span className={styles.activeUsageProvider}>
                            {session.provider || t('common.not_set')}
                          </span>
                        </div>
                        <dl className={styles.activeUsageMeta}>
                          <div>
                            <dt>{t('dashboard.owner')}</dt>
                            <dd>{ownerName || t('dashboard.unassigned_owner')}</dd>
                          </div>
                          <div>
                            <dt>{t('dashboard.model')}</dt>
                            <dd>{formatSessionModel(session)}</dd>
                          </div>
                          <div>
                            <dt>{t('dashboard.account')}</dt>
                            <dd>{formatSessionAccount(session)}</dd>
                          </div>
                          <div>
                            <dt>{t('dashboard.terminal')}</dt>
                            <dd>{session.terminal || t('common.not_set')}</dd>
                          </div>
                          <div>
                            <dt>{t('dashboard.last_active')}</dt>
                            <dd>{formatLastUsed(session.lastSeenAt)}</dd>
                          </div>
                        </dl>
                      </div>
                    ))}
                  </div>
                )}
              </div>

              <div className={styles.clientUsageTableWrap}>
                <table className={styles.clientUsageTable}>
                  <thead>
                    <tr>
                      <th>{t('dashboard.key')}</th>
                      <th>{t('dashboard.owner')}</th>
                      <th>{t('dashboard.today_tokens')}</th>
                      <th>{t('dashboard.history_tokens')}</th>
                      <th>{t('dashboard.active_terminals')}</th>
                      <th>{t('dashboard.requests')}</th>
                      <th>{t('dashboard.last_used')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {clientKeyUsageRows.map((item) => (
                      <tr key={item.key}>
                        <td>
                          <div className={styles.clientUsageKeyCell}>
                            <code className={styles.clientUsageKey}>
                              {item.maskedKey || t('common.not_set')}
                            </code>
                          </div>
                        </td>
                        <td>
                          <span className={styles.clientUsageOwner}>
                            {item.ownerName || t('dashboard.unassigned_owner')}
                          </span>
                        </td>
                        <td>
                          <div className={styles.clientUsageMetric}>
                            <span>{formatMillionTokens(item.todayTokens.totalTokens)}</span>
                          </div>
                        </td>
                        <td>
                          <div className={styles.clientUsageMetric}>
                            <span>{formatMillionTokens(item.historyTokens.totalTokens)}</span>
                          </div>
                        </td>
                        <td>
                          <span className={styles.activeTerminalValue}>
                            {formatNumber(item.activeTerminals)}
                          </span>
                        </td>
                        <td>
                          <div className={styles.clientUsageMetric}>
                            <span>
                              {formatNumber(item.todayRequests)} /{' '}
                              {formatNumber(item.historyRequests)}
                            </span>
                            <small>{t('dashboard.today_history_requests')}</small>
                          </div>
                        </td>
                        <td className={styles.clientUsageLastUsed}>
                          {formatLastUsed(item.lastUsedAt)}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </>
          )}
        </div>
      </section>

      {/* 当前配置：展示最常用的基础开关与路由策略。 */}
      {config && (
        <section className={styles.configSection}>
          <h2 className={styles.sectionHeading}>{t('dashboard.current_config')}</h2>
          <div className={styles.configPillGrid}>
            <div className={styles.configPill}>
              <span className={styles.configPillLabel}>{t('basic_settings.debug_enable')}</span>
              <span
                className={`${styles.configPillValue} ${config.debug ? styles.on : styles.off}`}
              >
                {config.debug ? t('common.yes') : t('common.no')}
              </span>
            </div>
            <div className={styles.configPill}>
              <span className={styles.configPillLabel}>
                {t('basic_settings.logging_to_file_enable')}
              </span>
              <span
                className={`${styles.configPillValue} ${config.loggingToFile ? styles.on : styles.off}`}
              >
                {config.loggingToFile ? t('common.yes') : t('common.no')}
              </span>
            </div>
            <div className={styles.configPill}>
              <span className={styles.configPillLabel}>
                {t('basic_settings.retry_count_label')}
              </span>
              <span className={styles.configPillValue}>{config.requestRetry ?? 0}</span>
            </div>
            <div className={styles.configPill}>
              <span className={styles.configPillLabel}>{t('basic_settings.ws_auth_enable')}</span>
              <span
                className={`${styles.configPillValue} ${config.wsAuth ? styles.on : styles.off}`}
              >
                {config.wsAuth ? t('common.yes') : t('common.no')}
              </span>
            </div>
            <div className={styles.configPill}>
              <span className={styles.configPillLabel}>{t('dashboard.routing_strategy')}</span>
              <span className={`${styles.configBadge} ${routingStrategyBadgeClass}`}>
                {routingStrategyDisplay}
              </span>
            </div>
            {config.proxyUrl && (
              <div className={`${styles.configPill} ${styles.configPillWide}`}>
                <span className={styles.configPillLabel}>
                  {t('basic_settings.proxy_url_label')}
                </span>
                <span className={styles.configPillMono}>{config.proxyUrl}</span>
              </div>
            )}
          </div>
          <Link to="/config" className={styles.viewMoreLink}>
            {t('dashboard.edit_settings')} →
          </Link>
        </section>
      )}
    </div>
  );
}
