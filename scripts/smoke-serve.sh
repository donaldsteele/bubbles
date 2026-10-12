#!/usr/bin/env bash
# Smoke test for `bubbles serve`: builds the binary, starts it on free
# loopback ports with a temporary data directory, fetches the CA over the
# onboarding listener, calls /healthz over HTTPS pinned to that CA, checks the
# HTTP -> HTTPS 308 redirect, then stops the server and cleans up.
# --backup-roundtrip also backs up, restores and serves the copy;
# --reset-roundtrip posts a canary message and photo, factory resets the
# server over the API and checks that the setup wizard is back and that
# neither canary survives in any file under the data directory.
# Works on Linux, macOS and Git Bash on Windows.
set -euo pipefail

mode="${1:-}"
case "$mode" in
  ""|--backup-roundtrip|--reset-roundtrip) ;;
  *) echo "usage: smoke-serve.sh [--backup-roundtrip|--reset-roundtrip]" >&2; exit 2 ;;
esac

cd "$(dirname "$0")/.."

tmp="$(mktemp -d)"
pid=""

cleanup() {
  if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  fi
  # Windows can keep the executable locked for a moment after exit.
  for _ in 1 2 3 4 5; do
    rm -rf "$tmp" 2>/dev/null && break
    sleep 1
  done
}
trap cleanup EXIT

fail() {
  echo "smoke-serve: FAIL: $*" >&2
  if [ -f "$tmp/stderr.log" ]; then
    sed 's/^/  | /' "$tmp/stderr.log" >&2
  fi
  exit 1
}

exe="$tmp/bubbles"
if [ "$(go env GOOS)" = "windows" ]; then
  exe="$tmp/bubbles.exe"
fi
CGO_ENABLED=0 go build -o "$exe" ./cmd/bubbles

# start_server DATA_DIR LOG_PREFIX: starts a server on OS-picked loopback ports
# and sets pid, https_port and http_port from its startup banner.
start_server() {
  local data="$1" log="$2"
  : >"$tmp/$log.err"
  "$exe" serve --data-dir "$data" --https-addr 127.0.0.1:0 --http-addr 127.0.0.1:0 \
    --no-mdns --allow-public >"$tmp/$log.out" 2>"$tmp/$log.err" &
  pid=$!
  cp "$tmp/$log.err" "$tmp/stderr.log" 2>/dev/null || true
  https_port=""
  http_port=""
  for _ in $(seq 1 150); do
    kill -0 "$pid" 2>/dev/null || { cp "$tmp/$log.err" "$tmp/stderr.log"; fail "server exited during startup"; }
    https_port="$(sed -n 's#.*https://127\.0\.0\.1:\([0-9][0-9]*\)/.*#\1#p' "$tmp/$log.err" | head -n 1)"
    http_port="$(sed -n 's#.*http://127\.0\.0\.1:\([0-9][0-9]*\)/.*#\1#p' "$tmp/$log.err" | head -n 1)"
    if [ -n "$https_port" ] && [ -n "$http_port" ]; then
      break
    fi
    sleep 0.2
  done
  cp "$tmp/$log.err" "$tmp/stderr.log"
  if [ -z "$https_port" ] || [ -z "$http_port" ]; then
    fail "no startup banner within 30s"
  fi
}

stop_server() {
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  pid=""
}

# Port 0 lets the OS pick free ports; the startup banner reports them.
# --allow-public skips the private-address bind policy check, which a loopback-only test run would otherwise trip.
start_server "$tmp/data" server1

curl -fsS --max-time 10 -o "$tmp/ca.pem" "http://127.0.0.1:$http_port/ca.pem" || fail "GET /ca.pem over HTTP"
grep -q "BEGIN CERTIFICATE" "$tmp/ca.pem" || fail "/ca.pem is not a PEM certificate"

# curl on Windows uses Schannel, which checks revocation by default; a local
# CA publishes no CRL, so turn that check off there.
curl_tls=(--cacert "$tmp/ca.pem")
if curl -V | grep -qi schannel; then
  curl_tls+=(--ssl-no-revoke)
fi
body="$(curl -fsS --max-time 10 "${curl_tls[@]}" "https://127.0.0.1:$https_port/healthz")" ||
  fail "GET /healthz over HTTPS with --cacert"
case "$body" in
  *'"status":"ok"'*) ;;
  *) fail "unexpected /healthz body: $body" ;;
esac

got="$(curl -sS --max-time 10 -o /dev/null -w '%{http_code} %{redirect_url}' "http://127.0.0.1:$http_port/api/v1/me")" ||
  fail "GET /api/v1/me over HTTP"
want="308 https://127.0.0.1:$https_port/api/v1/me"
[ "$got" = "$want" ] || fail "HTTP redirect: got '$got', want '$want'"

if [ "$mode" = "--backup-roundtrip" ]; then
  api="https://127.0.0.1:$https_port/api/v1"
  origin="https://127.0.0.1:$https_port"
  jar="$tmp/cookies.txt"
  jhdr=(-H "Content-Type: application/json" -H "Origin: $origin")
  token="$(tr -d '\r\n' <"$tmp/data/setup-token.txt")"
  [ -n "$token" ] || fail "no setup token"
  pw="correct-horse-battery-staple-42"
  marker="roundtrip-marker-$$"

  curl -fsS --max-time 10 "${curl_tls[@]}" "${jhdr[@]}" -c "$jar" -o /dev/null \
    -d "{\"setup_token\":\"$token\",\"username\":\"admin\",\"password\":\"$pw\",\"instance_name\":\"Smoke\"}" \
    "$api/setup" || fail "POST /setup"
  chan_json="$(curl -fsS --max-time 10 "${curl_tls[@]}" "${jhdr[@]}" -b "$jar" \
    -d '{"name":"backup-test","kind":"public"}' "$api/channels")" || fail "POST /channels"
  chan="$(printf '%s' "$chan_json" | grep -o '"id":"[^"]*"' | head -n 1 | cut -d'"' -f4)"
  [ -n "$chan" ] || fail "no channel id in: $chan_json"
  curl -fsS --max-time 10 "${curl_tls[@]}" "${jhdr[@]}" -b "$jar" -o /dev/null \
    -d "{\"body\":\"$marker\"}" "$api/channels/$chan/messages" || fail "POST message"

  printf '%s\n' "backup passphrase for the smoke test" >"$tmp/pass.txt"
  printf '%s\n' "not the right passphrase" >"$tmp/pass-bad.txt"
  # Online backup: the server is still running and holds the data-dir lock.
  "$exe" backup --data-dir "$tmp/data" --out "$tmp/smoke.bbk" --passphrase-file "$tmp/pass.txt" ||
    fail "bubbles backup while the server runs"
  [ -s "$tmp/smoke.bbk" ] || fail "backup file is empty"
  if head -c 2 "$tmp/smoke.bbk" | grep -q "^$(printf '\037')"; then
    fail "backup is not encrypted"
  fi
  if "$exe" backup --data-dir "$tmp/data" --out "$tmp/nopass.bbk" </dev/null 2>/dev/null; then
    fail "backup without passphrase succeeded on a non-TTY"
  fi

  set +e
  "$exe" restore "$tmp/smoke.bbk" --data-dir "$tmp/data" --passphrase-file "$tmp/pass.txt" --force 2>/dev/null
  rc=$?
  set -e
  [ "$rc" = "3" ] || fail "restore into a running server's dir: exit $rc, want 3"

  stop_server

  if "$exe" restore "$tmp/smoke.bbk" --data-dir "$tmp/restored" --passphrase-file "$tmp/pass-bad.txt" 2>/dev/null; then
    fail "restore with the wrong passphrase succeeded"
  fi
  [ ! -e "$tmp/restored" ] || fail "wrong passphrase left a data directory behind"

  "$exe" restore "$tmp/smoke.bbk" --data-dir "$tmp/restored" --passphrase-file "$tmp/pass.txt" ||
    fail "bubbles restore"
  cmp -s "$tmp/data/tls/ca.crt" "$tmp/restored/tls/ca.crt" || fail "restored CA certificate differs"

  cp "$tmp/ca.pem" "$tmp/ca-original.pem"
  start_server "$tmp/restored" server2
  curl -fsS --max-time 10 -o "$tmp/ca-restored.pem" "http://127.0.0.1:$http_port/ca.pem" || fail "GET /ca.pem (restored)"
  cmp -s "$tmp/ca-original.pem" "$tmp/ca-restored.pem" || fail "CA differs after restore"
  fp_orig="$(openssl x509 -in "$tmp/ca-original.pem" -noout -fingerprint -sha256 2>/dev/null || sha256sum <"$tmp/ca-original.pem")"
  fp_rest="$(openssl x509 -in "$tmp/ca-restored.pem" -noout -fingerprint -sha256 2>/dev/null || sha256sum <"$tmp/ca-restored.pem")"
  [ "$fp_orig" = "$fp_rest" ] || fail "CA fingerprint differs after restore"

  api="https://127.0.0.1:$https_port/api/v1"
  origin="https://127.0.0.1:$https_port"
  jhdr=(-H "Content-Type: application/json" -H "Origin: $origin")
  rm -f "$jar"
  curl -fsS --max-time 10 "${curl_tls[@]}" "${jhdr[@]}" -c "$jar" -o /dev/null \
    -d "{\"username\":\"admin\",\"password\":\"$pw\"}" "$api/auth/login" || fail "login on the restored server"
  msgs="$(curl -fsS --max-time 10 "${curl_tls[@]}" -b "$jar" "$api/channels/$chan/messages")" ||
    fail "GET messages on the restored server"
  case "$msgs" in
    *"$marker"*) ;;
    *) fail "message missing after restore" ;;
  esac
  "$exe" healthcheck --data-dir "$tmp/restored" --url "https://127.0.0.1:$https_port" >/dev/null ||
    fail "bubbles healthcheck against the restored server"
  echo "smoke-serve: backup round trip OK"
fi

if [ "$mode" = "--reset-roundtrip" ]; then
  api="https://127.0.0.1:$https_port/api/v1"
  origin="https://127.0.0.1:$https_port"
  jar="$tmp/cookies.txt"
  jhdr=(-H "Content-Type: application/json" -H "Origin: $origin")
  data="$tmp/data"
  old_token="$(tr -d '\r\n' <"$data/setup-token.txt")"
  [ -n "$old_token" ] || fail "no setup token"
  pw="correct-horse-battery-staple-42"
  canary="reset-canary-$-$RANDOM"
  # A 32x1 grayscale PNG stored without compression: its pixel bytes spell
  # photo_canary, and metadata stripping keeps the IDAT chunk verbatim.
  photo_canary="bubbles-photo-canary-5d1e9a7c3b2"
  png_b64="iVBORw0KGgoAAAANSUhEUgAAACAAAAABCAAAAACa4fO7AAAALElEQVR4AQEhAN7/AGJ1YmJsZXMtcGhvdG8tY2FuYXJ5LTVkMWU5YTdjM2IyxhsLOdpScj0AAAAASUVORK5CYII="
  printf '%s' "$png_b64" | base64 --decode >"$tmp/photo.png" || fail "decode the test photo"

  curl -fsS --max-time 10 "${curl_tls[@]}" "${jhdr[@]}" -c "$jar" -o /dev/null \
    -d "{\"setup_token\":\"$old_token\",\"username\":\"admin\",\"password\":\"$pw\",\"instance_name\":\"Smoke\"}" \
    "$api/setup" || fail "POST /setup"
  chan_json="$(curl -fsS --max-time 10 "${curl_tls[@]}" "${jhdr[@]}" -b "$jar" \
    -d '{"name":"reset-test","kind":"public"}' "$api/channels")" || fail "POST /channels"
  chan="$(printf '%s' "$chan_json" | grep -o '"id":"[^"]*"' | head -n 1 | cut -d'"' -f4)"
  [ -n "$chan" ] || fail "no channel id in: $chan_json"
  # A relative path: a native Windows curl cannot read MSYS paths inside -F.
  file_json="$(cd "$tmp" && curl -fsS --max-time 10 "${curl_tls[@]}" -H "Origin: $origin" -b "$jar" \
    -F "file=@photo.png;type=image/png" "$api/files")" || fail "POST /files"
  file_id="$(printf '%s' "$file_json" | grep -o '"id":"[^"]*"' | head -n 1 | cut -d'"' -f4)"
  [ -n "$file_id" ] || fail "no file id in: $file_json"
  curl -fsS --max-time 10 "${curl_tls[@]}" "${jhdr[@]}" -b "$jar" -o /dev/null \
    -d "{\"body\":\"$canary\",\"file_ids\":[\"$file_id\"]}" "$api/channels/$chan/messages" || fail "POST message"
  # Both canaries are on disk before the reset, so the scan below means something.
  grep -rqaF "$photo_canary" "$data/files" || fail "the photo bytes are not in data/files"

  reset_json="$(curl -fsS --max-time 30 "${curl_tls[@]}" "${jhdr[@]}" -b "$jar" \
    -d "{\"mode\":\"factory\",\"confirm\":\"Smoke\",\"password\":\"$pw\"}" "$api/admin/reset")" ||
    fail "POST /admin/reset"
  new_token="$(printf '%s' "$reset_json" | grep -o '"setup_token":"[^"]*"' | cut -d'"' -f4)"
  [ -n "$new_token" ] || fail "no setup token in: $reset_json"
  [ "$new_token" != "$old_token" ] || fail "the setup token did not change"
  [ "$new_token" = "$(tr -d '\r\n' <"$data/setup-token.txt")" ] || fail "setup-token.txt does not hold the new token"
  case "$reset_json" in
    *'"ca_rotated":false'*) ;;
    *) fail "unexpected reset answer: $reset_json" ;;
  esac

  status="$(curl -fsS --max-time 10 "${curl_tls[@]}" "$api/setup/status")" || fail "GET /setup/status"
  case "$status" in
    *'"needs_setup":true'*) ;;
    *) fail "setup wizard not back after the reset: $status" ;;
  esac
  got="$(curl -sS --max-time 10 "${curl_tls[@]}" -b "$jar" -o /dev/null -w '%{http_code}' "$api/me")"
  [ "$got" = "401" ] || fail "old session after the reset: $got, want 401"

  # The files were moved aside and are deleted in the background.
  for _ in $(seq 1 50); do
    ls -d "$data"/trash-* >/dev/null 2>&1 || break
    sleep 0.2
  done
  ! ls -d "$data"/trash-* >/dev/null 2>&1 || fail "trash not deleted within 10s"
  for needle in "$canary" "$photo_canary"; do
    if hits="$(grep -rlasF "$needle" "$data")"; then
      fail "'$needle' survives the reset in: $hits"
    fi
  done
  echo "smoke-serve: reset round trip OK"
fi

stop_server
echo "smoke-serve: OK (https 127.0.0.1:$https_port, http 127.0.0.1:$http_port)"
