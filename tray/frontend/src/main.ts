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
    case "login":
      return showLogin(state);
    case "done":
      return showDone(state);
    default:
      return showServer(state);
  }
}

function showServer(state: ViewState): void {
  const list = el("ul", { class: "servers", "aria-label": "Servers found on this network" });
  const url = el("input", {
    type: "text", id: "server", autocomplete: "off", spellcheck: "false", placeholder: "192.168.1.20 or bubbles.local",
    value: state.serverUrl,
  });
  const port = el("input", {
    type: "text", id: "port", inputmode: "numeric", autocomplete: "off", placeholder: "8080", value: state.port,
  });
  const found = el("p", { class: "muted" }, "Looking for servers on this network...");
  const cont = el("button", { class: "primary", type: "button" }, "Continue");
  const search = el("button", { type: "button" }, "Search again");

  const scan = async () => {
    found.textContent = "Looking for servers on this network...";
    list.replaceChildren();
    let servers: ServerInfo[] = [];
    try {
      servers = (await AuthService.Discover()) ?? [];
    } catch {
      servers = [];
    }
    found.textContent = servers.length ? "Servers found:" : "No server found. Type the address below.";
    for (const s of servers) {
      const b = el("button", { type: "button" }, `${s.name || s.url} (${s.url})`);
      b.addEventListener("click", () => {
        url.value = s.url;
        port.value = s.onboardPort;
        void submit();
      });
      list.append(el("li", {}, b));
    }
  };

  const submit = async () => {
    const next = await busy(cont, "Contacting server...", () => AuthService.Prepare(url.value.trim(), port.value.trim()));
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
  const ok = el("button", { class: "primary", type: "button", disabled: true }, "Trust this server");
  const back = el("button", { type: "button" }, "Cancel");

  sure.addEventListener("change", () => {
    ok.disabled = !sure.checked;
  });
  back.addEventListener("click", () => void AuthService.Back().then(show));
  ok.addEventListener("click", async () => {
    const next = await busy(ok, "Working...", () => AuthService.Confirm(prompt.fingerprint, trust.checked));
    show(next);
  });

  const heading = state.reconfirm || prompt.changed ? "The server identity changed" : "Check the server fingerprint";
  render(
    el("h1", {}, heading),
    notice(state.notice),
    (state.reconfirm || prompt.changed) &&
      notice(
        "This server presents a different certificate authority than the one you trusted before. " +
          "This is expected only if an administrator reset or rebuilt the server. Do not continue otherwise.",
        "bad",
      ),
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

function showLogin(state: ViewState): void {
  const user = el("input", { type: "text", id: "user", autocomplete: "username", spellcheck: "false" });
  const pass = el("input", { type: "password", id: "pass", autocomplete: "current-password" });
  const device = el("input", { type: "text", id: "device", autocomplete: "off", value: state.deviceName });
  const go = el("button", { class: "primary", type: "button" }, "Sign in");
  const back = el("button", { type: "button" }, "Change server");

  const submit = async () => {
    const next = await busy(go, "Signing in...", () => AuthService.Login(user.value.trim(), pass.value, device.value));
    pass.value = "";
    show(next);
  };
  go.addEventListener("click", () => void submit());
  pass.addEventListener("keydown", (e) => {
    if (e.key === "Enter") void submit();
  });
  back.addEventListener("click", () => void AuthService.Back().then(show));

  render(
    el("h1", {}, "Sign in"),
    el("p", { class: "muted" }, state.serverUrl),
    notice(state.notice, state.notice.toLowerCase().includes("could not") ? "warn" : "ok"),
    el("label", { for: "user" }, "Username"),
    user,
    el("label", { for: "pass" }, "Password"),
    pass,
    el("label", { for: "device" }, "Name of this computer"),
    device,
    el("div", { class: "row" }, go, back),
  );
}

function showDone(state: ViewState): void {
  render(el("h1", {}, state.notice || "Done"), el("p", { class: "muted" }, "You can close this window."));
}

void AuthService.State().then(show);
