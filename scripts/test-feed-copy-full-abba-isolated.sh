#!/usr/bin/env bash
# Reversed-order COPY/GORM full-rebuild validation on four disposable PG16
# containers. This script intentionally cannot target a supplied database.
set -euo pipefail
if [[ "$#" -ne 1 || ! "$1" =~ ^(10000|100000)$ ]]; then
  echo 'Usage: scripts/test-feed-copy-full-abba-isolated.sh 10000|100000' >&2
  exit 2
fi
if [[ -n "${TEST_POSTGRES_DSN:-}" || -n "${TEST_DATABASE_URL:-}" ]]; then
  echo 'Refusing ABBA benchmark with externally supplied DB credentials' >&2
  exit 2
fi
cd "$(dirname "$0")/.."
output_dir="$(mktemp -d /tmp/atoman-feed-copy-abba-XXXXXXXX)"
printf 'ABBA_LOG_DIR %s\n' "$output_dir"
# COPY-first tests the opposite cache/load ordering of the earlier GORM-first
# comparison. Both implementations use the same checked-in dataset recipe.
modes=(copy gorm gorm copy)
for i in "${!modes[@]}"; do
  mode="${modes[i]}"
  if [[ "$mode" == 'copy' ]]; then copy_experiment=yes; else copy_experiment=''; fi
  label="$((i+1))-${mode}"
  printf 'ABBA_BEGIN trial=%d mode=%s count=%s\n' "$((i+1))" "$mode" "$1"
  FEED_SCALE_PROFILE=prodshape FEED_SNAPSHOT_STAGE_TRACE=yes \
  FEED_SNAPSHOT_COPY_EXPERIMENT="$copy_experiment" \
  nice -n 10 ionice -c 3 bash scripts/test-feed-scale-isolated.sh "$1" 2>&1 | tee "$output_dir/$label.log"
  printf 'ABBA_END trial=%d mode=%s\n' "$((i+1))" "$mode"
done
python3 - "$output_dir" "$1" <<'PY'
import pathlib
import re
import statistics
import sys

folder=pathlib.Path(sys.argv[1]);count=int(sys.argv[2]);records=[]
def seconds(s):
    match=re.fullmatch(r'(?:(\d+)h)?(?:(\d+)m)?([\d.]+)(ns|us|µs|ms|s)',s)
    if not match: raise ValueError('bad duration: '+s)
    h,m,value,unit=match.groups()
    return int(h or 0)*3600+int(m or 0)*60+float(value)*{'s':1,'ms':0.001,'us':1e-6,'µs':1e-6,'ns':1e-9}[unit]
for index, mode in enumerate(('copy','gorm','gorm','copy'),1):
    txt=(folder/f'{index}-{mode}.log').read_text()
    if 'TEST_PASSED scale='+str(count) not in txt: raise RuntimeError('missing pass marker on '+mode)
    initial=re.search(r'SCALE_BUILD count=\d+ groups=(\d+) elapsed=(\S+)',txt)
    second=re.search(r'SCALE_SECOND_REBUILD elapsed=(\S+) total=(\d+)',txt)
    commits=re.findall(r'FEED_BUILD_STAGE stage=publish_commit elapsed=(\S+)',txt)
    annotations=re.findall(r'FEED_BUILD_STAGE stage=annotate elapsed=(\S+)',txt)
    if not initial or not second or len(commits)!=2 or len(annotations)!=2:
        raise RuntimeError('incomplete metrics in '+mode)
    rec={'trial':index,'mode':mode,'groups':(int(initial.group(1)),int(second.group(2))),
      'build':(seconds(initial.group(2)),seconds(second.group(1))),
      'commit':tuple(map(seconds,commits)), 'annotate':tuple(map(seconds,annotations))}
    records.append(rec)
    print(f'ABBA_RESULT trial={index} mode={mode} first_s={rec["build"][0]:.3f} second_s={rec["build"][1]:.3f} first_commit_s={rec["commit"][0]:.3f} second_commit_s={rec["commit"][1]:.3f} groups={rec["groups"][0]}/{rec["groups"][1]}')
if len({r['groups'] for r in records})!=1:
    raise RuntimeError('COPY/GORM different duplicate counts across ABBA runs')
for field in ('build','commit','annotate'):
    for phase in (0,1):
        gorm=statistics.median(r[field][phase] for r in records if r['mode']=='gorm')
        copy=statistics.median(r[field][phase] for r in records if r['mode']=='copy')
        delta=100*(gorm-copy)/gorm if gorm else float('nan')
        print(f'ABBA_MEDIAN metric={field} phase={phase+1} gorm_s={gorm:.3f} copy_s={copy:.3f} copy_change_pct={delta:.1f}')
print('ABBA_PASSED comparisons=4 phases=2 data_counts_equal=true')
PY
