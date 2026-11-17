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
                before=dict(usage_usec=1000), after=dict(usage_usec=1000 + cores * 20e6))


class BudgetProbeTest(unittest.TestCase):
    def measured(self, mode, quota=None):
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
                    contextlib.redirect_stdout(io.StringIO()):
                try:
                    probe.measure(output, mode)
                finally:
                    for burner in burners:
                        burner.terminate.assert_called_once()
                        burner.wait.assert_called_once()
            return json.loads(output.read_text())

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


if __name__ == '__main__':
    unittest.main()
