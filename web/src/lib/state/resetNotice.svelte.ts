// "This server was reset" notice. It is shown on a device that learns about a
// reset it did not start: a 4010 / 4011 close or a changed instance id / epoch
// on reconnect. A factory reset reloads the page, so the level survives in
// sessionStorage (under a key outside the bubbles: prefix a reset clears).
export type NoticeLevel = 'factory' | 'content';

export const NOTICE_KEY = 'bubbles-reset-notice';

class ResetNotice {
  level = $state<NoticeLevel | null>(null);

  /** Records a reset; a factory reset also survives the coming page reload. */
  show(level: NoticeLevel): void {
    this.level = level;
    if (level === 'factory') {
      try {
        sessionStorage.setItem(NOTICE_KEY, level);
      } catch {
        // storage unavailable: the notice is lost with the reload
      }
    }
  }

  /** Picks up a notice stored before the reload; call once at start. */
  restore(): void {
    try {
      const v = sessionStorage.getItem(NOTICE_KEY);
      sessionStorage.removeItem(NOTICE_KEY);
      if (v === 'factory' || v === 'content') this.level = v;
    } catch {
      // ignore
    }
  }

  dismiss(): void {
    this.level = null;
    try {
      sessionStorage.removeItem(NOTICE_KEY);
    } catch {
      // ignore
    }
  }
}

export const resetNotice = new ResetNotice();
