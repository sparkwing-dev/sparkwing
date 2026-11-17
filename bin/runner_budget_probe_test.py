import contextlib
import copy
import io
import json
from pathlib import Path
import runpy
import subprocess
import sys
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
                before=dict(usage_usec=1000), after=dict(usage_usec=1000 + cores * 20e6))


class BudgetProbeTest(unittest.TestCase):
    def measured(self, mode, quota=None, peer=None):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            group = root / 'cgroup' / 'sparkwing-runner-proof.service'
            group.mkdir(parents=True)
            (root / 'membership').write_text('0::/sparkwing-runner-proof.service\n')
            (group / 'cpu.stat').write_text('usage_usec 1000\n')
            if quota is not None:
                (group / 'cpu.max').write_text(quota)
            output = root / 'result.json'
            burners = [mock.Mock(pid=pid) for pid in range(6)]
            for burner in burners:
                burner.poll.return_value = None

            def path(value):
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
                    mock.patch.dict(globals_, process_usage=mock.Mock(side_effect=[0] * 6 + [10000000] * 6),
                                    wait_for=mock.Mock()), \
                    contextlib.redirect_stdout(io.StringIO()):
                try:
                    probe.measure(str(output), mode, peer)
                finally:
                    for burner in burners:
                        burner.terminate.assert_called_once()
                        burner.wait.assert_called_once()
            return json.loads(output.read_text())

    def test_job_measurement_uses_only_its_process_counters(self):
        result = self.measured('capped', '400000 100000', 'peer.json')
        self.assertEqual(result['cores'], 3)
        self.assertEqual(result['before']['usage_usec'], 0)
        self.assertEqual(result['after']['usage_usec'], 60000000)
        self.assertEqual(result['end_ns'] - result['start_ns'], 20000000000)

    def test_process_counters_include_user_and_system_ticks(self):
        with tempfile.TemporaryDirectory() as directory:
            stat = Path(directory) / 'stat'
            stat.write_text('123 (a name with ) parentheses) S ' + '0 ' * 10 + '150 50 999 999')
            with mock.patch.dict(globals_, Path=lambda _: stat), \
                    mock.patch.object(probe.os, 'sysconf', return_value=100):
                self.assertEqual(probe.process_usage(123), 2000000)

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


class JobBudgetVerdictTest(unittest.TestCase):
    def pair(self, capped_cores=1):
        capped = row(capped_cores, '400000 100000', 10)
        busy = row(2.8, '400000 100000', 10)
        for index, result in enumerate((capped, busy)):
            result.update(cgroup='/sparkwing-runner-proof.service',
                          start_ns=index * 100000000, end_ns=20000000000 + index * 100000000,
                          burners=list(range(index * 6, (index + 1) * 6)))
        return capped, busy

    def test_one_core_job_with_busy_peer(self):
        probe.judge_jobs(*self.pair())

    def test_rejects_per_job_oversubscription(self):
        with self.assertRaisesRegex(AssertionError, 'one-core job'):
            probe.judge_jobs(*self.pair(2))

    def test_cli_reports_measured_cap_failure(self):
        with tempfile.TemporaryDirectory() as directory:
            paths = [Path(directory) / name for name in ('capped.json', 'busy.json')]
            for path, result in zip(paths, self.pair(2)):
                path.write_text(json.dumps(result))
            result = subprocess.run([sys.executable, '-I',
                                     str(Path(__file__).with_name('runner-budget-probe.py')),
                                     'judge-jobs', *(str(path) for path in paths)],
                                    capture_output=True, text=True, check=False)
            self.assertEqual(result.returncode, 1)
            self.assertIn('one-core job measured 2.000 cores', result.stderr)

    def test_rejects_idle_or_serial_peer_and_wrong_runner(self):
        for key, value, reason in [('cores', 0.5, 'busy peer'),
                                   ('start_ns', 20000000000, 'overlap'),
                                   ('unit', 'sparkwing-runner-other.service', 'same runner'),
                                   ('burners', list(range(6)), 'shared burners')]:
            with self.subTest(key=key):
                capped, busy = self.pair()
                busy[key] = value
                if key == 'cores':
                    busy['after']['usage_usec'] = 1000 + value * 20e6
                with self.assertRaisesRegex(AssertionError, reason):
                    probe.judge_jobs(capped, busy)


if __name__ == '__main__':
    unittest.main()
