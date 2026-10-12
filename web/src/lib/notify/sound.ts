// A short synthesized chime (WebAudio). No audio asset is shipped.

let ctx: AudioContext | null = null;

function context(): AudioContext | null {
  if (ctx) return ctx;
  const Ctor = typeof AudioContext === 'undefined' ? undefined : AudioContext;
  if (!Ctor) return null;
  try {
    ctx = new Ctor();
  } catch {
    return null;
  }
  return ctx;
}

/** Plays a two-note chime. Silent when audio is unavailable or blocked. */
export function playChime(): void {
  const c = context();
  if (!c) return;
  if (c.state === 'suspended') void c.resume().catch(() => {});
  const start = c.currentTime;
  const notes: [number, number][] = [
    [880, 0],
    [1174.66, 0.12],
  ];
  for (const [freq, offset] of notes) {
    const osc = c.createOscillator();
    const gain = c.createGain();
    osc.type = 'sine';
    osc.frequency.value = freq;
    const t0 = start + offset;
    gain.gain.setValueAtTime(0.0001, t0);
    gain.gain.exponentialRampToValueAtTime(0.15, t0 + 0.02);
    gain.gain.exponentialRampToValueAtTime(0.0001, t0 + 0.22);
    osc.connect(gain).connect(c.destination);
    osc.start(t0);
    osc.stop(t0 + 0.25);
  }
}
