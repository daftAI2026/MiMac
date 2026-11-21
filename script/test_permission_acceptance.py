import json
import unittest
from unittest.mock import patch

from permission_acceptance import (
    Acceptance,
    CONTROL_PAGES,
    CONTROL_ROW_TITLES,
    INPUT_ROW_TITLE,
    existing_input_target,
    existing_input_segment_granted,
    ordinary_existing_input_target,
    ordinary_permission_guide_complete,
    general_input_action_target,
    drag_regressions_snapshot_ready,
    complete_permission_view,
    draggable_window_geometry_ready,
    drag_page_probe_ready,
    resume_input_drop_args,
)


def node(role, title=None, value=None, description=None, bounds=None, enabled=True):
    return {
        "role": role,
        "title": title,
        "description": description,
        "value": value,
        "enabled": enabled,
        "bounds": bounds,
    }


def ax_bounds(x, y, width=180, height=20):
    return {"x": x, "y": y, "width": width, "height": height}


def ordinary_permission_nodes(*, input_pending=False, control_pending=False):
    name = "MiMac Dev"
    nodes = [
        node("AXWindow", name, bounds=ax_bounds(0, 0, 1000, 700)),
        node("AXButton", description="管理输入监控", bounds=ax_bounds(600, 210)),
        node("AXButton", description="管理辅助功能", bounds=ax_bounds(600, 270)),
        node("AXWindow", name + " 系统权限", bounds=ax_bounds(1016, 0, 500, 430)),
        node("AXButton", description=INPUT_ROW_TITLE, bounds=ax_bounds(1040, 160, 220, 22)),
        node("AXButton", description=CONTROL_ROW_TITLES[0], bounds=ax_bounds(1040, 220, 220, 22)),
    ]
    if not input_pending:
        nodes.extend([
            node("AXStaticText", value="已授权", bounds=ax_bounds(440, 210, 100, 20)),
            node("AXStaticText", value="已授权", bounds=ax_bounds(1260, 160, 90, 22)),
        ])
    else:
        nodes.extend([
            node("AXButton", "授权…", bounds=ax_bounds(1350, 160, 110, 22)),
        ])
    if not control_pending:
        nodes.extend([
            node("AXStaticText", value="已授权", bounds=ax_bounds(440, 270, 100, 20)),
            node("AXStaticText", value="已授权", bounds=ax_bounds(1260, 220, 90, 22)),
        ])
    else:
        nodes.append(node("AXButton", "授权…", bounds=ax_bounds(1350, 220, 110, 22)))
    return nodes


class ExistingInputTargetTests(unittest.TestCase):
    def setUp(self):
        self.name = "MiMac Dev"
        self.control = CONTROL_ROW_TITLES[0]
        self.nodes = [
            node("AXWindow", self.name + " 首次设置"),
            node("AXWindow", self.name + " 系统权限"),
            node("AXButton", INPUT_ROW_TITLE),
            node("AXButton", self.control),
            node("AXButton", "授权…"),
            node("AXStaticText", value="已授权 1/2"),
        ]

    def test_selects_only_pending_device_control_segment(self):
        self.assertEqual(existing_input_target(self.nodes, self.name), self.control)

    def test_rejects_another_permission_count(self):
        nodes = [dict(item) for item in self.nodes]
        nodes[-1]["value"] = "已授权 1/3"
        self.assertIsNone(existing_input_target(nodes, self.name))

    def test_rejects_missing_input_row_or_permission_panel(self):
        self.assertIsNone(existing_input_target(self.nodes[2:], self.name))
        nodes = [item for item in self.nodes if item["title"] != INPUT_ROW_TITLE]
        self.assertIsNone(existing_input_target(nodes, self.name))

    def test_existing_input_wait_predicate_requires_the_actual_one_of_two_panel(self):
        acceptance = object.__new__(Acceptance)
        acceptance.name = self.name
        acceptance.view = lambda _pid: self.nodes[:1]
        self.assertIsNone(acceptance.existing_input_panel(101))
        acceptance.view = lambda _pid: self.nodes
        self.assertEqual(acceptance.existing_input_panel(101), self.nodes)


class OrdinaryGeneralPermissionViewTests(unittest.TestCase):
    def setUp(self):
        self.name = "MiMac Dev"

    def test_existing_input_target_uses_the_two_real_ordinary_surfaces(self):
        nodes = ordinary_permission_nodes(input_pending=False, control_pending=True)
        self.assertEqual(ordinary_existing_input_target(nodes, self.name), CONTROL_ROW_TITLES[0])
        self.assertEqual(existing_input_target(nodes, self.name), CONTROL_ROW_TITLES[0])
        self.assertFalse(any("2/2" in str(value) for item in nodes for value in item.values()))
        self.assertEqual(sum(1 for item in nodes if "已授权" in (item["title"], item["value"], item["description"])), 2)

    def test_existing_input_target_requires_both_surfaces_and_the_general_manage_rows(self):
        nodes = ordinary_permission_nodes(input_pending=False, control_pending=True)
        missing_card = [item for item in nodes if item["title"] != self.name + " 系统权限"]
        missing_settings = [item for item in nodes if item["title"] != self.name]
        missing_manage = [item for item in nodes if item.get("description") != "管理辅助功能"]
        self.assertIsNone(existing_input_target(missing_card, self.name))
        self.assertIsNone(existing_input_target(missing_settings, self.name))
        self.assertIsNone(existing_input_target(missing_manage, self.name))

    def test_existing_input_target_rejects_wrong_row_unmapped_or_duplicated_authorization(self):
        nodes = ordinary_permission_nodes(input_pending=False, control_pending=True)
        wrong_row = [dict(item) for item in nodes]
        action = next(item for item in wrong_row if item.get("title") == "授权…" and item["bounds"]["y"] == 220)
        action["bounds"] = ax_bounds(1350, 160, 110, 22)
        extra_action = [dict(item) for item in nodes] + [
            node("AXButton", "授权…", bounds=ax_bounds(1350, 330, 110, 22))
        ]
        extra_status = [dict(item) for item in nodes] + [
            node("AXStaticText", value="已授权", bounds=ax_bounds(500, 400, 100, 20))
        ]
        self.assertIsNone(existing_input_target(wrong_row, self.name))
        self.assertIsNone(existing_input_target(extra_action, self.name))
        self.assertIsNone(existing_input_target(extra_status, self.name))

    def test_cold_general_flow_accepts_input_pending_with_control_pending_or_granted(self):
        both_pending = ordinary_permission_nodes(input_pending=True, control_pending=True)
        control_granted = ordinary_permission_nodes(input_pending=True, control_pending=False)
        self.assertEqual(general_input_action_target(both_pending, self.name), INPUT_ROW_TITLE)
        self.assertEqual(general_input_action_target(control_granted, self.name), INPUT_ROW_TITLE)
        self.assertIsNone(general_input_action_target(
            ordinary_permission_nodes(input_pending=False, control_pending=True), self.name
        ))
        self.assertIsNone(general_input_action_target(
            ordinary_permission_nodes(input_pending=True, control_pending=True) + [
                node("AXWindow", self.name + " 首次设置", bounds=ax_bounds(10, 10, 400, 300))
            ], self.name
        ))

    def test_ordinary_complete_view_requires_two_rows_on_both_surfaces(self):
        nodes = ordinary_permission_nodes(input_pending=False, control_pending=False)
        self.assertTrue(ordinary_permission_guide_complete(nodes, self.name))
        self.assertFalse(ordinary_permission_guide_complete(nodes + [
            node("AXStaticText", value="已授权 2/2", bounds=ax_bounds(10, 500, 120, 20))
        ], self.name))
        self.assertFalse(ordinary_permission_guide_complete(
            [item for item in nodes if not (item["role"] == "AXStaticText" and item["bounds"]["x"] == 1260 and item["bounds"]["y"] == 220)],
            self.name,
        ))

    def test_ordinary_final_contract_keeps_settings_frontmost_or_exited_guards(self):
        nodes = ordinary_permission_nodes(input_pending=False, control_pending=False)
        state = {"app": 101, "settings": 202, "front": 202, "page": CONTROL_PAGES[0], "restart": False}
        self.assertTrue(existing_input_segment_granted(state, 101, 202, False, True, True, nodes, self.name))
        self.assertFalse(existing_input_segment_granted(
            {**state, "front": 101}, 101, 202, False, True, True, nodes, self.name
        ))
        self.assertFalse(existing_input_segment_granted(
            {"app": 101, "settings": 0, "front": 101, "restart": False},
            101, 202, False, True, False,
            ordinary_permission_nodes(input_pending=False, control_pending=True), self.name,
        ))


class ExistingInputGrantTests(unittest.TestCase):
    def setUp(self):
        self.name = "MiMac Dev"
        self.nodes = ordinary_permission_nodes(input_pending=False, control_pending=False)
        self.pending_nodes = ordinary_permission_nodes(input_pending=False, control_pending=True)
        self.first_use_nodes = [
            node("AXWindow", self.name + " 首次设置"),
            node("AXButton", "继续"),
            node("AXStaticText", value="全部已授权"),
        ]

    def test_original_process_grant_passes_after_settings_exits(self):
        state = {"app": 101, "settings": 0, "front": 101, "restart": False}
        self.assertTrue(existing_input_segment_granted(
            state, 101, 202, False, True, False, self.nodes, self.name
        ))

    def test_ordinary_settings_grant_keeps_settings_open_and_frontmost(self):
        state = {
            "app": 101, "settings": 202, "front": 202, "page": CONTROL_PAGES[0],
            "restart": False,
        }
        self.assertTrue(existing_input_segment_granted(
            state, 101, 202, False, True, True, self.nodes, self.name,
        ))
        self.assertFalse(existing_input_segment_granted(
            {**state, "front": 101}, 101, 202, False, True, True, self.nodes, self.name,
        ))

    def test_restart_path_requires_scoped_restart_and_old_process_exit(self):
        state = {"app": 303, "settings": 0, "front": 303, "restart": False}
        self.assertTrue(existing_input_segment_granted(
            state, 101, 202, True, False, False, self.nodes, self.name
        ))
        self.assertFalse(existing_input_segment_granted(
            state, 101, 202, False, False, False, self.nodes, self.name
        ))
        self.assertFalse(existing_input_segment_granted(
            state, 101, 202, True, True, False, self.nodes, self.name
        ))

    def test_both_paths_require_settings_exit_frontmost_dev_and_authorized_panel(self):
        direct = {"app": 101, "settings": 0, "front": 101, "restart": False}
        self.assertFalse(existing_input_segment_granted(
            {**direct, "settings": 202}, 101, 202, False, True, True, self.nodes, self.name
        ))
        self.assertFalse(existing_input_segment_granted(
            {**direct, "front": 202}, 101, 202, False, True, False, self.nodes, self.name
        ))
        self.assertFalse(existing_input_segment_granted(
            direct, 101, 202, False, True, False, self.nodes[:1], self.name
        ))

    def test_first_use_control_drop_accepts_continue_with_all_authorized(self):
        state = {"app": 101, "settings": 0, "front": 101, "restart": False}
        self.assertTrue(existing_input_segment_granted(
            state, 101, 202, False, True, False, self.first_use_nodes, self.name,
            full_first_use=True,
        ))

    def test_first_use_handoff_keeps_identity_focus_and_settings_exit_guards(self):
        state = {"app": 101, "settings": 0, "front": 101, "restart": False}
        self.assertFalse(existing_input_segment_granted(
            {**state, "front": 303}, 101, 202, False, True, False,
            self.first_use_nodes, self.name, full_first_use=True,
        ))
        self.assertFalse(existing_input_segment_granted(
            {**state, "settings": 202}, 101, 202, False, True, True,
            self.first_use_nodes, self.name, full_first_use=True,
        ))
        self.assertFalse(existing_input_segment_granted(
            {**state, "app": 303}, 101, 202, False, True, False,
            self.first_use_nodes, self.name, full_first_use=True,
        ))

    def test_first_use_path_does_not_accept_the_ordinary_settings_card(self):
        state = {"app": 101, "settings": 0, "front": 101, "restart": False}
        self.assertFalse(existing_input_segment_granted(
            state, 101, 202, False, True, False, self.nodes, self.name,
            full_first_use=True,
        ))


class FullFirstUsePermissionViewTests(unittest.TestCase):
    def setUp(self):
        self.name = "MiMac Dev"
        self.nodes = [
            node("AXWindow", self.name + " 首次设置"),
            node("AXButton", "继续"),
            node("AXStaticText", value="全部已授权"),
        ]

    def test_first_use_requires_explicit_all_authorized_summary(self):
        self.assertTrue(complete_permission_view(self.nodes, self.name))
        for summary in ("已授权", "已授权 1/2"):
            partial = [dict(item) for item in self.nodes]
            partial[-1]["value"] = summary
            self.assertFalse(complete_permission_view(partial, self.name))

    def test_first_use_wait_also_requires_expected_pid_focus_and_settings_exit(self):
        acceptance = object.__new__(Acceptance)
        acceptance.name = self.name
        acceptance.view = lambda _pid: self.nodes
        state = {"app": 303, "settings": 0, "front": 303, "restart": False}
        with patch("permission_acceptance.pid_exists", return_value=False):
            self.assertIs(acceptance.granted_after_restart(state, 101, 202, 303), state)
            self.assertIsNone(acceptance.granted_after_restart(
                {**state, "front": 202}, 101, 202, 303
            ))
            self.assertIsNone(acceptance.granted_after_restart(
                {**state, "app": 304, "front": 304}, 101, 202, 303
            ))
            self.assertIsNone(acceptance.granted_after_restart(
                {**state, "settings": 202}, 101, 202, 303
            ))
        for live_pid in (101, 202):
            with patch("permission_acceptance.pid_exists", side_effect=lambda pid: pid == live_pid):
                self.assertIsNone(acceptance.granted_after_restart(state, 101, 202, 303))


class DraggableGeometryTests(unittest.TestCase):
    def setUp(self):
        self.app_pid = 101
        self.settings_pid = 202
        self.page = "Device Control and Data Access"
        self.sample = {
            "dev_pids": [self.app_pid],
            "settings_pid": self.settings_pid,
            # NSWorkspace's observer value may be stale; drag readiness uses the fresh AX probe.
            "frontmost_pid": 1239,
            "permission_card_visible": True,
            "permission_card_title_match": True,
            "settings_page_title_matches": [{
                "title": self.page,
                "bounds": [780, 144, 740, 625],
            }],
            "attachment": {
                "known": True, "attached": True, "horizontal_gap": 16,
                "top_delta": 0,
                "permission_card_bounds": [264, 144, 500, 359],
                "settings_bounds": [780, 144, 740, 625],
            },
        }

    def test_geometry_gate_uses_same_windows_and_attached_rectangles_not_stale_frontmost(self):
        self.assertTrue(draggable_window_geometry_ready(
            self.sample, self.app_pid, self.settings_pid
        ))
        variants = []
        wrong_app = dict(self.sample, dev_pids=[303])
        variants.append(wrong_app)
        wrong_settings = dict(self.sample, settings_pid=303)
        variants.append(wrong_settings)
        no_card = dict(self.sample, permission_card_visible=False)
        variants.append(no_card)
        for key, value in (("known", False), ("attached", False)):
            changed = dict(self.sample)
            changed["attachment"] = dict(self.sample["attachment"], **{key: value})
            variants.append(changed)
        for sample in variants:
            self.assertFalse(draggable_window_geometry_ready(
                sample, self.app_pid, self.settings_pid
            ))

    def test_window_observer_page_label_does_not_replace_fresh_page_probe(self):
        stale_page = dict(self.sample, settings_page_title_matches=[{"title": "Input Monitoring"}])
        self.assertTrue(draggable_window_geometry_ready(
            stale_page, self.app_pid, self.settings_pid
        ))
        ready_probe = {
            "app": self.app_pid, "settings": self.settings_pid, "front": self.settings_pid,
            "page": self.page, "password": False, "system_sheet": False, "restart": False,
            "permission_card_visible": True,
        }
        self.assertTrue(drag_page_probe_ready(
            ready_probe, self.app_pid, self.settings_pid, (self.page,)
        ))

    def test_native_drag_waits_for_attached_geometry_then_fresh_settings_probe(self):
        instance = object.__new__(Acceptance)
        instance.calls = []
        instance.records = []
        instance.latest_window_observation = lambda: {
            "sample_completed_at": 123.0, "state": self.sample,
        }
        fresh = {
            "app": self.app_pid, "settings": self.settings_pid, "front": self.settings_pid,
            "page": self.page, "password": False, "system_sheet": False, "restart": False,
            "permission_card_visible": True,
        }

        def probe(operation="probe"):
            instance.calls.append(operation)
            if operation == "probe":
                return fresh
            return {"performed": operation, "source_kind": "panel-center"}

        instance.probe = probe
        instance.record = lambda stage, values: instance.records.append((stage, values))
        result = instance.drag(
            "drag-control", permission="device-control", app_pid=self.app_pid,
            settings_pid=self.settings_pid, pages=(self.page,),
        )
        self.assertEqual(instance.calls, ["probe", "drag-control"])
        self.assertEqual(result["performed"], "drag-control")
        self.assertEqual([stage for stage, _ in instance.records], ["drag_geometry_ready"])
        event = instance.records[0][1]
        self.assertEqual(event["window_sample_completed_at"], 123.0)
        self.assertEqual(event["fresh_probe"]["front"], self.settings_pid)
        self.assertLessEqual(event["fresh_probe_started_at"], event["fresh_probe_completed_at"])

    def test_unattached_geometry_blocks_before_native_driver_call(self):
        instance = object.__new__(Acceptance)
        instance.calls = []
        instance.records = []
        instance.latest_window_observation = lambda: None
        instance.probe = lambda operation="probe": instance.calls.append(operation)
        instance.record = lambda stage, values: instance.records.append((stage, values))
        with patch("permission_acceptance.DRAG_GEOMETRY_TIMEOUT", 0):
            with self.assertRaisesRegex(RuntimeError, "timed out waiting for the attached permission card"):
                instance.drag(
                    "drag-control", permission="device-control", app_pid=self.app_pid,
                    settings_pid=self.settings_pid, pages=(self.page,),
                )
        self.assertEqual(instance.calls, ["probe"])
        self.assertEqual(instance.records[0][0], "drag_geometry_blocked")

    def test_fresh_pre_gesture_probe_must_confirm_same_app_and_settings_frontmost(self):
        ready = {
            "app": self.app_pid, "settings": self.settings_pid, "front": self.settings_pid,
            "page": self.page, "password": False, "system_sheet": False, "restart": False,
            "permission_card_visible": True,
        }
        self.assertTrue(drag_page_probe_ready(
            ready, self.app_pid, self.settings_pid, (self.page,)
        ))
        self.assertFalse(drag_page_probe_ready(
            dict(ready, front=1239), self.app_pid, self.settings_pid, (self.page,)
        ))
        self.assertFalse(drag_page_probe_ready(
            dict(ready, app=303), self.app_pid, self.settings_pid, (self.page,)
        ))
        self.assertFalse(drag_page_probe_ready(
            dict(ready, page="Input Monitoring"), self.app_pid, self.settings_pid, (self.page,)
        ))


class NativeDragRegressionTests(unittest.TestCase):
    def test_drag_only_preflight_accepts_readable_partial_authorization_snapshot(self):
        self.assertFalse(drag_regressions_snapshot_ready({"permission_card_visible": False, "permission_summary": ["已授权 1/2"]}))
        self.assertFalse(drag_regressions_snapshot_ready({"permission_card_visible": True, "permission_summary": []}))
        self.assertTrue(drag_regressions_snapshot_ready({"permission_card_visible": True, "permission_summary": ["已授权 1/2"]}))
        self.assertTrue(drag_regressions_snapshot_ready({"permission_card_visible": True, "permission_summary": ["未授权 0/2"]}))
        self.assertTrue(drag_regressions_snapshot_ready({"permission_card_visible": True, "permission_summary": ["已授权 2/2"]}))
        self.assertTrue(drag_regressions_snapshot_ready({"permission_card_visible": True, "permission_summary": ["全部已授权"]}))

    def acceptance(self, outcomes):
        instance = object.__new__(Acceptance)
        instance.calls = []
        instance.records = []

        def drag(operation):
            instance.calls.append(operation)
            return outcomes[operation]

        instance.drag = drag
        instance.record = lambda stage, values: instance.records.append((stage, values))
        return instance

    @staticmethod
    def same_permission_snapshot():
        return {
            "permission_state_before": {"summary": ["已授权 1/2"], "dev_controls": [{"value": "0"}]},
            "permission_state_after": {"summary": ["已授权 1/2"], "dev_controls": [{"value": "0"}]},
        }

    def test_runs_escape_and_wrong_target_before_reporting_success(self):
        instance = self.acceptance({
            "drag-cancel-escape": {"permission_state_unchanged": True,
                                   **self.same_permission_snapshot(),
                                   "source_kind": "ax-labelled-icon", "icon_hit_test": True,
                                   "drag_started_overlay_observed": True, "drag_ended_overlay_removed": True,
                                   "permission_target_reached_while_overlay_visible": True},
            "drag-wrong-target": {"permission_state_unchanged": True,
                                  **self.same_permission_snapshot(),
                                  "source_kind": "validated-centered-image-fallback", "icon_hit_test": True,
                                  "drag_started_overlay_observed": True, "drag_ended_overlay_removed": True,
                                  "target_kind": "unpermissioned-settings-title", "target_role": "AXStaticText"},
        })
        instance.drag_regressions()
        self.assertEqual(instance.calls, ["drag-cancel-escape", "drag-wrong-target"])
        self.assertEqual([stage for stage, _ in instance.records], [
            "native_drag_regression", "native_drag_regression"
        ])

    def test_stops_when_a_noop_drag_does_not_prove_unchanged_permission_state(self):
        instance = self.acceptance({
            "drag-cancel-escape": {"permission_state_unchanged": True,
                                   **self.same_permission_snapshot(),
                                   "source_kind": "ax-labelled-icon", "icon_hit_test": True,
                                   "drag_started_overlay_observed": True, "drag_ended_overlay_removed": True,
                                   "permission_target_reached_while_overlay_visible": True},
            "drag-wrong-target": {"permission_state_unchanged": False,
                                  **self.same_permission_snapshot(),
                                  "source_kind": "ax-labelled-icon", "icon_hit_test": True,
                                  "drag_started_overlay_observed": True, "drag_ended_overlay_removed": True,
                                  "target_kind": "unpermissioned-settings-title", "target_role": "AXStaticText"},
        })
        with self.assertRaisesRegex(RuntimeError, "drag-wrong-target did not prove"):
            instance.drag_regressions()
        self.assertEqual(instance.calls, ["drag-cancel-escape", "drag-wrong-target"])
        self.assertEqual(instance.records[-1][0], "native_drag_regression_failed")

    def test_requires_equal_before_and_after_permission_snapshots(self):
        changed = self.same_permission_snapshot()
        changed["permission_state_after"] = {
            "summary": ["已授权 2/2"], "dev_controls": [{"value": "1"}],
        }
        instance = self.acceptance({
            "drag-cancel-escape": {
                **changed, "permission_state_unchanged": True,
                "source_kind": "ax-labelled-icon", "icon_hit_test": True,
                "drag_started_overlay_observed": True, "drag_ended_overlay_removed": True,
                "permission_target_reached_while_overlay_visible": True,
            },
            "drag-wrong-target": {},
        })
        with self.assertRaisesRegex(RuntimeError, "drag-cancel-escape did not prove"):
            instance.drag_regressions()
        self.assertEqual(instance.calls, ["drag-cancel-escape"])

    def test_records_driver_errors_and_stops_before_the_real_drop(self):
        instance = object.__new__(Acceptance)
        instance.records = []
        instance.drag = lambda operation: (_ for _ in ()).throw(RuntimeError({"error": "unsafe target"}))
        instance.record = lambda stage, values: instance.records.append((stage, values))
        with self.assertRaises(RuntimeError):
            instance.drag_regressions()
        self.assertEqual(instance.records[0][0], "native_drag_regression_failed")
        self.assertEqual(instance.records[0][1]["case"], "drag-cancel-escape")

    def test_successful_permission_drop_keeps_the_existing_center_origin(self):
        instance = object.__new__(Acceptance)
        instance.probe = lambda _operation: {
            "performed": "drag-control", "source_kind": "panel-center",
        }
        self.assertEqual(instance.drag("drag-control")["source_kind"], "panel-center")
        instance.probe = lambda _operation: {"performed": "drag-control"}
        self.assertEqual(instance.drag("drag-control"), {"performed": "drag-control"})


class ResumeInputDropTests(unittest.TestCase):
    def setUp(self):
        self.identity = {
            "bundle": "/Users/test/Library/Caches/MiMac/MiMac Verify.app",
            "bundle_id": "dev.mimac.verification.test",
            "sha256": "a" * 64,
        }
        self.entries = [
            {"stage": "identity", **self.identity, "mode": "full-first-use"},
            {"stage": "app_button", "app": 101, "panel_title": "MiMac Dev 系统权限",
             "row_title": INPUT_ROW_TITLE, "action": "授权…", "scope": "permissions_panel_and_row",
             "ax_press_started_at": 10.0, "ax_press_completed_at": 10.1},
            {"stage": "permission_row_action", "app": 101, "permission": "input-monitoring",
             "source": "right-side 授权…", "press_started_at": 10.0, "press_completed_at": 10.1},
            {"stage": "input_ready", "app": 101, "settings": 202},
            {"stage": "native_drop", "performed": "drag-input", "app": 101, "settings": 202},
            {"stage": "NEEDS_PASSWORD", "permission": "input-monitoring"},
        ]

    def test_resumes_from_the_recorded_drop(self):
        self.assertEqual(resume_input_drop_args(self.entries, self.identity), (101, 202))

    def test_accepts_legacy_row_action_when_exact_ax_press_pair_proves_pid_and_target(self):
        legacy = [dict(entry) for entry in self.entries]
        row_action = next(entry for entry in legacy if entry.get("stage") == "permission_row_action")
        row_action.pop("app")
        self.assertEqual(resume_input_drop_args(legacy, self.identity), (101, 202))

    def test_rejects_missing_drop_or_mismatched_identity(self):
        self.assertRaises(RuntimeError, resume_input_drop_args, self.entries[:-2], self.identity)
        wrong = {**self.identity, "sha256": "b" * 64}
        self.assertRaises(RuntimeError, resume_input_drop_args, self.entries, wrong)

    def test_rejects_a_run_that_has_reached_device_control(self):
        progressed = self.entries + [{"stage": "permission_row_action", "permission": "device-control"}]
        self.assertRaises(RuntimeError, resume_input_drop_args, progressed, self.identity)


class AuthWaitTests(unittest.TestCase):
    @staticmethod
    def acceptance(timeout, probes, sample_times=None):
        from permission_acceptance import Acceptance

        instance = object.__new__(Acceptance)
        instance.auth_timeout = timeout
        instance.events = []
        instance.clock = [0.0]
        iterator = iter(probes)
        times = iter(sample_times) if sample_times is not None else None

        def probe():
            if times is not None:
                instance.clock[0] = next(times)
            return next(iterator)

        instance.probe = probe
        instance.record = lambda stage, values: instance.events.append((stage, values))
        return instance

    def run_with_clock(self, instance, stage, ready):
        with patch("permission_acceptance.time.monotonic", side_effect=lambda: instance.clock[0]), \
             patch("permission_acceptance.time.sleep"):
            return instance.wait_auth_or(stage, ready)

    def test_zero_timeout_waits_until_auth_ui_closes_then_times_out_if_cancelled(self):
        states = [
            {"password": True, "system_sheet": False},
            {"password": True, "system_sheet": False},
            {"password": False, "system_sheet": False},
            {"password": False, "system_sheet": False},
            {"password": False, "system_sheet": False},
        ]
        instance = self.acceptance(0, states, [1.0, 13.0, 14.0, 25.0, 26.1])
        with self.assertRaisesRegex(RuntimeError, "expected system confirmation did not arrive"):
            self.run_with_clock(instance, "input-monitoring", lambda _: None)
        self.assertEqual([stage for stage, _ in instance.events], [
            "AUTH_STATE_CHANGED", "NEEDS_PASSWORD", "AUTH_STATE_CHANGED", "AUTH_WAIT_TIMEOUT",
        ])
        self.assertFalse(instance.events[-1][1]["last_state"]["password_prompt"])

    def test_finite_timeout_resets_for_each_reopened_password_prompt(self):
        states = [
            {"password": True, "system_sheet": False},
            {"password": False, "system_sheet": False},
            {"password": True, "system_sheet": False},
            {"password": False, "system_sheet": False},
            {"password": False, "system_sheet": False},
        ]
        instance = self.acceptance(10, states, [1.0, 4.0, 4.1, 12.0, 12.1])
        result = self.run_with_clock(instance, "input-monitoring", lambda state: "ready" if not state["password"] and instance.clock[0] > 12 else None)
        self.assertEqual(result, "ready")
        self.assertEqual([stage for stage, _ in instance.events].count("NEEDS_PASSWORD"), 2)

    def test_finite_timeout_resets_for_reentered_regular_settings_sheet(self):
        states = [
            {"password": False, "system_sheet": True},
            {"password": False, "system_sheet": False},
            {"password": False, "system_sheet": True},
            {"password": False, "system_sheet": False},
            {"password": False, "system_sheet": False},
        ]
        instance = self.acceptance(5, states, [1.0, 2.0, 13.0, 17.0, 17.1])
        result = self.run_with_clock(instance, "input-monitoring", lambda state: "ready" if not state["system_sheet"] and instance.clock[0] > 17 else None)
        self.assertEqual(result, "ready")
        self.assertEqual([stage for stage, _ in instance.events].count("SYSTEM_SHEET_WAIT"), 2)

    def test_finite_timeout_uses_existing_post_cancel_feedback_window(self):
        states = [
            {"password": True, "system_sheet": False},
            {"password": False, "system_sheet": False},
            {"password": False, "system_sheet": False},
            {"password": False, "system_sheet": False},
        ]
        instance = self.acceptance(120, states, [1.0, 2.0, 13.0, 14.1])
        with self.assertRaisesRegex(RuntimeError, "expected system confirmation did not arrive"):
            self.run_with_clock(instance, "input-monitoring", lambda _: None)
        self.assertEqual(instance.events[-1][0], "AUTH_WAIT_TIMEOUT")

    def test_system_sheet_blocks_ready_until_dismissed(self):
        states = [
            {"password": False, "system_sheet": True, "settings": 202,
             "page": "Input Monitoring", "system_sheet_titles": ["Allow access"]},
            {"password": False, "system_sheet": False},
        ]
        instance = self.acceptance(120, states, [1.0, 2.0])
        ready_states = []
        result = self.run_with_clock(
            instance, "input-monitoring", lambda state: ready_states.append(state) or "ready"
        )
        self.assertEqual(result, "ready")
        self.assertEqual(len(ready_states), 1)
        self.assertEqual([stage for stage, _ in instance.events], [
            "AUTH_STATE_CHANGED", "SYSTEM_SHEET_WAIT", "AUTH_STATE_CHANGED",
        ])
        self.assertNotIn("sheet_titles", instance.events[1][1])

    def test_app_scoped_restart_sheet_reaches_its_ready_handler(self):
        state = {"password": False, "system_sheet": True, "restart": True}
        instance = self.acceptance(0, [state], [1.0])
        result = self.run_with_clock(
            instance, "input-monitoring", lambda probe: probe if probe["restart"] else None
        )
        self.assertIs(result, state)
        self.assertEqual([stage for stage, _ in instance.events], ["AUTH_STATE_CHANGED"])

    def test_timeout_records_only_changed_redacted_state_and_last_observation(self):
        state_before = {
            "app": 101, "settings": 202, "front": 202, "restart": False,
            "system_sheet": True, "system_sheet_titles": ["Private System Sheet"],
            "password": False, "page": "Private Settings Page",
        }
        state_after = {
            "app": 101, "settings": 0, "front": 101, "restart": False,
            "system_sheet": False, "system_sheet_titles": [],
            "password": False, "page": "Private Settings Page",
        }
        instance = self.acceptance(
            0, [state_before, dict(state_before), state_after, dict(state_after)],
            [1.0, 1.5, 2.0, 14.0],
        )
        with self.assertRaisesRegex(RuntimeError, "expected system confirmation did not arrive"):
            self.run_with_clock(instance, "device-control", lambda _: None)

        self.assertEqual([stage for stage, _ in instance.events], [
            "AUTH_STATE_CHANGED", "SYSTEM_SHEET_WAIT", "AUTH_STATE_CHANGED", "AUTH_WAIT_TIMEOUT",
        ])
        final_state = {
            "app_present": True, "settings_present": False, "front": "app",
            "restart_prompt": False, "security_popup": False, "password_prompt": False,
        }
        self.assertEqual(instance.events[-2][1]["state"], final_state)
        self.assertEqual(instance.events[-1][1]["last_state"], final_state)
        for _, values in instance.events:
            encoded = json.dumps(values)
            self.assertNotIn("101", encoded)
            self.assertNotIn("202", encoded)
            self.assertNotIn("Private", encoded)


if __name__ == "__main__":
    unittest.main()
