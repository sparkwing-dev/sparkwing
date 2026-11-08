#!/usr/bin/env python3
"""Measure host exec (P1 logged, P2 captured) against today's SDK-run commands.

Every variant runs real Sparkwing code: TestHostExecBenchmark in internal/nodehost starts
a real protocol session with a Go or TypeScript SDK child and one bench step.

  D   the SDK spawns directly and discards output (process spawn floor, no masking)
  T   today's Run: the SDK spawns, sends base64 output frames, the host masks and logs
  TC  today's Capture: the SDK spawns and keeps the output, nothing is logged
  P1  host exec Run: the host spawns, masks and logs, and returns bounded tails
  P2  host exec Capture: the host spawns and relays every byte, nothing is logged

Usage:
  bench.py run --binary nodehost.test --root <repo> --out <dir> [--conditions quiet,loaded]
  bench.py summarize <dir>
"""
import argparse, gzip, hashlib, json, os, platform, random, signal, subprocess as sp, sys, threading, time
from pathlib import Path

VARIANTS = ['D', 'T', 'TC', 'P1', 'P2']


def write(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(path.suffix + '.tmp')
    if path.suffix == '.gz':
        with gzip.open(tmp, 'wt') as f:
            json.dump(value, f)
    else:
        tmp.write_text(json.dumps(value, indent=1) + '\n')
    tmp.rename(path)


def read(path):
    if str(path).endswith('.gz'):
        with gzip.open(path, 'rt') as f:
            return json.load(f)
    return json.loads(Path(path).read_text())


def load():
    return list(os.getloadavg())


def cpu_idle(seconds=2.0):
    # WSL's load average counts blocked tasks, so idle CPU is the honest quiet signal there.
    def sample():
        fields = [int(v) for v in Path('/proc/stat').read_text().split('\n')[0].split()[1:]]
        return fields[3] + fields[4], sum(fields)
    idle0, total0 = sample()
    time.sleep(seconds)
    idle1, total1 = sample()
    return (idle1 - idle0) / max(1, total1 - total0)


def quiet(args):
    if args.quiet_idle:
        return cpu_idle() >= args.quiet_idle
    return load()[0] < args.quiet_threshold


def load_worker(kind, scratch):
    # The prototype's load: one busy loop per worker, plus one writer that fsyncs a 64 MiB ring.
    if kind == 'cpu':
        x = 1
        while True:
            x = (x * 1103515245 + 12345) & 0xffffffff
    block = os.urandom(1 << 20)
    with open(Path(scratch) / 'load-io.bin', 'wb', buffering=0) as f:
        while True:
            f.seek(0)
            for _ in range(64):
                f.write(block)
            os.fsync(f.fileno())


def run_cell(args, raw, name, spec, env):
    work = raw / 'work' / name
    work.mkdir(parents=True, exist_ok=True)
    proc_env = dict(env, HOSTEXEC_BENCH=json.dumps(dict(spec, out=str(work / 'result.json'), dir=str(work))))
    began = time.time()
    p = sp.Popen([args.binary, '-test.run=^TestHostExecBenchmark$', '-test.count=1', '-test.timeout=30m'],
                 cwd=Path(args.root) / 'internal' / 'nodehost', env=proc_env, stdout=sp.PIPE, stderr=sp.STDOUT, start_new_session=True)
    try:
        out, _ = p.communicate(timeout=args.run_timeout)
    except sp.TimeoutExpired:
        os.killpg(p.pid, signal.SIGKILL)
        out, _ = p.communicate()
    result = None
    if (work / 'result.json').exists():
        result = json.loads((work / 'result.json').read_text())
    log = work / 'result.json.log'
    log_size = log.stat().st_size if log.exists() else 0
    record = {'name': name, 'spec': spec, 'returncode': p.returncode, 'output': out.decode(errors='replace')[-4000:],
              'wall_start': began, 'wall_end': time.time(), 'log_file_bytes': log_size, 'result': result}
    write(raw / 'runs' / (name + '.json.gz'), record)
    # The log file is the product's own output of repeated text; its size and counts are kept, the bytes are not.
    for f in work.iterdir():
        f.unlink()
    work.rmdir()
    return record


def measure(args):
    raw = Path(args.out)
    raw.mkdir(parents=True, exist_ok=True)
    env = dict(os.environ)
    env['PATH'] = os.pathsep.join(p for p in [args.node_dir, env.get('PATH', '')] if p)
    versions = {}
    for label, cmd in [('node', ['node', '--version']), ('kernel', ['uname', '-r'])]:
        try:
            versions[label] = sp.run(cmd, capture_output=True, text=True, env=env).stdout.strip()
        except OSError:
            versions[label] = None
    # The repository is public: record the machine's kind, never its name, user or paths.
    u = platform.uname()
    environment = {'uname': [u.system, u.release, u.version, u.machine], 'cpu_count': os.cpu_count(), 'initial_load': load(),
                   'binary_sha256': hashlib.sha256(Path(args.binary).read_bytes()).hexdigest(), 'versions': versions,
                   'small_count': args.small_count, 'bulk_bytes': args.bulk_bytes, 'rounds': args.rounds,
                   'quiet_threshold': args.quiet_threshold, 'quiet_idle': args.quiet_idle, 'languages': args.languages, 'conditions': args.conditions,
                   'concurrency': args.concurrency, 'meminfo': Path('/proc/meminfo').read_text() if Path('/proc/meminfo').exists() else None}
    write(raw / ('environment-%d.json' % int(time.time())), environment)
    cores = os.cpu_count() or 1
    load_spec = {'cpu_workers': max(1, cores - 1), 'io_workers': 1, 'io_ring_bytes': 64 << 20, 'io_fsync': 'every ring', 'stabilize_s': args.stabilize}
    generators = []
    stop = threading.Event()

    def stop_load():
        for g in generators:
            try:
                os.killpg(g.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
        codes = [g.wait() for g in generators]
        with (raw / 'load-cleanup.ndjson').open('a') as f:
            f.write(json.dumps({'pids': [g.pid for g in generators], 'returncodes': codes, 'time': time.time()}) + '\n')
        generators.clear()

    def monitor():
        with (raw / 'monitor.ndjson').open('a') as f:
            while not stop.is_set():
                f.write(json.dumps({'time': time.time(), 'load': load(), 'load_generators': len(generators)}) + '\n')
                f.flush()
                stop.wait(1)

    def interrupted(signum, frame):
        stop_load()
        sys.exit(130)

    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    threading.Thread(target=monitor, daemon=True).start()
    done = {p.name.removesuffix('.json.gz') for p in (raw / 'runs').glob('*.json.gz')} if (raw / 'runs').exists() else set()
    try:
        for condition in args.conditions.split(','):
            if condition == 'loaded':
                write(raw / 'load-parameters.json', load_spec)
                for _ in range(load_spec['cpu_workers']):
                    generators.append(sp.Popen([sys.executable, __file__, 'load-worker', 'cpu', str(raw)], start_new_session=True))
                generators.append(sp.Popen([sys.executable, __file__, 'load-worker', 'io', str(raw)], start_new_session=True))
                time.sleep(args.stabilize)
            for rnd in range(args.rounds):
                for lang in args.languages.split(','):
                    for size in ['small', 'bulk']:
                        for con in [int(c) for c in args.concurrency.split(',')]:
                            order = VARIANTS[rnd % len(VARIANTS):] + VARIANTS[:rnd % len(VARIANTS)]
                            if rnd % 2:
                                order.reverse()
                            for variant in order:
                                name = f'{condition}-r{rnd}-{lang}-{size}-c{con}-{variant}'
                                if name in done:
                                    continue
                                waited = time.monotonic()
                                while condition == 'quiet' and not quiet(args):
                                    time.sleep(3)
                                count = args.small_count if size == 'small' else con
                                spec = {'lang': lang, 'variant': variant, 'size': size, 'concurrency': con, 'count': count, 'bytes': args.bulk_bytes}
                                before = load()
                                idle = cpu_idle(1.0) if args.quiet_idle else None
                                record = run_cell(args, raw, name, spec, env)
                                ok = record['returncode'] == 0 and record['result'] and record['result']['errors'] == 0
                                meta = {'condition': condition, 'round': rnd, 'quiet_wait_s': time.monotonic() - waited, 'load_start': before, 'cpu_idle_start': idle, 'load_end': load()}
                                record.update(meta)
                                write(raw / 'runs' / (name + '.json.gz'), record)
                                print(name, 'ok' if ok else 'FAILED', round(record['wall_end'] - record['wall_start'], 2), 's load', before[0], flush=True)
            if generators:
                stop_load()
    finally:
        stop.set()
        stop_load()


def pct(values, p):
    values = sorted(values)
    return values[min(len(values) - 1, max(0, int(round(p / 100 * len(values) + 0.5)) - 1))] if values else None


def median(values):
    values = sorted(v for v in values if v is not None)
    if not values:
        return None
    n = len(values)
    return values[n // 2] if n % 2 else (values[n // 2 - 1] + values[n // 2]) / 2


def summarize(directory):
    runs = {}
    failures = []
    for path in sorted(Path(directory).glob('runs/*.json.gz')):
        r = read(path)
        res = r.get('result')
        if r['returncode'] != 0 or not res or res['errors']:
            failures.append(r['name'])
            continue
        s = r['spec']
        key = (r.get('condition', r['name'].split('-')[0]), s['lang'], s['size'], s['concurrency'])
        samples = res['samples_ns']
        total = s['bytes'] * s['count'] if s['size'] == 'bulk' else 0
        runs.setdefault(key, {}).setdefault(r.get('round', 0), {})[s['variant']] = {
            'p50_ms': pct(samples, 50) / 1e6, 'p95_ms': pct(samples, 95) / 1e6,
            'cmds_s': s['count'] / (res['wall_ns'] / 1e9), 'mib_s': total / (1 << 20) / (res['wall_ns'] / 1e9) if total else None,
            'host_cpu_s': res['host_cpu_ns'] / 1e9, 'sdk_cpu_s': res['sdk_cpu_ns'] / 1e9,
            'log_records': res['log_records'], 'log_bytes': res['log_message_bytes'], 'sdk_bytes': res['bytes'], 'expected_bytes': total}
    rows = []
    for key in sorted(runs):
        rounds = runs[key]
        for variant in VARIANTS:
            vals = [rd[variant] for rd in rounds.values() if variant in rd]
            if not vals:
                continue
            row = {'condition': key[0], 'lang': key[1], 'size': key[2], 'concurrency': key[3], 'variant': variant, 'rounds': len(vals)}
            for field in ['p50_ms', 'p95_ms', 'cmds_s', 'mib_s', 'host_cpu_s', 'sdk_cpu_s']:
                row[field] = median([v[field] for v in vals])
            # Paired against the same round's baseline, so drift between rounds cancels.
            for base in ['D', 'T', 'TC']:
                pairs = [(rd[variant], rd[base]) for rd in rounds.values() if variant in rd and base in rd]
                if pairs and variant != base:
                    row['added_p50_ms_vs_' + base] = median([a['p50_ms'] - b['p50_ms'] for a, b in pairs])
                    row['added_p95_ms_vs_' + base] = median([a['p95_ms'] - b['p95_ms'] for a, b in pairs])
                    metric = 'mib_s' if key[2] == 'bulk' else 'cmds_s'
                    row['ratio_vs_' + base] = median([a[metric] / b[metric] for a, b in pairs])
            if key[2] == 'bulk':
                row['bytes_check'] = all((v['log_bytes'] if variant in ('T', 'P1') else v['sdk_bytes']) >= v['expected_bytes'] * 80 / 81 - 81 * key[3] for v in vals)
            rows.append(row)
    summary = {'rows': rows, 'failures': failures}
    write(Path(directory) / 'summary.json', summary)
    return summary


def table(summary, baseline_for):
    lines = ['| Condition | Lang | Size | C | Variant | Rounds | p50 ms | p95 ms | cmds/s | MiB/s | Added p50 vs base | Ratio vs base | Ratio vs D | Host CPU s | SDK CPU s |',
             '| --- | --- | --- | ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |']
    f = lambda v, d=3: '' if v is None else f'{v:.{d}f}'
    for r in summary['rows']:
        base = baseline_for.get(r['variant'])
        lines.append('| ' + ' | '.join([r['condition'], r['lang'], r['size'], str(r['concurrency']), r['variant'] + (f' (vs {base})' if base else ''), str(r['rounds']),
                                        f(r['p50_ms']), f(r['p95_ms']), f(r['cmds_s'], 1), f(r['mib_s'], 1),
                                        f(r.get('added_p50_ms_vs_' + base)) if base else '', (f(r.get('ratio_vs_' + base) * 100, 1) + '%') if base and r.get('ratio_vs_' + base) else '',
                                        (f(r['ratio_vs_D'] * 100, 1) + '%') if r.get('ratio_vs_D') else '', f(r['host_cpu_s']), f(r['sdk_cpu_s'])]) + ' |')
    return '\n'.join(lines)


def main():
    if len(sys.argv) > 1 and sys.argv[1] == 'load-worker':
        load_worker(sys.argv[2], sys.argv[3])
        return
    p = argparse.ArgumentParser()
    sub = p.add_subparsers(dest='cmd', required=True)
    r = sub.add_parser('run')
    r.add_argument('--binary', required=True)
    r.add_argument('--root', required=True, help='tree holding internal/nodehost/testdata and sdk/typescript')
    r.add_argument('--out', required=True)
    r.add_argument('--node-dir', default='')
    r.add_argument('--conditions', default='quiet')
    r.add_argument('--languages', default='go,typescript')
    r.add_argument('--concurrency', default='1,8')
    r.add_argument('--rounds', type=int, default=5)
    r.add_argument('--small-count', type=int, default=2000)
    r.add_argument('--bulk-bytes', type=int, default=32 << 20)
    r.add_argument('--quiet-threshold', type=float, default=2.0)
    r.add_argument('--quiet-idle', type=float, default=0.0, help='admit quiet runs at this idle CPU fraction instead of load average')
    r.add_argument('--stabilize', type=int, default=30)
    r.add_argument('--run-timeout', type=int, default=900)
    s = sub.add_parser('summarize')
    s.add_argument('dir')
    a = p.parse_args()
    if a.cmd == 'run':
        measure(a)
    summary = summarize(a.out if a.cmd == 'run' else a.dir)
    print(table(summary, {'P1': 'T', 'P2': 'TC', 'T': 'D', 'TC': 'D'}))
    if summary['failures']:
        print('failed runs:', summary['failures'])


if __name__ == '__main__':
    main()
