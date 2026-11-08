#!/usr/bin/env bash
set -eu
bench_root=$HOME/sdkstep3
D=$bench_root/diag
mkdir -p "$D"
for v in TC P2 T; do
  env -C "$bench_root/tree/internal/nodehost" \
  HOME="$bench_root/home" TMPDIR="$bench_root/tmp" HOSTEXEC_SDK_CPUPROFILE="$D/sdk-$v.prof" \
  HOSTEXEC_BENCH="{\"lang\":\"go\",\"variant\":\"$v\",\"size\":\"bulk\",\"concurrency\":1,\"count\":1,\"bytes\":8388608,\"out\":\"$D/r-$v.json\",\"dir\":\"$D\"}" \
  timeout 300 "$bench_root/nodehost-diag.test" -test.run '^TestHostExecBenchmark$' > "$D/out-$v.txt" 2>&1
  python3 -c "import json;r=json.load(open('$D/r-$v.json'));print('$v wall',r['wall_ns']//1000000,'step',r['step_ns']//1000000,'sdkcpu',r['sdk_cpu_ns']//1000000)"
done
