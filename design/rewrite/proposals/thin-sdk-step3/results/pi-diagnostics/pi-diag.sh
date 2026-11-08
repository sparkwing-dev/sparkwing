#!/usr/bin/env bash
set -u
D=$HOME/sdkstep3/diag; mkdir -p "$D"; cd "$HOME/sdkstep3/tree/internal/nodehost"
for v in TC P2 T; do
  HOME=$HOME/sdkstep3/home TMPDIR=$HOME/sdkstep3/tmp HOSTEXEC_SDK_CPUPROFILE=$D/sdk-$v.prof \
  HOSTEXEC_BENCH="{\"lang\":\"go\",\"variant\":\"$v\",\"size\":\"bulk\",\"concurrency\":1,\"count\":1,\"bytes\":8388608,\"out\":\"$D/r-$v.json\",\"dir\":\"$D\"}" \
  timeout 300 "$HOME/sdkstep3/nodehost-diag.test" -test.run '^TestHostExecBenchmark$' > "$D/out-$v.txt" 2>&1
  python3 -c "import json;r=json.load(open('$D/r-$v.json'));print('$v wall',r['wall_ns']//1000000,'step',r['step_ns']//1000000,'sdkcpu',r['sdk_cpu_ns']//1000000)"
done
