#!/usr/bin/env bash
# Run a batch of local games and tally the result.
#
# One Jev-backed snake on port 8080 plays one or three deterministic snakes on
# 8081-8083. The Jev snake's configuration comes from JEV_ENV, so two runs can
# be compared by changing only that.
#
# Usage: scripts/tournament.sh [-n games] [-m duel|1v3] [-s seed_base] [-t timeout_ms] [-l label]
set -euo pipefail

GAMES=20
MODE=duel
SEED_BASE=9000
TIMEOUT=2000
LABEL=run
JEV_ENV="${JEV_ENV:-}"

while getopts "n:m:s:t:l:" opt; do
  case "$opt" in
    n) GAMES=$OPTARG ;;
    m) MODE=$OPTARG ;;
    s) SEED_BASE=$OPTARG ;;
    t) TIMEOUT=$OPTARG ;;
    l) LABEL=$OPTARG ;;
    *) echo "usage: $0 [-n games] [-m duel|1v3] [-s seed_base] [-t timeout_ms] [-l label]" >&2; exit 2 ;;
  esac
done

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="${TOURNAMENT_DIR:-${TMPDIR:-/tmp}/battlesnake-tournament}/$LABEL"
BIN="$WORK/bsnake"
CLI="$(go env GOPATH)/bin/battlesnake"

[ -x "$CLI" ] || { echo "battlesnake CLI not installed - run 'make tools'" >&2; exit 1; }
[ -n "${TYPESAFE_API_KEY:-}" ] || { echo "TYPESAFE_API_KEY is not set" >&2; exit 1; }

mkdir -p "$WORK"
go build -o "$BIN" "$ROOT/cmd/battlesnake"

ports=(8080 8081 8082 8083)
stop_servers() {
  for p in "${ports[@]}"; do
    pid=$(ss -lptn "sport = :$p" 2>/dev/null | grep -o 'pid=[0-9]*' | head -1 | cut -d= -f2 || true)
    [ -n "$pid" ] && kill "$pid" 2>/dev/null || true
  done
  sleep 1
}
trap stop_servers EXIT

stop_servers
# shellcheck disable=SC2086
env $JEV_ENV LOG_LEVEL=info PORT=8080 "$BIN" > "$WORK/jev.log" 2>&1 &
for p in 8081 8082 8083; do
  env -u TYPESAFE_API_KEY LOG_LEVEL=info PORT=$p "$BIN" > "$WORK/det-$p.log" 2>&1 &
done

for _ in $(seq 1 40); do
  curl -sf -m 1 -o /dev/null "http://localhost:8080/" && break
  sleep 0.25
done
sleep 3  # let the inference connection warm before the first game

snakes=(--name jev --url http://localhost:8080 --name det-1 --url http://localhost:8081)
[ "$MODE" = "1v3" ] && snakes+=(--name det-2 --url http://localhost:8082 --name det-3 --url http://localhost:8083)

echo "tournament: $GAMES $MODE games, timeout ${TIMEOUT}ms, jev env: ${JEV_ENV:-<defaults>}"
jev_wins=0; det_wins=0; draws=0
for i in $(seq 1 "$GAMES"); do
  seed=$((SEED_BASE + i))
  log="$WORK/game-$seed.log"
  timeout 600 "$CLI" play -W 11 -H 11 -t "$TIMEOUT" "${snakes[@]}" -g standard -r "$seed" > "$log" 2>&1 || true
  result=$(grep -oE '([a-z0-9-]+ was the winner|It was a draw)' "$log" | tail -1 || true)
  turns=$(grep -o 'after [0-9]* turns' "$log" | grep -o '[0-9]*' || echo 0)
  case "$result" in
    jev*)  jev_wins=$((jev_wins + 1)) ;;
    det*)  det_wins=$((det_wins + 1)) ;;
    *)     draws=$((draws + 1)) ;;
  esac
  printf "  %3d/%d seed %-6s %4s turns  %s\n" "$i" "$GAMES" "$seed" "$turns" "${result:-no result}"
done

tokens=$(grep -o 'input_tokens=[0-9]*' "$WORK/jev.log" | cut -d= -f2 | paste -sd+ | bc 2>/dev/null || echo 0)
calls=$(grep -o 'inference_calls=[0-9]*' "$WORK/jev.log" | cut -d= -f2 | paste -sd+ | bc 2>/dev/null || echo 0)
overrides=$(grep -o 'overrides=[0-9]*' "$WORK/jev.log" | cut -d= -f2 | paste -sd+ | bc 2>/dev/null || echo 0)
misses=$(grep -o 'fallbacks=[0-9]*' "$WORK/jev.log" | cut -d= -f2 | paste -sd+ | bc 2>/dev/null || echo 0)

echo
echo "RESULT $LABEL: jev=$jev_wins det=$det_wins draw=$draws of $GAMES ($MODE)"
# slog quotes the value because it contains '=', so strip the quotes first.
grep -o 'reasons="[^"]*"' "$WORK/jev.log" | sed 's/reasons="//; s/"$//' | tr ',' '\n' \
  | awk -F= '{t[$1]+=$2} END {printf "PATHS  "; for (k in t) printf "%s=%d ", k, t[k]; print ""}' 
printf 'COST   %s: calls=%s tokens=%s cost=$%s overrides=%s deadline_misses=%s\n' \
  "$LABEL" "$calls" "${tokens:-0}" \
  "$(python3 -c "print(f'{${tokens:-0}*0.042/1e6:.4f}')")" "$overrides" "$misses"
echo "logs: $WORK"
