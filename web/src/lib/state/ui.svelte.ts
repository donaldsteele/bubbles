// Small cross-component UI state: which message is being edited and where focus
// returns afterwards.
class UiState {
  editingId = $state<string | null>(null);
  /** A message to scroll to once its channel is loaded (search, pins). */
  jump = $state<{ channelId: string; messageId: string } | null>(null);
  /** Right-hand panel next to the channel. */
  panel = $state<'pins' | 'members' | null>(null);
  searchOpen = $state(false);
  switcherOpen = $state(false);
  #returnTo: HTMLElement | null = null;
  /** Element that opened the thread drawer; Esc returns focus there. */
  drawerTrigger: HTMLElement | null = null;

  startEdit(id: string): void {
    const a = document.activeElement;
    this.#returnTo = a instanceof HTMLElement ? a : null;
    this.editingId = id;
  }

  endEdit(): void {
    this.editingId = null;
    const el = this.#returnTo;
    this.#returnTo = null;
    if (el?.isConnected) el.focus();
  }
}

export const ui = new UiState();
