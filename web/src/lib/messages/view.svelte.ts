// Windowed view of one channel's top-level messages: bidirectional pagination
// over the message store with the 400-message DOM cap.
import { api, call } from '../api/client';
import type { Message } from '../api/types';
import { messages } from '../state/messages.svelte';
import { DOM_CAP, capWindow, mergeItems } from './paging';

export const PAGE_SIZE = 50;

export interface PageQuery {
  before?: string;
  after?: string;
  around?: string;
  limit: number;
}

export interface PageResult {
  items: Message[];
  has_more_before: boolean;
  has_more_after: boolean;
}

export type PageFetcher = (channelId: string, q: PageQuery) => Promise<PageResult>;

export const fetchPage: PageFetcher = (channelId, q) =>
  call(
    api.GET('/channels/{id}/messages', {
      params: { path: { id: channelId }, query: q },
    }),
  );

export type LoadState = 'idle' | 'initial' | 'older' | 'newer';

export class MessageWindow {
  channelId = $state('');
  hasBefore = $state(false);
  hasAfter = $state(false);
  loading = $state<LoadState>('idle');
  failed = $state(false);
  /** Bumped whenever the window content was replaced wholesale (scroll resets). */
  generation = $state(0);

  #fetch: PageFetcher;
  #cap: number;
  /** Newest id inside the window while newer messages exist beyond it. */
  #maxId = '';
  #token = 0;

  constructor(fetcher: PageFetcher = fetchPage, cap = DOM_CAP) {
    this.#fetch = fetcher;
    this.#cap = cap;
  }

  /** Messages inside the window (live arrivals past the window are hidden). */
  get items(): Message[] {
    const all = messages.list(this.channelId);
    if (!this.hasAfter || !this.#maxId) return all;
    return all.filter((m) => m.id <= this.#maxId);
  }

  #store(items: Message[]): void {
    messages.setChannelPage(this.channelId, items);
    this.#maxId = this.hasAfter ? (items[items.length - 1]?.id ?? '') : '';
  }

  /** Loads the newest page, or a page centred on aroundId. */
  async open(channelId: string, aroundId: string | null = null): Promise<void> {
    const token = ++this.#token;
    this.channelId = channelId;
    this.loading = 'initial';
    this.failed = false;
    try {
      const page = await this.#fetch(channelId, {
        limit: PAGE_SIZE,
        ...(aroundId ? { around: aroundId } : {}),
      });
      if (token !== this.#token) return;
      this.hasBefore = page.has_more_before;
      this.hasAfter = page.has_more_after;
      this.#store(page.items);
      this.generation += 1;
    } catch {
      if (token !== this.#token) return;
      this.failed = true;
    } finally {
      if (token === this.#token) this.loading = 'idle';
    }
  }

  async loadOlder(): Promise<boolean> {
    const first = this.items[0];
    if (this.loading !== 'idle' || !this.hasBefore || !first) return false;
    const token = this.#token;
    this.loading = 'older';
    try {
      const page = await this.#fetch(this.channelId, { before: first.id, limit: PAGE_SIZE });
      if (token !== this.#token) return false;
      const merged = capWindow(
        {
          items: mergeItems(this.items, page.items),
          hasBefore: page.has_more_before,
          hasAfter: this.hasAfter,
        },
        'older',
        this.#cap,
      );
      this.hasBefore = merged.hasBefore;
      this.hasAfter = merged.hasAfter;
      this.#store(merged.items);
      return true;
    } catch {
      return false;
    } finally {
      if (token === this.#token) this.loading = 'idle';
    }
  }

  async loadNewer(): Promise<boolean> {
    const list = this.items;
    const last = list[list.length - 1];
    if (this.loading !== 'idle' || !this.hasAfter || !last) return false;
    const token = this.#token;
    this.loading = 'newer';
    try {
      const page = await this.#fetch(this.channelId, { after: last.id, limit: PAGE_SIZE });
      if (token !== this.#token) return false;
      const merged = capWindow(
        {
          items: mergeItems(list, page.items),
          hasBefore: this.hasBefore,
          hasAfter: page.has_more_after,
        },
        'newer',
        this.#cap,
      );
      this.hasBefore = merged.hasBefore;
      this.hasAfter = merged.hasAfter;
      this.#store(merged.items);
      return true;
    } catch {
      return false;
    } finally {
      if (token === this.#token) this.loading = 'idle';
    }
  }

  /** Live arrivals while at the bottom: drop the oldest nodes beyond the cap. */
  enforceCap(): void {
    if (this.hasAfter) return;
    const all = messages.list(this.channelId);
    if (all.length <= this.#cap) return;
    this.hasBefore = true;
    this.#store(all.slice(all.length - this.#cap));
  }

  reset(): void {
    this.#token += 1;
    this.channelId = '';
    this.hasBefore = false;
    this.hasAfter = false;
    this.loading = 'idle';
    this.failed = false;
    this.#maxId = '';
  }
}
