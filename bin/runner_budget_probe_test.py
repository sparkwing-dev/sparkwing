import contextlib
import copy
import io
import json
from pathlib import Path
import runpy
import tempfile
import types
import unittest
from unittest import mock


probe = types.SimpleNamespace(**runpy.run_path(str(Path(__file__).with_name('runner-budget-probe.py'))))
# Functions keep runpy's globals; patch those rather than the namespace copy.
globals_ = probe.measure.__globals__


def row(cores, quota, nice):
    return dict(cores=cores, seconds=20, cpu_max=quota, nice=nice,
                unit='sparkwing-runner-proof.service', burners=list(range(6)),
                cgroup='/sparkwing-runner-proof.service',
                burner_cgroups={str(pid): '/sparkwing-runner-proof.service' for pid in range(6)},
                before=dict(usage_usec=1000), after=dict(usage_usec=1000 + cores * 20e6))


class BudgetProbeTest(unittest.TestCase):
    def measured(self, mode, quota=None, foreign_pid=None):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            group = root / 'cgroup' / 'sparkwing-runner-proof.service'
            group.mkdir(parents=True)
            (root / 'membership').write_text('0::/sparkwing-runner-proof.service\n')
            (group / 'cpu.stat').write_text('usage_usec 1000\n')
            if quota is not None:
                (group / 'cpu.max').write_text(quota)
            for pid in range(6):
                membership = root / f'pid-{pid}'
                membership.write_text('0::/foreign.service\n' if pid == foreign_pid else
                                      '0::/sparkwing-runner-proof.service\n')
            output = root / 'result.json'
            burners = [mock.Mock(pid=pid) for pid in range(6)]
            for burner in burners:
                burner.poll.return_value = None

            def path(value):
                for pid in range(6):
                    if str(value) == f'/proc/{pid}/cgroup':
                        return root / f'pid-{pid}'
                return {'/proc/self/cgroup': root / 'membership',
                        '/sys/fs/cgroup': root / 'cgroup'}.get(str(value), Path(value))

            def elapsed(seconds):
                self.assertEqual(seconds, 20)
                cores = 2 if mode == 'enforce' else 4
                (group / 'cpu.stat').write_text(f'usage_usec {1000 + cores * 20000000}\n')

            with mock.patch.dict(globals_, Path=path), \
                    mock.patch.object(probe.subprocess, 'Popen', side_effect=burners), \
                    mock.patch.object(probe.time, 'sleep', side_effect=elapsed), \
                    mock.patch.object(probe.time, 'monotonic_ns', side_effect=[0, 20000000000]), \
                    mock.patch.object(probe.os, 'getpriority', return_value=10 if mode == 'enforce' else 0), \
                    contextlib.redirect_stdout(io.StringIO()):
                try:
                    probe.measure(output, mode)
                finally:
                    for burner in burners:
                        burner.terminate.assert_called_once()
                        burner.wait.assert_called_once()
            return json.loads(output.read_text())

    @unittest.skipUnless(probe.sys.platform == "linux", "hosted runner proof requires Linux")
    def test_both_runner_cases_reuse_the_build_cache(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            shims = root / 'shims'
            shims.mkdir()
            capture = root / 'capture'
            capture.mkdir()
            shim = shims / 'shim'
            shim.write_text('''#!/usr/bin/python3
import json
import os
from pathlib import Path
import sys

name = Path(sys.argv[0]).name
args = sys.argv[1:]
capture = Path(os.environ['BUDGET_TEST_CAPTURE'])
if name == 'bash':
    assert Path(args[0]).name == 'runner-isolation-proof.sh' and args[1:] == ['--preflight']
    print('fixture: hosted VM preflight accepted')
elif name == 'go':
    if args == ['version']:
        print('fixture Go')
    elif args[:1] == ['env']:
        print(os.environ[args[1]])
    else:
        assert 'build' in args
        (capture / 'build.json').write_text(json.dumps({key: os.environ[key] for key in ('GOCACHE', 'GOPATH')}))
        target = Path(args[args.index('-o') + 1])
        target.write_bytes(Path(__file__).read_bytes())
        target.chmod(0o700)
elif name == 'systemctl':
    if args[:2] == ['--user', 'set-environment']:
        (capture / 'manager.json').write_text(json.dumps(dict(item.split('=', 1) for item in args[2:])))
    else:
        assert args in [['--version'], ['--user', 'show-environment']] or args[:2] == ['--user', 'show']
        print('fixture manager')
elif name == 'sudo':
    assert args[:2] == ['-n', 'loginctl'] or args[:2] == ['-n', 'systemctl']
elif name == 'loginctl':
    assert args[0] == 'show-user'
    print('Linger=yes')
elif name == 'git':
    if 'rev-parse' in args:
        print('0123456789abcdef0123456789abcdef01234567')
    else:
        assert any(verb in args for verb in ('init', 'add', 'commit'))
elif name == 'sparkwing':
    if args[:1] == ['run']:
        case = Path(os.environ['SPARKWING_HOME']).parent.name
        manager = json.loads((capture / 'manager.json').read_text())
        (capture / (case + '.json')).write_text(json.dumps(dict(
            environment={key: os.environ[key] for key in ('HOME', 'GOCACHE', 'GOPATH')}, manager=manager)))
        cores = 2 if case == 'enforce' else 4
        group = '/sparkwing-runner-proof.service'
        result = dict(unit='sparkwing-runner-proof.service', cgroup=group,
                      burners=list(range(6)), burner_cgroups={str(pid): group for pid in range(6)},
                      cores=cores, seconds=20, nice=10 if case == 'enforce' else 0,
                      cpu_max='200000 100000' if case == 'enforce' else 'max',
                      before=dict(usage_usec=1000), after=dict(usage_usec=1000 + cores * 20e6))
        (Path(os.environ['BUDGET_TEST_EVIDENCE']) / (case + '.json')).write_text(json.dumps(result))
    else:
        assert args == ['queue', 'list', '-o', 'json'] or args == ['daemon', 'stop']
else:
    raise AssertionError('unexpected shim: ' + name)
''')
            shim.chmod(0o700)
            for name in ('bash', 'go', 'systemctl', 'sudo', 'loginctl', 'git'):
                (shims / name).symlink_to(shim)
            expected = {'GOCACHE': str(root / 'build cache'), 'GOPATH': str(root / 'go path')}
            evidence = root / 'evidence'
            environment = dict(probe.os.environ, **expected,
                               PATH=str(shims) + probe.os.pathsep + probe.os.environ['PATH'],
                               HOME=str(root / 'home'), SPARKWING_HOME=str(root / 'state'),
                               XDG_CONFIG_HOME=str(root / 'config'), XDG_DATA_HOME=str(root / 'data'),
                               XDG_CACHE_HOME=str(root / 'cache'), XDG_STATE_HOME=str(root / 'state'),
                               XDG_RUNTIME_DIR=str(root / 'runtime'),
                               DBUS_SESSION_BUS_ADDRESS='unix:path=' + str(root / 'no-bus'),
                               RUNNER_TEMP=str(root / 'runner temp'),
                               BUDGET_TEST_CAPTURE=str(capture), BUDGET_TEST_EVIDENCE=str(evidence),
                               GITHUB_STEP_SUMMARY=str(root / 'summary'))
            script = Path(__file__).with_name('runner-budget-proof.sh')
            result = probe.subprocess.run(['/bin/bash', str(script), str(evidence)],
                                          env=environment, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertIn('RESULT: PASS', (evidence / 'verdict.txt').read_text())
            self.assertEqual(json.loads((capture / 'build.json').read_text()), expected)
            for case in ('enforce', 'control'):
                with self.subTest(case=case):
                    received = json.loads((capture / (case + '.json')).read_text())
                    self.assertEqual(received['manager'], expected)
                    self.assertNotEqual(received['environment'].pop('HOME'), environment['HOME'])
                    self.assertEqual(received['environment'], expected)

    def test_each_burner_must_share_the_measured_cgroup(self):
        for pid in range(6):
            with self.subTest(pid=pid), self.assertRaisesRegex(AssertionError, 'burner.*cgroup'):
                self.measured('enforce', '200000 100000', foreign_pid=pid)

    def test_control_without_cpu_max_measures_usage_and_passes_verdict(self):
        control = self.measured('control')
        self.assertEqual(control['cpu_max'], 'max')
        self.assertEqual(control['before']['usage_usec'], 1000)
        self.assertEqual(control['after']['usage_usec'], 80001000)
        self.assertEqual(control['cores'], 4)
        probe.judge(self.measured('enforce', '200000 100000'), control)

    def test_enforced_measurement_requires_cpu_max(self):
        with self.assertRaises(FileNotFoundError):
            self.measured('enforce')

    def test_present_control_quota_is_preserved_and_rejected(self):
        control = self.measured('control', '200000 100000')
        self.assertEqual(control['cpu_max'], '200000 100000')
        with self.assertRaisesRegex(AssertionError, 'control has a CPU quota'):
            probe.judge(row(2, '200000 100000', 10), control)

    def test_verdict_rejects_invalid_measurements(self):
        enforced = row(2.2, '200000 100000', 10)
        control = row(4, 'max', 0)
        cases = [
            ('enforced nice', 0, 'nice', 0, 'nice 10'),
            ('control nice', 1, 'nice', 10, 'nice adjustment'),
            ('control quota', 1, 'cpu_max', '200000 100000', 'control has a CPU quota'),
            ('missing enforced quota', 0, 'cpu_max', 'max 100000', 'kernel quota'),
            ('wrong enforced quota', 0, 'cpu_max', '300000 100000', 'kernel quota'),
            ('short window', 0, 'seconds', 18.9, 'measurement interval'),
            ('long window', 1, 'seconds', 25.1, 'measurement interval'),
            ('missing burner', 0, 'burners', list(range(5)), 'six burners'),
            ('extra burner', 0, 'burners', list(range(7)), 'six burners'),
            ('extra duplicate burner', 1, 'burners', [0, 1, 2, 3, 4, 5, 5], 'six burners'),
            ('duplicate burner', 1, 'burners', [0, 1, 2, 3, 4, 4], 'six burners'),
            ('missing burner evidence', 0, 'burner_cgroups', {}, 'burner cgroup evidence'),
            ('foreign burner evidence', 1, 'burner_cgroups', {'0': '/other'}, 'burner cgroup evidence'),
            ('wrong unit prefix', 0, 'unit', 'other-proof.service', 'local runner'),
            ('wrong unit suffix', 1, 'unit', 'sparkwing-runner-proof.scope', 'local runner'),
        ]
        for name, index, key, value, reason in cases:
            with self.subTest(name=name):
                pair = copy.deepcopy([enforced, control])
                pair[index][key] = value
                with self.assertRaisesRegex(AssertionError, reason):
                    probe.judge(*pair)
        for cores, passes in [(3.3, False), (3.30001, True)]:
            with self.subTest(control_cores=cores):
                candidate = row(cores, 'max', 0)
                if passes:
                    probe.judge(enforced, candidate)
                else:
                    with self.assertRaisesRegex(AssertionError, 'control lacks CPU headroom'):
                        probe.judge(enforced, candidate)


if __name__ == '__main__':
    unittest.main()
