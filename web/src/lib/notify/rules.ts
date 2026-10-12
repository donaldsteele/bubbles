// Exact mirror of pkg/notifyrules Decide. Both are tested against
// testdata/notify-rules.json; change them together.

export interface NotifyMentions {
  user_ids: string[] | null;
  channel: boolean;
  here: boolean;
}

export interface NotifyInput {
  recipient_id: string;
  author_id: string;
  channel_kind: string;
  notify_level: string;
  alert_min_severity: string;
  /** Unix ms, or null when not muted. */
  muted_until: number | null;
  /** Unix ms. */
  now: number;
  mentions: NotifyMentions;
  recipient_online: boolean;
  message_kind: string;
  alert_transition: string;
  alert_severity: string;
}

export interface NotifyDecision {
  notify: boolean;
  reason: string;
}

const yes = (reason: string): NotifyDecision => ({ notify: true, reason });
const no = (reason: string): NotifyDecision => ({ notify: false, reason });

function severityRank(s: string): number | undefined {
  switch (s) {
    case 'info':
      return 0;
    case 'warning':
      return 1;
    case 'critical':
      return 2;
    default:
      return undefined;
  }
}

export function decide(input: NotifyInput): NotifyDecision {
  switch (input.message_kind) {
    case 'system':
      return no('system_message');
    case 'text':
    case 'alert':
      break;
    default:
      return no('unknown_message_kind');
  }
  if (input.recipient_id !== '' && input.recipient_id === input.author_id) {
    return no('own_message');
  }
  if (input.muted_until !== null && input.now < input.muted_until) return no('muted');
  let critical = false;
  if (input.message_kind === 'alert') {
    switch (input.alert_transition) {
      case 'created':
      case 'escalated':
      case 'renotify_due':
        break;
      default:
        return no('alert_transition');
    }
    const sev = severityRank(input.alert_severity);
    if (sev === undefined) return no('alert_severity_invalid');
    const floor = severityRank(input.alert_min_severity);
    if (floor === undefined) return no('alert_min_severity_invalid');
    if (sev < floor) return no('below_alert_min_severity');
    critical = input.alert_severity === 'critical';
  }
  if (input.channel_kind === 'dm' || input.channel_kind === 'group_dm') {
    return yes('direct_message');
  }
  switch (input.notify_level) {
    case 'all':
      return yes('level_all');
    case 'mentions': {
      const m = input.mentions;
      if (input.recipient_id !== '' && (m.user_ids ?? []).includes(input.recipient_id)) {
        return yes('mentioned');
      }
      if (m.channel) return yes('channel_mention');
      if (m.here && input.recipient_online) return yes('here_mention');
      if (critical) return yes('critical_alert');
      return no('not_mentioned');
    }
    case 'none':
      return no('level_none');
    default:
      return no('unknown_notify_level');
  }
}
