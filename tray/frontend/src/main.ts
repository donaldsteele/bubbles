import "./style.css";
import { AuthService } from "../bindings/github.com/donaldsteele/bubbles/tray";
import type { ServerInfo, ViewState } from "../bindings/github.com/donaldsteele/bubbles/tray";

// The sign-in window of the bubbles tray app. It is plain DOM code: every
// piece of text from the Go side is set with textContent, never as HTML.

const root = document.getElementById("app") as HTMLElement;

type Child = Node | string | null | false;

function el<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  props: Partial<Record<string, string | boolean>> = {},
  ...children: Child[]
): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (v === false || v === undefined) continue;
    if (k === "class") node.className = String(v);
    else if (v === true) node.setAttribute(k, "");
    else node.setAttribute(k, String(v));
  }
  for (const c of children) {
    if (c === null || c === false) continue;
    node.append(typeof c === "string" ? document.createTextNode(c) : c);
  }
  return node;
}

function notice(text: string, kind = "warn"): HTMLElement | null {
  return text ? el("div", { class: `notice ${kind}`, role: "status" }, text) : null;
}

function render(...nodes: Child[]): void {
  root.replaceChildren(...(nodes.filter((n) => n !== null && n !== false) as Node[]));
  const first = root.querySelector<HTMLElement>("input, button.primary");
  first?.focus();
}

async function busy<T>(button: HTMLButtonElement, label: string, work: () => Promise<T>): Promise<T> {
  const original = button.textContent;
  button.disabled = true;
  button.textContent = label;
  try {
    return await work();
  } finally {
    button.disabled = false;
    button.textContent = original;
  }
}

function show(state: ViewState): void {
  switch (state.screen) {
    case "confirm":
      return showConfirm(state);
    case "account":
      return showAccount(state);
    case "done":
      return showDone(state);
    default:
      return showServer(state);
  }
}

function joinLabel(s: ServerInfo): string {
  let address = s.url;
  try {
    address = new URL(s.url).host || s.url;
  } catch {
    // keep the text as given
  }
  return s.name ? `Join ${s.name} · ${address}` : `Join ${address}`;
}

function showServer(state: ViewState): void {
  const list = el("ul", { class: "servers", "aria-label": "Servers found on this network" });
  const url = el("input", {
    type: "text", id: "server", autocomplete: "off", spellcheck: "false", placeholder: "192.168.1.20 or bubbles.local",
    value: state.serverUrl,
  });
  const port = el("input", {
    type: "text", id: "port", inputmode: "numeric", autocomplete: "off", placeholder: "28226", value: state.port,
  });
  const found = el("p", { class: "muted" }, "Looking for servers on this network...");
  const cont = el("button", { class: "primary", type: "button" }, "Continue");
  const search = el("button", { type: "button" }, "Search again");

  let first = true;
  const scan = async () => {
    found.textContent = "Looking for servers on this network...";
    list.replaceChildren();
    let servers: ServerInfo[] = [];
    try {
      servers = (await AuthService.Discover()) ?? [];
    } catch {
      servers = [];
    }
    const initial = first;
    first = false;
    found.textContent = servers.length ? "Servers found:" : "No server found. Type the address below.";
    for (const s of servers) {
      const b = el("button", { type: "button", class: "join" }, joinLabel(s));
      b.addEventListener("click", () => {
        url.value = s.url;
        port.value = s.onboardPort;
        void submit(s.name);
      });
      list.append(el("li", {}, b));
    }
    if (servers.length === 1 && initial) list.querySelector<HTMLElement>("button.join")?.focus();
  };

  const submit = async (name = "") => {
    const next = await busy(cont, "Contacting server...", () =>
      name
        ? AuthService.PrepareNamed(url.value.trim(), port.value.trim(), name)
        : AuthService.Prepare(url.value.trim(), port.value.trim()),
    );
    show(next);
  };

  cont.addEventListener("click", () => void submit());
  url.addEventListener("keydown", (e) => {
    if (e.key === "Enter") void submit();
  });
  search.addEventListener("click", () => void scan());

  render(
    el("h1", {}, "Connect to bubbles"),
    el("p", { class: "muted" }, "Pick your server, or type its address."),
    notice(state.notice),
    found,
    list,
    el("div", { class: "row" }, search),
    el("label", { for: "server" }, "Server address"),
    url,
    el("label", { for: "port" }, "Onboarding port (optional)"),
    port,
    el("div", { class: "row" }, cont),
  );
  void scan();
}

function showConfirm(state: ViewState): void {
  const prompt = state.prompt;
  if (!prompt) return showServer(state);
  const sure = el("input", { type: "checkbox", id: "sure" });
  const trust = el("input", { type: "checkbox", id: "trust", checked: state.canInstall });
  const ok = el("button", { class: "primary", type: "button", disabled: true }, "Trust and continue");
  const back = el("button", { type: "button" }, "Cancel");

  sure.addEventListener("change", () => {
    ok.disabled = !sure.checked;
  });
  back.addEventListener("click", () => void AuthService.Back().then(show));
  ok.addEventListener("click", async () => {
    const next = await busy(ok, "Working...", () => AuthService.Confirm(prompt.fingerprint, trust.checked));
    show(next);
  });

  const changed = state.reconfirm || prompt.changed;
  const heading = changed
    ? "The server identity changed"
    : state.instance
      ? `Join ${state.instance}?`
      : "Check the server fingerprint";
  render(
    el("h1", {}, heading),
    notice(state.notice),
    changed &&
      notice(
        "This server presents a different certificate authority than the one you trusted before. " +
          "This is expected only if an administrator reset or rebuilt the server. Do not continue otherwise.",
        "bad",
      ),
    el("p", { class: "muted" }, `Server: ${state.address}`),
    el("p", {}, "Compare this fingerprint with the one shown by the bubbles server (Admin, TLS) or by its administrator:"),
    el("div", { class: "fingerprint", id: "fingerprint" }, prompt.grouped),
    prompt.changed && prompt.previous
      ? el("p", { class: "muted" }, "Previously trusted:")
      : null,
    prompt.changed && prompt.previous ? el("div", { class: "fingerprint old" }, prompt.previous) : null,
    el("label", { class: "inline", for: "sure" }, sure, "I compared the fingerprints and they are identical."),
    el(
      "label",
      { class: "inline", for: "trust" },
      trust,
      state.canInstall
        ? "Trust this server's CA for my account (needed so the bubbles window opens without a certificate warning)."
        : "Show me how to trust this server's CA for my account (manual steps on this system).",
    ),
    el("div", { class: "row" }, ok, back),
  );
}

function showAccount(state: ViewState): void {
  const instance = state.instance;
  const tabs: { id: string; label: string }[] = [
    { id: "create", label: "Create account" },
    { id: "signin", label: "Sign in" },
  ];
  let current = state.defaultTab === "create" ? "create" : "signin";

  const tablist = el("div", { role: "tablist", "aria-label": "Account" });
  const buttons = new Map<string, HTMLButtonElement>();
  const panels = new Map<string, HTMLElement>();
  for (const t of tabs) {
    const b = el("button", {
      type: "button", role: "tab", id: `tab-${t.id}`, "aria-controls": `panel-${t.id}`, class: "tab",
    }, t.label);
    b.addEventListener("click", () => select(t.id, false));
    b.addEventListener("keydown", (e) => {
      if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
      e.preventDefault();
      const at = tabs.findIndex((x) => x.id === current);
      const next = tabs[(at + (e.key === "ArrowRight" ? 1 : tabs.length - 1)) % tabs.length];
      select(next.id, true);
    });
    buttons.set(t.id, b);
    tablist.append(b);
  }

  const select = (id: string, focus: boolean) => {
    current = id;
    for (const t of tabs) {
      const b = buttons.get(t.id) as HTMLButtonElement;
      const on = t.id === id;
      b.setAttribute("aria-selected", String(on));
      b.tabIndex = on ? 0 : -1;
      (panels.get(t.id) as HTMLElement).hidden = !on;
    }
    if (focus) buttons.get(id)?.focus();
  };

  const makeBack = () => {
    const b = el("button", { type: "button" }, "Change server");
    b.addEventListener("click", () => void AuthService.Back().then(show));
    return b;
  };

  // Create account panel.
  const createPanel = el("div", { role: "tabpanel", id: "panel-create", "aria-labelledby": "tab-create" });
  let regName: HTMLInputElement | null = null;
  if (state.registrationOpen) {
    const name = el("input", { type: "text", id: "reg-name", autocomplete: "name", spellcheck: "false" });
    const pass = el("input", { type: "password", id: "reg-pass", autocomplete: "new-password", "aria-describedby": "reg-pass-hint" });
    const device = el("input", { type: "text", id: "reg-device", autocomplete: "off", value: state.deviceName });
    const go = el("button", { class: "primary", type: "button" }, "Create account");
    const submit = async () => {
      const next = await busy(go, "Creating account...", () => AuthService.Register(name.value.trim(), pass.value, device.value));
      pass.value = "";
      show(next);
    };
    go.addEventListener("click", () => void submit());
    pass.addEventListener("keydown", (e) => {
      if (e.key === "Enter") void submit();
    });
    regName = name;
    createPanel.append(
      el("label", { for: "reg-name" }, "Your name"),
      name,
      el("label", { for: "reg-pass" }, "Password"),
      pass,
      el("p", { class: "muted", id: "reg-pass-hint" }, "Any password works."),
      el("label", { for: "reg-device" }, "Name of this computer"),
      device,
      el("div", { class: "row" }, go, makeBack()),
    );
  } else {
    createPanel.append(
      el("p", {}, "Sign-up is closed. To join, ask the admin for an invite link, or sign in if you already have an account."),
    );
  }

  // Sign in panel.
  const signinPanel = el("div", { role: "tabpanel", id: "panel-signin", "aria-labelledby": "tab-signin" });
  const user = el("input", { type: "text", id: "user", autocomplete: "username", spellcheck: "false" });
  const pass = el("input", { type: "password", id: "pass", autocomplete: "current-password" });
  const device = el("input", { type: "text", id: "device", autocomplete: "off", value: state.deviceName });
  const go = el("button", { class: "primary", type: "button" }, "Sign in");
  const submit = async () => {
    const next = await busy(go, "Signing in...", () => AuthService.Login(user.value.trim(), pass.value, device.value));
    pass.value = "";
    show(next);
  };
  go.addEventListener("click", () => void submit());
  pass.addEventListener("keydown", (e) => {
    if (e.key === "Enter") void submit();
  });
  signinPanel.append(
    el("label", { for: "user" }, "Your name or username"),
    user,
    el("label", { for: "pass" }, "Password"),
    pass,
    el("label", { for: "device" }, "Name of this computer"),
    device,
    el("div", { class: "row" }, go, makeBack()),
  );

  panels.set("create", createPanel);
  panels.set("signin", signinPanel);

  // The closed-sign-up text has no form, so its panel gets its own way back.
  if (!state.registrationOpen) createPanel.append(el("div", { class: "row" }, makeBack()));

  const title = el("h1", {});
  const retitle = () => {
    title.textContent =
      current === "create" ? (instance ? `Join ${instance}` : "Join this server") : instance ? `Sign in to ${instance}` : "Sign in";
  };
  for (const t of tabs) buttons.get(t.id)?.addEventListener("click", retitle);
  for (const t of tabs) buttons.get(t.id)?.addEventListener("keydown", retitle);

  render(title, el("p", { class: "muted" }, state.address), notice(state.notice, state.notice.toLowerCase().includes("could not") ? "warn" : "ok"), tablist, createPanel, signinPanel);
  select(current, false);
  retitle();
  (current === "create" ? regName : user)?.focus();
  if (current === "create" && !regName) buttons.get("create")?.focus();
}

function showDone(state: ViewState): void {
  render(el("h1", {}, state.notice || "Done"), el("p", { class: "muted" }, "You can close this window."));
}

void AuthService.State().then(show);
