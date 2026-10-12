// Upload queue: one POST /api/v1/files per file via XHR (for progress and
// abort), a few at a time. Uploaded files are attached to the next message.
import { SvelteMap } from 'svelte/reactivity';
import type { Schemas } from '../api/client';

export type UploadedFile = Schemas['File'];
export type UploadStatus = 'queued' | 'uploading' | 'done' | 'failed' | 'rejected';

export interface UploadItem {
  id: string;
  name: string;
  size: number;
  status: UploadStatus;
  /** 0..1 */
  progress: number;
  /** Message key for failed / rejected items. */
  error: 'tooLarge' | 'network' | 'server' | 'storage' | '';
  file: UploadedFile | null;
}

export const MAX_CONCURRENT_UPLOADS = 2;

export type XhrFactory = () => XMLHttpRequest;

let counter = 0;

export class UploadQueue {
  items = $state<UploadItem[]>([]);
  #blobs = new SvelteMap<string, File>();
  #xhrs = new SvelteMap<string, XMLHttpRequest>();
  #maxBytes: () => number;
  #makeXhr: XhrFactory;

  constructor(maxBytes: () => number, makeXhr: XhrFactory = () => new XMLHttpRequest()) {
    this.#maxBytes = maxBytes;
    this.#makeXhr = makeXhr;
  }

  /** Files that finished uploading, in queue order. */
  get done(): UploadedFile[] {
    return this.items.flatMap((i) => (i.status === 'done' && i.file ? [i.file] : []));
  }

  /** True while anything is queued or uploading. */
  get busy(): boolean {
    return this.items.some((i) => i.status === 'queued' || i.status === 'uploading');
  }

  add(files: Iterable<File>): void {
    const max = this.#maxBytes();
    for (const f of files) {
      counter += 1;
      const id = `up${counter}`;
      const tooLarge = max > 0 && f.size > max;
      this.items.push({
        id,
        name: f.name,
        size: f.size,
        status: tooLarge ? 'rejected' : 'queued',
        progress: 0,
        error: tooLarge ? 'tooLarge' : '',
        file: null,
      });
      if (!tooLarge) this.#blobs.set(id, f);
    }
    this.#pump();
  }

  #item(id: string): UploadItem | undefined {
    return this.items.find((i) => i.id === id);
  }

  #pump(): void {
    let active = this.items.filter((i) => i.status === 'uploading').length;
    for (const it of this.items) {
      if (active >= MAX_CONCURRENT_UPLOADS) return;
      if (it.status === 'queued') {
        active += 1;
        this.#start(it.id);
      }
    }
  }

  #start(id: string): void {
    const item = this.#item(id);
    const blob = this.#blobs.get(id);
    if (!item || !blob) return;
    item.status = 'uploading';
    item.progress = 0;
    item.error = '';
    const xhr = this.#makeXhr();
    this.#xhrs.set(id, xhr);
    xhr.open('POST', '/api/v1/files');
    xhr.withCredentials = true;
    xhr.responseType = 'text';
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable && e.total > 0) item.progress = e.loaded / e.total;
    };
    const fail = (error: UploadItem['error']) => {
      this.#xhrs.delete(id);
      item.status = 'failed';
      item.error = error;
      this.#pump();
    };
    xhr.onerror = () => fail('network');
    xhr.ontimeout = () => fail('network');
    xhr.onload = () => {
      this.#xhrs.delete(id);
      if (xhr.status === 201 || xhr.status === 200) {
        try {
          item.file = JSON.parse(xhr.responseText) as UploadedFile;
          item.status = 'done';
          item.progress = 1;
          this.#blobs.delete(id);
          this.#pump();
          return;
        } catch {
          // fall through to the failure path
        }
      }
      if (xhr.status === 413) {
        item.status = 'rejected';
        item.error = 'tooLarge';
        this.#pump();
      } else {
        fail(xhr.status === 507 ? 'storage' : 'server');
      }
    };
    const form = new FormData();
    form.append('file', blob, item.name);
    xhr.send(form);
  }

  /** Aborts an in-flight upload (or drops a queued one) and removes it. */
  cancel(id: string): void {
    const xhr = this.#xhrs.get(id);
    this.#xhrs.delete(id);
    this.#blobs.delete(id);
    const i = this.items.findIndex((x) => x.id === id);
    if (i !== -1) this.items.splice(i, 1);
    xhr?.abort();
    this.#pump();
  }

  retry(id: string): void {
    const item = this.#item(id);
    if (!item || item.status !== 'failed' || !this.#blobs.has(id)) return;
    item.status = 'queued';
    item.error = '';
    this.#pump();
  }

  /** Removes finished and rejected entries after the message is sent. */
  clearFinished(): void {
    this.items = this.items.filter((i) => i.status !== 'done' && i.status !== 'rejected');
  }

  /** Aborts everything and empties the queue. */
  reset(): void {
    for (const x of this.#xhrs.values()) x.abort();
    this.#xhrs.clear();
    this.#blobs.clear();
    this.items = [];
  }
}
