/**
 * Svelte action that moves an element under <body> while it is mounted.
 * Message rows use content-visibility, which contains fixed-position
 * descendants, so a dialog opened from a row would otherwise be clipped to it.
 */
export function portal(node: HTMLElement): { destroy(): void } {
  document.body.appendChild(node);
  return {
    destroy() {
      node.remove();
    },
  };
}
