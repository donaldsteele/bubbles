// Stand-in for the generated Wails bindings, used only to capture marketing
// screenshots of the tray sign-in window in a plain browser. The screen comes
// from the page address: ?screen=server|confirm|account
const FINGERPRINT =
  'A29F 73CA 36DA C90A 6905 A6D5 64AC E368 BF74 3EAD E54B 9A0B E449 0278 25CA 3A24';

const screen = (new URLSearchParams(location.search).get('screen') ?? 'server') as
  | 'server'
  | 'confirm'
  | 'account';

const base = {
  screen,
  notice: '',
  serverUrl: '',
  port: '',
  prompt: null as null | { fingerprint: string; grouped: string; changed: boolean },
  reconfirm: false,
  deviceName: 'Bea’s laptop',
  canInstall: true,
  instance: '',
  address: '',
  registrationOpen: true,
  defaultTab: 'create',
};

function state() {
  if (screen === 'confirm')
    return {
      ...base,
      instance: 'Basement LAN',
      address: '192.168.1.20:28225',
      prompt: { fingerprint: FINGERPRINT.replace(/ /g, ''), grouped: FINGERPRINT, changed: false },
    };
  if (screen === 'account')
    return { ...base, instance: 'Basement LAN', address: '192.168.1.20:28225' };
  return base;
}

const ok = <T>(v: T) => Promise.resolve(v);

export const AuthService = {
  State: () => ok(state()),
  Discover: () =>
    ok([
      {
        name: 'Basement LAN',
        url: 'https://192.168.1.20:28225',
        onboardingUrl: 'http://192.168.1.20:28226',
        onboardPort: '28226',
      },
    ]),
  PrepareNamed: (_url: string, _port: string, _name: string) => ok(state()),
  Prepare: (_url: string, _port: string) => ok(state()),
  Confirm: (_fp: string, _trust: boolean) => ok(state()),
  Register: (_name: string, _pass: string, _device: string) => ok(state()),
  Login: (_user: string, _pass: string, _device: string) => ok(state()),
  Back: () => ok(state()),
};
