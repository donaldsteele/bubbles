import { beforeEach, describe, expect, it, vi } from 'vitest';
import { takeAround } from '../../src/lib/jump';
import { matchRoute, parseAround } from '../../src/lib/router';
import { ui } from '../../src/lib/state/ui.svelte';

const ID = '01KJMMA2G06YPRB7YH2WSS5TQZ';

describe('parseAround', () => {
  it('reads a message id', () => {
    expect(parseAround(`?around=${ID}`)).toBe(ID);
    expect(parseAround(`?x=1&around=${ID}`)).toBe(ID);
  });
  it.each(['', '?around=', '?around=abc', `?around=${ID}x`, '?around=../../etc', '?before=' + ID])(
    'ignores %j',
    (search) => {
      expect(parseAround(search)).toBeNull();
    },
  );
});

describe('takeAround', () => {
  beforeEach(() => {
    ui.jump = null;
  });

  function loc(search: string, hash = ''): Location {
    return { search, hash } as Location;
  }

  it('asks the message list to jump and cleans the address bar', () => {
    const replaceState = vi.fn();
    const route = matchRoute('/c/CHAN1');
    expect(
      takeAround(route, loc(`?around=${ID}`, '#x'), { replaceState } as unknown as History),
    ).toBe(true);
    expect(ui.jump).toEqual({ channelId: 'CHAN1', messageId: ID });
    expect(replaceState).toHaveBeenCalledWith(null, '', '/c/CHAN1#x');
  });

  it('does nothing on other routes or malformed ids', () => {
    const replaceState = vi.fn();
    const hist = { replaceState } as unknown as History;
    expect(takeAround(matchRoute('/alerts'), loc(`?around=${ID}`), hist)).toBe(false);
    expect(takeAround(matchRoute('/c/CHAN1'), loc('?around=nope'), hist)).toBe(false);
    expect(takeAround(matchRoute('/c/CHAN1'), loc(''), hist)).toBe(false);
    expect(ui.jump).toBeNull();
    expect(replaceState).not.toHaveBeenCalled();
  });
});
