/**
 * 本文件实现“用户使用信息统计”页面。
 * 默认视图只读取每个客户端 Key 的汇总数据，展示使用人、当天/历史 Token、请求次数和活跃终端。
 * 用户点击使用人或文件夹按钮时，由后端直接打开该使用人的本地日志目录。
 * 个人每日日志采用纯文本 .log，只保留 Input 和 Output，页面不再加载或展示逐次明细弹窗。
 * 页面提供“是否保存所有对话”开关；关闭后保留汇总统计，但停止生成新的个人对话日志。
 */
import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  IconEye,
  IconFolderOpen,
  IconKey,
  IconRefreshCw,
  IconSatellite,
  IconTimer,
} from '@/components/ui/icons';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { useAuthStore, useNotificationStore } from '@/stores';
import {
  clientKeyUsageApi,
  type ClientKeyUsageKeyReport,
  type ClientKeyUsageResponse,
} from '@/services/api';
import { formatDateTimeValue } from '@/utils/format';
import styles from './UserUsagePage.module.scss';

export function UserUsagePage() {
  const { t, i18n } = useTranslation();
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const showNotification = useNotificationStore((state) => state.showNotification);
  const [usage, setUsage] = useState<ClientKeyUsageResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState(false);
  const [openingFolderKeyId, setOpeningFolderKeyId] = useState('');
  const [saveAllConversations, setSaveAllConversations] = useState(true);
  const [conversationSettingAvailable, setConversationSettingAvailable] = useState(false);
  const [conversationSettingLoading, setConversationSettingLoading] = useState(true);
  const [conversationSettingSaving, setConversationSettingSaving] = useState(false);

  const numberFormatter = useMemo(() => new Intl.NumberFormat(i18n.language), [i18n.language]);

  const loadUsage = useCallback(async () => {
    if (connectionStatus !== 'connected') {
      setUsage(null);
      return;
    }
    setLoading(true);
    setLoadError(false);
    try {
      setUsage(await clientKeyUsageApi.getUsage());
    } catch {
      setUsage(null);
      setLoadError(true);
    } finally {
      setLoading(false);
    }
  }, [connectionStatus]);

  const loadConversationSaving = useCallback(async () => {
    if (connectionStatus !== 'connected') {
      setSaveAllConversations(true);
      setConversationSettingAvailable(false);
      setConversationSettingLoading(false);
      return;
    }
    setConversationSettingLoading(true);
    try {
      setSaveAllConversations(await clientKeyUsageApi.getConversationSaving());
      setConversationSettingAvailable(true);
    } catch {
      setConversationSettingAvailable(false);
    } finally {
      setConversationSettingLoading(false);
    }
  }, [connectionStatus]);

  useEffect(() => {
    void loadUsage();
    void loadConversationSaving();
  }, [loadConversationSaving, loadUsage]);

  const rows = useMemo(() => {
    const source = usage?.keys ?? [];
    return [...source].sort((left, right) => {
      const todayDiff = right.todayTokens.totalTokens - left.todayTokens.totalTokens;
      if (todayDiff !== 0) return todayDiff;
      const historyDiff = right.historyTokens.totalTokens - left.historyTokens.totalTokens;
      if (historyDiff !== 0) return historyDiff;
      return (left.ownerName || left.maskedKey).localeCompare(right.ownerName || right.maskedKey);
    });
  }, [usage]);

  const totals = useMemo(
    () =>
      rows.reduce(
        (summary, item) => ({
          todayTokens: summary.todayTokens + item.todayTokens.totalTokens,
          historyTokens: summary.historyTokens + item.historyTokens.totalTokens,
          activeTerminals: summary.activeTerminals + item.activeTerminals,
        }),
        { todayTokens: 0, historyTokens: 0, activeTerminals: 0 }
      ),
    [rows]
  );

  const formatNumber = useCallback(
    (value: number) => numberFormatter.format(Number.isFinite(value) ? value : 0),
    [numberFormatter]
  );

  const formatMillionTokens = useCallback(
    (value: number) => {
      const millions = (Number.isFinite(value) ? value : 0) / 1_000_000;
      return millions.toLocaleString(i18n.language, {
        minimumFractionDigits: 2,
        maximumFractionDigits: 2,
      });
    },
    [i18n.language]
  );

  const formatTimestamp = useCallback(
    (value: string | null) =>
      value
        ? formatDateTimeValue(value, i18n.language) || t('common.not_set')
        : t('common.not_set'),
    [i18n.language, t]
  );

  const openUserFolder = useCallback(
    async (user: ClientKeyUsageKeyReport) => {
      if (openingFolderKeyId) return;
      setOpeningFolderKeyId(user.keyId);
      try {
        await clientKeyUsageApi.openFolder(user.keyId);
        showNotification(t('user_usage.open_folder_success'), 'success');
      } catch {
        showNotification(t('user_usage.open_folder_failed'), 'error');
      } finally {
        setOpeningFolderKeyId('');
      }
    },
    [openingFolderKeyId, showNotification, t]
  );

  const updateConversationSaving = useCallback(
    async (enabled: boolean) => {
      if (conversationSettingSaving) return;
      const previous = saveAllConversations;
      setSaveAllConversations(enabled);
      setConversationSettingSaving(true);
      try {
        setSaveAllConversations(await clientKeyUsageApi.setConversationSaving(enabled));
        showNotification(t('user_usage.save_conversations_updated'), 'success');
      } catch {
        setSaveAllConversations(previous);
        showNotification(t('user_usage.save_conversations_update_failed'), 'error');
      } finally {
        setConversationSettingSaving(false);
      }
    },
    [conversationSettingSaving, saveAllConversations, showNotification, t]
  );

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <div>
          <h1>{t('user_usage.title')}</h1>
          <p>{t('user_usage.description')}</p>
        </div>
        <button
          type="button"
          className={styles.refreshButton}
          onClick={() => void loadUsage()}
          disabled={loading}
          title={t('common.refresh')}
        >
          <IconRefreshCw size={17} className={loading ? styles.spinning : undefined} />
          <span>{t('common.refresh')}</span>
        </button>
      </header>

      <section className={styles.summaryGrid} aria-label={t('user_usage.summary')}>
        <article className={styles.summaryItem}>
          <span className={`${styles.summaryIcon} ${styles.userIcon}`}>
            <IconKey size={20} />
          </span>
          <div>
            <small>{t('user_usage.users')}</small>
            <strong>{formatNumber(rows.length)}</strong>
          </div>
        </article>
        <article className={styles.summaryItem}>
          <span className={`${styles.summaryIcon} ${styles.todayIcon}`}>
            <IconTimer size={20} />
          </span>
          <div>
            <small>{t('user_usage.today_tokens')}</small>
            <strong>{formatMillionTokens(totals.todayTokens)}</strong>
          </div>
        </article>
        <article className={styles.summaryItem}>
          <span className={`${styles.summaryIcon} ${styles.historyIcon}`}>
            <IconSatellite size={20} />
          </span>
          <div>
            <small>{t('user_usage.history_tokens')}</small>
            <strong>{formatMillionTokens(totals.historyTokens)}</strong>
          </div>
        </article>
        <article className={styles.summaryItem}>
          <span className={`${styles.summaryIcon} ${styles.activeIcon}`}>
            <IconEye size={20} />
          </span>
          <div>
            <small>{t('user_usage.active_terminals')}</small>
            <strong>{formatNumber(totals.activeTerminals)}</strong>
          </div>
        </article>
      </section>

      <section className={styles.usersSection}>
        <div className={styles.sectionHeader}>
          <div>
            <h2>{t('user_usage.user_list')}</h2>
            <p>{t('user_usage.user_list_desc')}</p>
          </div>
          <div className={styles.sectionActions}>
            <ToggleSwitch
              checked={saveAllConversations}
              onChange={(enabled) => void updateConversationSaving(enabled)}
              disabled={
                connectionStatus !== 'connected' ||
                !conversationSettingAvailable ||
                conversationSettingLoading ||
                conversationSettingSaving
              }
              label={t('user_usage.save_all_conversations')}
              labelPosition="left"
              ariaLabel={t('user_usage.save_all_conversations')}
            />
            <span className={styles.dateBadge}>{usage?.date || t('common.not_set')}</span>
          </div>
        </div>

        {loading ? (
          <div className={styles.state}>{t('common.loading')}</div>
        ) : loadError ? (
          <div className={styles.state}>{t('user_usage.load_failed')}</div>
        ) : rows.length === 0 ? (
          <div className={styles.state}>{t('user_usage.empty')}</div>
        ) : (
          <div className={styles.tableWrap}>
            <table className={styles.table}>
              <thead>
                <tr>
                  <th>{t('user_usage.owner')}</th>
                  <th>{t('user_usage.key')}</th>
                  <th>{t('user_usage.today_tokens')}</th>
                  <th>{t('user_usage.history_tokens')}</th>
                  <th>{t('user_usage.active_terminals')}</th>
                  <th>{t('user_usage.requests')}</th>
                  <th>{t('user_usage.last_used')}</th>
                  <th className={styles.conversationFileColumn}>
                    {t('user_usage.conversation_files')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {rows.map((item) => (
                  <tr key={item.keyId}>
                    <td className={styles.conversationFileColumn}>
                      <button
                        type="button"
                        className={styles.ownerButton}
                        onClick={() => void openUserFolder(item)}
                        disabled={Boolean(openingFolderKeyId)}
                        title={t('user_usage.open_folder')}
                      >
                        {item.ownerName || t('user_usage.unassigned_owner')}
                      </button>
                    </td>
                    <td>
                      <code className={styles.keyValue}>{item.maskedKey}</code>
                    </td>
                    <td className={styles.metric}>
                      {formatMillionTokens(item.todayTokens.totalTokens)}
                    </td>
                    <td className={styles.metric}>
                      {formatMillionTokens(item.historyTokens.totalTokens)}
                    </td>
                    <td>
                      <span className={styles.activeValue}>
                        {formatNumber(item.activeTerminals)}
                      </span>
                    </td>
                    <td className={styles.metric}>
                      {formatNumber(item.todayRequests)} / {formatNumber(item.historyRequests)}
                    </td>
                    <td className={styles.lastUsed}>{formatTimestamp(item.lastUsedAt)}</td>
                    <td>
                      <button
                        type="button"
                        className={styles.detailButton}
                        onClick={() => void openUserFolder(item)}
                        disabled={Boolean(openingFolderKeyId)}
                        title={t('user_usage.open_folder')}
                        aria-label={t('user_usage.open_folder')}
                      >
                        <IconFolderOpen
                          size={17}
                          className={
                            openingFolderKeyId === item.keyId ? styles.spinning : undefined
                          }
                        />
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </div>
  );
}
