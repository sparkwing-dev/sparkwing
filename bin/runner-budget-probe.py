#!/usr/bin/env python3
"""Measure runner cgroup or per-job process CPU, then judge the proof."""

import json
import math
import os
from pathlib import Path
import subprocess
import sys
import time


def read_cpu_max(group, mode):
    try:
        return (group / 'cpu.max').read_text().strip()
    except FileNotFoundError:
        if mode != 'control':
            raise
        return 'max'


def process_usage(pid):
    fields = Path(f'/proc/{pid}/stat').read_text().rsplit(')', 1)[1].split()
    return (int(fields[11]) + int(fields[12])) * 1000000 / os.sysconf('SC_CLK_TCK')


def wait_for(path):
    deadline = time.monotonic() + 60
    while not path.exists():
        if time.monotonic() >= deadline:
            raise TimeoutError(f'peer did not reach {path.name}')
        time.sleep(0.05)


def measure(output, mode, peer=None):
    if mode not in ('enforce', 'control', 'capped', 'busy'):
        raise ValueError('expected enforce, control, capped or busy measurement')
    relative = next(line[3:] for line in Path('/proc/self/cgroup').read_text().splitlines()
                    if line.startswith('0::'))
    group = Path('/sys/fs/cgroup') / relative.lstrip('/')
    unit = next(part for part in reversed(group.parts)
                if part.startswith('sparkwing-runner-') and part.endswith('.service'))

    def stat():
        if peer is not None:
            return dict(usage_usec=sum(process_usage(p.pid) for p in burners))
        return dict((key, int(value)) for key, value in
                    (line.split() for line in (group / 'cpu.stat').read_text().splitlines()))

    burners = []
    try:
        for _ in range(6):
            burners.append(subprocess.Popen(['yes'], stdout=subprocess.DEVNULL))
        if peer is not None:
            Path(output + '.ready').touch()
            wait_for(Path(peer + '.ready'))
        before, start = stat(), time.monotonic_ns()
        time.sleep(20)
        end, after = time.monotonic_ns(), stat()
        assert all(p.poll() is None for p in burners), 'a burner exited early'
        seconds = (end - start) / 1e9
        result = dict(unit=unit, cgroup=relative, pid=os.getpid(), nice=os.getpriority(os.PRIO_PROCESS, 0),
                      cpu_max=read_cpu_max(group, mode), before=before, after=after,
                      seconds=seconds, cores=(after['usage_usec'] - before['usage_usec']) / seconds / 1e6,
                      burners=[p.pid for p in burners])
        if peer is not None:
            result.update(start_ns=start, end_ns=end)
        Path(output).write_text(json.dumps(result, indent=2) + '\n')
        print(json.dumps(result))
        if peer is not None:
            wait_for(Path(peer))
    finally:
        for process in burners:
            process.terminate()
        for process in burners:
            process.wait()


def validate(row):
    assert math.isfinite(row['cores']) and row['cores'] > 0, 'invalid CPU measurement'
    assert math.isfinite(row['seconds']) and 19 <= row['seconds'] <= 25, 'invalid measurement interval'
    measured = (row['after']['usage_usec'] - row['before']['usage_usec']) / row['seconds'] / 1e6
    assert math.isclose(measured, row['cores']), 'CPU result disagrees with raw counters'
    assert len(row['burners']) == 6 and len(set(row['burners'])) == 6, 'expected six burners'
    assert row['unit'].startswith('sparkwing-runner-') and row['unit'].endswith('.service'), 'not a local runner'


def judge(enforced, control):
    for row in (enforced, control):
        validate(row)
    quota, period = enforced['cpu_max'].split()
    assert quota != 'max' and int(quota) / int(period) == 2, 'kernel quota is not two cores'
    assert enforced['nice'] == 10, 'job did not inherit nice 10'
    assert control['cpu_max'].split()[0] == 'max', 'control has a CPU quota'
    assert control['nice'] == 0, 'control inherited a nice adjustment'
    assert enforced['cores'] <= 2.2, 'enforced job exceeded 2.2 cores'
    assert control['cores'] > 3 and control['cores'] > 1.5 * enforced['cores'], 'control lacks CPU headroom'
    print(f"RESULT: PASS (enforced={enforced['cores']:.3f} cores, control={control['cores']:.3f} cores)")


def judge_jobs(capped, busy):
    for result in (capped, busy):
        validate(result)
    assert capped['unit'] == busy['unit'], 'jobs did not share the same runner'
    assert not set(capped['burners']) & set(busy['burners']), 'jobs shared burners'
    overlap = (min(capped['end_ns'], busy['end_ns']) - max(capped['start_ns'], busy['start_ns'])) / 1e9
    assert overlap >= 19, 'jobs lack a concurrent measurement overlap'
    assert busy['cores'] > 1.5, 'busy peer lacks CPU headroom'
    assert 0.8 <= capped['cores'] <= 1.2, f"one-core job measured {capped['cores']:.3f} cores (expected 0.8..1.2)"
    print(f"RESULT: PASS (one-core job={capped['cores']:.3f} cores, busy peer={busy['cores']:.3f} cores)")


if __name__ == '__main__':
    try:
        if sys.argv[1] == 'measure':
            measure(sys.argv[2], sys.argv[3], sys.argv[4] if len(sys.argv) > 4 else None)
        elif sys.argv[1] == 'judge-jobs':
            judge_jobs(*(json.loads(Path(path).read_text()) for path in sys.argv[2:4]))
        elif sys.argv[1] == 'judge':
            judge(*(json.loads(Path(path).read_text()) for path in sys.argv[2:4]))
        else:
            raise ValueError('expected measure or judge')
    except Exception as error:
        print(f'RESULT: FAIL ({error})', file=sys.stderr)
        sys.exit(1)
