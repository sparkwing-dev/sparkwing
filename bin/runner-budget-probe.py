#!/usr/bin/env python3
"""Measure the job's real cgroup, then judge the enforced/control pair."""

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


def measure(output, mode):
    if mode not in ('enforce', 'control'):
        raise ValueError('expected enforce or control measurement')
    relative = next(line[3:] for line in Path('/proc/self/cgroup').read_text().splitlines()
                    if line.startswith('0::'))
    group = Path('/sys/fs/cgroup') / relative.lstrip('/')
    unit = next(part for part in reversed(group.parts)
                if part.startswith('sparkwing-runner-') and part.endswith('.service'))

    def stat():
        return dict((key, int(value)) for key, value in
                    (line.split() for line in (group / 'cpu.stat').read_text().splitlines()))

    burners = []
    try:
        for _ in range(6):
            burners.append(subprocess.Popen(['yes'], stdout=subprocess.DEVNULL))
        burner_cgroups = {
            str(p.pid): next(line[3:] for line in Path(f'/proc/{p.pid}/cgroup').read_text().splitlines()
                             if line.startswith('0::'))
            for p in burners
        }
        assert all(value == relative for value in burner_cgroups.values()), 'burner outside measured cgroup'
        before, start = stat(), time.monotonic_ns()
        time.sleep(20)
        end, after = time.monotonic_ns(), stat()
        assert all(p.poll() is None for p in burners), 'a burner exited early'
        seconds = (end - start) / 1e9
        result = dict(unit=unit, cgroup=relative, pid=os.getpid(), nice=os.getpriority(os.PRIO_PROCESS, 0),
                      cpu_max=read_cpu_max(group, mode), before=before, after=after,
                      seconds=seconds, cores=(after['usage_usec'] - before['usage_usec']) / seconds / 1e6,
                      burners=[p.pid for p in burners], burner_cgroups=burner_cgroups)
        Path(output).write_text(json.dumps(result, indent=2) + '\n')
        print(json.dumps(result))
    finally:
        for process in burners:
            process.terminate()
        for process in burners:
            process.wait()


def judge(enforced, control):
    for row in (enforced, control):
        assert math.isfinite(row['cores']) and row['cores'] > 0, 'invalid CPU measurement'
        assert math.isfinite(row['seconds']) and 19 <= row['seconds'] <= 25, 'invalid measurement interval'
        measured = (row['after']['usage_usec'] - row['before']['usage_usec']) / row['seconds'] / 1e6
        assert math.isclose(measured, row['cores']), 'CPU result disagrees with raw counters'
        assert len(row['burners']) == 6 and len(set(row['burners'])) == 6, 'expected six burners'
        assert row['burner_cgroups'] == {str(pid): row['cgroup'] for pid in row['burners']}, 'burner cgroup evidence disagrees'
        assert row['unit'].startswith('sparkwing-runner-') and row['unit'].endswith('.service'), 'not a local runner'
    quota, period = enforced['cpu_max'].split()
    assert quota != 'max' and int(quota) / int(period) == 2, 'kernel quota is not two cores'
    assert enforced['nice'] == 10, 'job did not inherit nice 10'
    assert control['cpu_max'].split()[0] == 'max', 'control has a CPU quota'
    assert control['nice'] == 0, 'control inherited a nice adjustment'
    assert enforced['cores'] <= 2.2, 'enforced job exceeded 2.2 cores'
    assert control['cores'] > 3 and control['cores'] > 1.5 * enforced['cores'], 'control lacks CPU headroom'
    print(f"RESULT: PASS (enforced={enforced['cores']:.3f} cores, control={control['cores']:.3f} cores)")


if __name__ == '__main__':
    try:
        if sys.argv[1] == 'measure':
            measure(sys.argv[2], sys.argv[3])
        elif sys.argv[1] == 'judge':
            judge(*(json.loads(Path(path).read_text()) for path in sys.argv[2:4]))
        else:
            raise ValueError('expected measure or judge')
    except Exception as error:
        print(f'RESULT: FAIL ({error})', file=sys.stderr)
        sys.exit(1)
