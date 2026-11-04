#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
mode="${1:-list}"
size="${2:-25}"
case "$size" in *[!0-9]*|'') exit 2;; esac
[ "$size" -ge 1 ] && [ "$size" -le 100 ] || exit 2
case "$mode" in list|run) ;; *) echo "usage: $0 list|run [tests_per_shard] [shard_number]" >&2; exit 2;; esac
only="${3:-0}"
case "$only" in *[!0-9]*|'') exit 2;; esac
if [ "$mode" = run ] && [ "${FEED_SCALE_ISOLATED:-}" != yes ]; then
 echo "FEED_SCALE_ISOLATED=yes required to run" >&2
 exit 2
fi
tmp="$(mktemp)"
raw="$(mktemp)"
trap 'rm -f "$tmp" "$raw"' EXIT HUP INT TERM
go test ./internal/modules/feed -list '^Test' > "$raw"
awk '/^Test[A-Za-z0-9_]*$/ {print}' "$raw" |
 awk -v size="$size" '{
  if(n) pat=pat "|" $0; else pat=$0
  n++
  if(n==size) {print pat; pat=""; n=0}
 } END {if(n) print pat}' > "$tmp"
i=0
while IFS= read -r names; do
 i=$((i+1))
 if [ "$only" -gt 0 ] && [ "$only" -ne "$i" ]; then continue; fi
 if [ "$mode" = list ]; then
  printf 'shard=%02d tests=%s\n' "$i" "$(printf '%s' "$names" | awk -F '|' '{print NF}')"
 else
  echo "RUN FEED SHARD $i"
  go test ./internal/modules/feed -run "^($names)$" -count=1 -timeout=140s
 fi
done < "$tmp"
[ "$i" -gt 0 ] || { echo "no Feed tests were enumerated" >&2; exit 2; }
if [ "$only" -gt "$i" ]; then echo "shard out of range" >&2; exit 2; fi
echo "FEED_SHARDS=$i MODE=$mode SELECTED=$only"
