"""Guard the repair overlay without changing historical task DAG semantics."""
import copy
import unittest
import validate

EXPECTED = {
    'T06': {'T27-F02', 'MERGE-F01'}, 'T14': {'T27-F01'},
    'T15': {'T28-F01', 'T28-F02', 'T28-F03'},
    'T16': {'T29-E01', 'T29-E02', 'MERGE-F03'},
    'T17': {'T30-F01', 'T30-U01', 'T30-F03', 'T30-F04', 'G05-F01', 'T26-F01'},
    'T23': {'T30-F02'},
    'T18': {'MERGE-F02'},
    'T04': {'G05-F02'},
}


def overlay_errors(overlay, package):
    errors = []
    lanes = overlay['lanes']
    if len(lanes) != 6 or {l['owner'] for l in lanes} != set(EXPECTED) - {'T18', 'T04'}:
        errors.append('six original owners required')
    followups = overlay.get('followups', [])
    if len(followups) != 2 or {l['owner'] for l in followups} != {'T18', 'T04'}:
        errors.append('T18 and T04 review followups required')
    lanes = lanes + followups
    if overlay['max_parallelism'] != 6:
        errors.append('parallel capacity changed')
    for lane in lanes:
        owner = lane['owner']
        findings = [f['id'] for f in lane['findings']]
        if set(findings) != EXPECTED.get(owner) or len(findings) != len(set(findings)):
            errors.append('finding coverage: ' + owner)
        task = package['tasks'][owner]
        for scope in lane['allow']:
            if not validate.valid_scope(scope) or not any(
                scope == allowed or validate.allows(allowed, scope.removesuffix('/**'))
                for allowed in task['ownership']['allow']
            ):
                errors.append('unauthorized scope: ' + scope)
        for finding in lane['findings']:
            verifier = finding['check'].split('-')[0]
            if finding['check'] not in {v['id'] for v in package['tasks'][verifier]['validation']}:
                errors.append('unknown check: ' + finding['check'])
            if not any(finding['id'] in a['requirement'] for a in task['acceptance_criteria']):
                errors.append('missing task acceptance: ' + finding['id'])
    for index, left in enumerate(lanes):
        for right in lanes[index+1:]:
            if any(validate.overlaps(a, b) for a in left['allow'] for b in right['allow']):
                errors.append('overlapping writers')
    summary = package['manifest']['summary']
    for key, expected in [('total_task_count', 47), ('requirement_count', 96), ('history_commit_count', 45)]:
        if summary[key] != expected:
            errors.append('historical coverage changed')
    if overlay['historical_g05_dependencies'] != package['tasks']['G05']['depends_on']:
        errors.append('G05 dependencies changed')
    return errors


class ReworkTests(unittest.TestCase):
    def setUp(self):
        self.package = validate.load_package()
        self.overlay = validate.read_json(validate.PACKAGE / 'rework.json')

    def test_current_overlay(self):
        self.assertEqual([], overlay_errors(self.overlay, self.package))

    def test_missing_followup_rejected(self):
        self.overlay['followups'] = []
        self.assertIn('T18 and T04 review followups required', overlay_errors(self.overlay, self.package))

    def test_missing_followup_finding_rejected(self):
        self.overlay['followups'][0]['findings'] = []
        self.assertIn('finding coverage: T18', overlay_errors(self.overlay, self.package))

    def test_missing_t04_followup_finding_rejected(self):
        self.overlay['followups'][1]['findings'] = []
        self.assertIn('finding coverage: T04', overlay_errors(self.overlay, self.package))

    def test_missing_finding_rejected(self):
        self.overlay['lanes'][2]['findings'].pop()
        self.assertTrue(overlay_errors(self.overlay, self.package))

    def test_cross_writer_scope_rejected(self):
        self.overlay['lanes'][0]['allow'].append('claude/**')
        self.assertIn('overlapping writers', overlay_errors(self.overlay, self.package))

    def test_missing_acceptance_rejected(self):
        package = copy.deepcopy(self.package)
        package['tasks']['T06']['acceptance_criteria'] = []
        self.assertTrue(overlay_errors(self.overlay, package))

    def test_missing_gate_dependency_rejected(self):
        self.overlay['historical_g05_dependencies'].pop()
        self.assertTrue(overlay_errors(self.overlay, self.package))


if __name__ == '__main__':
    unittest.main()
