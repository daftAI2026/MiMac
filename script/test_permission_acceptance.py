import json
from pathlib import Path
import unittest
from unittest.mock import patch

from permission_acceptance import (
    Acceptance,
    CONTROL_PAGES,
    CONTROL_ROW_TITLES,
    INPUT_PAGES,
    BLUETOOTH_ROW_TITLES,
    GENERAL_BLUETOOTH_MANAGE,
    INPUT_ROW_TITLE,
    existing_input_target,
    first_use_input_target,
    first_use_existing_input_target,
    first_use_progress_summary,
    existing_input_segment_granted,
    ordinary_existing_input_target,
    ordinary_general_nodes,
    ordinary_general_surface_ready,
    ordinary_permission_surfaces,
    ordinary_permission_counts,
    cancel_reentry_permission_counts,
    permission_rows_unchanged,
    non_target_permission_rows_unchanged,
    verified_cancel_result,
    ordinary_permission_guide_complete,
    ordinary_permission_guide_card_present,
    general_input_action_target,
    ordinary_general_startup_ready,
    is_transient_ax_windows_error,
    drag_regressions_snapshot_ready,
    complete_permission_view,
    draggable_window_geometry_ready,
    drag_page_probe_ready,
    resume_input_drop_args,
    restart_prompt_focus_ready,
    resumed_settings_focus_ready,
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


def ordinary_permission_nodes(*, input_pending=False, control_pending=False,
                              bluetooth=False, bluetooth_pending=False):
    name = "MiMac Dev"
    nodes = [
        node("AXWindow", name, bounds=ax_bounds(0, 0, 1000, 700)),
        node("AXButton", "通用", bounds=ax_bounds(20, 100)),
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
    if bluetooth:
        nodes.extend([
            node("AXButton", description=GENERAL_BLUETOOTH_MANAGE[0], bounds=ax_bounds(600, 330)),
            node("AXButton", description=BLUETOOTH_ROW_TITLES[0], bounds=ax_bounds(1040, 280, 220, 22)),
        ])
        if bluetooth_pending:
            nodes.append(node("AXButton", "授权…", bounds=ax_bounds(1350, 280, 110, 22)))
        else:
            nodes.extend([
                node("AXStaticText", value="已授权", bounds=ax_bounds(440, 330, 100, 20)),
                node("AXStaticText", value="已授权", bounds=ax_bounds(1260, 280, 90, 22)),
            ])
    return nodes


def first_use_permission_nodes(*, input_pending=False, bluetooth=False,
                               bluetooth_pending=False, summary=None):
    name = "MiMac Dev"
    rows = [
        (INPUT_ROW_TITLE, 120, "pending" if input_pending else "granted"),
        (CONTROL_ROW_TITLES[0], 180, "pending"),
    ]
    if bluetooth:
        rows.append((BLUETOOTH_ROW_TITLES[0], 240, "pending" if bluetooth_pending else "granted"))
    granted_count = sum(state == "granted" for _, _, state in rows)
    total = len(rows)
    summary = summary or ("尚未授权" if granted_count == 0 else f"已授权 {granted_count}/{total}")
    nodes = [
        node("AXWindow", name + " 首次设置", bounds=ax_bounds(0, 0, 480, 340)),
        node("AXWindow", name + " 系统权限", bounds=ax_bounds(500, 0, 500, 340)),
        node("AXButton", "继续", bounds=ax_bounds(100, 20)),
        node("AXStaticText", value=summary, bounds=ax_bounds(100, 70, 140, 20)),
    ]
    for title, y, state in rows:
        nodes.append(node("AXButton", description=title, bounds=ax_bounds(520, y, 240, 22)))
        if state == "granted":
            nodes.append(node("AXStaticText", value="已授权", bounds=ax_bounds(780, y, 90, 22)))
        else:
            nodes.append(node("AXButton", "授权…", bounds=ax_bounds(880, y, 100, 22)))
    return nodes


class ExistingInputTargetTests(unittest.TestCase):
    def setUp(self):
        self.name = "MiMac Dev"
        self.control = CONTROL_ROW_TITLES[0]
        self.nodes = first_use_permission_nodes(bluetooth=True)

    def test_selects_only_pending_device_control_segment(self):
        self.assertEqual(existing_input_target(self.nodes, self.name), self.control)

    def test_rejects_another_permission_count(self):
        wrong_denominator = [dict(item) for item in self.nodes]
        next(item for item in wrong_denominator if item.get("value") == "已授权 2/3")["value"] = "已授权 2/2"
        wrong_numerator = [dict(item) for item in self.nodes]
        next(item for item in wrong_numerator if item.get("value") == "已授权 2/3")["value"] = "已授权 1/3"
        self.assertIsNone(existing_input_target(wrong_denominator, self.name))
        self.assertIsNone(existing_input_target(wrong_numerator, self.name))

    def test_first_use_accepts_actual_two_row_legacy_contract(self):
        nodes = first_use_permission_nodes()
        self.assertEqual(first_use_existing_input_target(nodes, self.name), self.control)

    def test_first_use_three_row_contract_requires_bluetooth_to_be_granted(self):
        nodes = first_use_permission_nodes(bluetooth=True)
        self.assertEqual(existing_input_target(nodes, self.name), self.control)
        self.assertEqual(first_use_existing_input_target(nodes, self.name), self.control)
        bluetooth_pending = first_use_permission_nodes(bluetooth=True, bluetooth_pending=True)
        self.assertIsNone(existing_input_target(bluetooth_pending, self.name))
        self.assertIsNone(first_use_existing_input_target(bluetooth_pending, self.name))

    def test_full_first_use_preflight_accepts_two_or_three_actual_rows(self):
        self.assertEqual(first_use_input_target(
            first_use_permission_nodes(input_pending=True), self.name
        ), INPUT_ROW_TITLE)
        three_rows = first_use_permission_nodes(input_pending=True, bluetooth=True)
        self.assertEqual(first_use_input_target(three_rows, self.name), INPUT_ROW_TITLE)
        self.assertEqual(first_use_progress_summary(three_rows, 3), (1, 3, "已授权 1/3"))
        bluetooth_pending = first_use_permission_nodes(
            input_pending=True, bluetooth=True, bluetooth_pending=True
        )
        self.assertIsNone(first_use_input_target(bluetooth_pending, self.name))

    def test_rejects_missing_input_row_or_permission_panel(self):
        self.assertIsNone(existing_input_target(self.nodes[2:], self.name))
        nodes = [item for item in self.nodes
                 if INPUT_ROW_TITLE not in (item.get("title"), item.get("description"))]
        self.assertIsNone(existing_input_target(nodes, self.name))

    def test_existing_input_wait_predicate_requires_the_actual_permission_panel(self):
        acceptance = object.__new__(Acceptance)
        acceptance.name = self.name
        acceptance.view = lambda _pid: self.nodes[:1]
        self.assertIsNone(acceptance.existing_input_panel(101))
        acceptance.view = lambda _pid: self.nodes
        self.assertEqual(acceptance.existing_input_panel(101), self.nodes)


class OrdinaryGeneralPermissionViewTests(unittest.TestCase):
    def setUp(self):
        self.name = "MiMac Dev"

    def test_general_startup_waits_for_real_main_window_and_visible_navigation_button(self):
        self.assertFalse(ordinary_general_startup_ready(10892, [], self.name))
        no_window = [node("AXButton", "通用", bounds=ax_bounds(20, 100))]
        self.assertFalse(ordinary_general_startup_ready(10892, no_window, self.name))
        no_button = [node("AXWindow", self.name, bounds=ax_bounds(0, 0, 1000, 700))]
        self.assertFalse(ordinary_general_startup_ready(10892, no_button, self.name))
        outside_window = [
            node("AXWindow", self.name, bounds=ax_bounds(0, 0, 1000, 700)),
            node("AXButton", "通用", bounds=ax_bounds(1200, 100)),
        ]
        self.assertFalse(ordinary_general_startup_ready(10892, outside_window, self.name))

    def test_startup_readiness_does_not_require_launch_pid_to_be_frontmost(self):
        nodes = [
            node("AXWindow", self.name, bounds=ax_bounds(0, 0, 1000, 700)),
            node("AXButton", "通用", bounds=ax_bounds(20, 100)),
        ]
        # The launch probe may report a stale or transitional foreground PID;
        # the ordinary-only path waits for these real AX targets instead.
        observed = {"app": 10892, "front": 1239}
        self.assertNotEqual(observed["app"], observed["front"])
        self.assertTrue(ordinary_general_startup_ready(observed["app"], nodes, self.name))

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
        duplicate_row_status = [dict(item) for item in nodes] + [
            node("AXStaticText", value="已授权", bounds=ax_bounds(500, 210, 100, 20))
        ]
        self.assertIsNone(existing_input_target(wrong_row, self.name))
        self.assertIsNone(existing_input_target(extra_action, self.name))
        self.assertIsNone(existing_input_target(duplicate_row_status, self.name))

    def test_saved_after_reopen_ax_targets_the_pending_device_control_row(self):
        fixture = Path(__file__).parent / "fixtures" / "ordinary-general-after-reopen-ax.json"
        nodes = json.loads(fixture.read_text(encoding="utf-8"))
        windows = {item.get("title"): item for item in nodes if item.get("role") == "AXWindow"}
        self.assertIn(self.name + " 权限拖放引导", windows)
        general_bounds = windows[self.name]["bounds"]
        card_bounds = windows[self.name + " 系统权限"]["bounds"]
        card_input_grant = next(
            item for item in nodes if item.get("role") == "AXStaticText" and item.get("value") == "已授权"
            and item["bounds"]["x"] < card_bounds["x"] + card_bounds["width"]
        )
        grant_bounds = card_input_grant["bounds"]
        center_x = grant_bounds["x"] + grant_bounds["width"] / 2
        center_y = grant_bounds["y"] + grant_bounds["height"] / 2
        self.assertTrue(general_bounds["x"] <= center_x <= general_bounds["x"] + general_bounds["width"])
        self.assertTrue(general_bounds["y"] <= center_y <= general_bounds["y"] + general_bounds["height"])
        self.assertIsNotNone(ordinary_general_nodes(nodes, self.name))
        self.assertIsNotNone(ordinary_permission_surfaces(nodes, self.name))
        self.assertEqual(existing_input_target(nodes, self.name), CONTROL_ROW_TITLES[0])

    def test_three_row_overlapping_ax_statuses_use_root_window_ownership(self):
        fixture = Path(__file__).parent / "fixtures" / "ordinary-general-three-row-overlap-ax.json"
        nodes = json.loads(fixture.read_text(encoding="utf-8"))
        surfaces = ordinary_permission_surfaces(nodes, self.name)
        self.assertIsNotNone(surfaces)
        general, _ = ordinary_general_nodes(nodes, self.name)
        self.assertTrue(all(item.get("root_window_index") == 1 for item in general))
        self.assertFalse(any(item.get("root_window_index") == 0 for item in general))
        self.assertEqual(general_input_action_target(nodes, self.name), INPUT_ROW_TITLE)
        self.assertEqual(ordinary_permission_counts(nodes, self.name), {
            "row_count": 3,
            "granted_row_count": 1,
            "rows": {"bluetooth": "granted", "device_control": "pending", "input": "pending"},
        })

        legacy_nodes = [
            {key: value for key, value in item.items()
             if key not in ("root_window_index", "root_window_title")}
            for item in nodes
        ]
        self.assertIsNotNone(ordinary_permission_surfaces(legacy_nodes, self.name))
        self.assertIsNone(ordinary_permission_counts(legacy_nodes, self.name))

    def test_overlapping_panel_status_does_not_count_as_a_general_row_status(self):
        nodes = ordinary_permission_nodes(input_pending=False, control_pending=True)
        # This label is in the General window's bounding box but not aligned to
        # either General permission row, as with the overlapping permission card.
        nodes.append(node("AXStaticText", value="已授权", bounds=ax_bounds(700, 380, 36, 14)))
        self.assertEqual(ordinary_existing_input_target(nodes, self.name), CONTROL_ROW_TITLES[0])

    def test_cold_general_flow_accepts_input_pending_with_control_pending_or_granted(self):
        both_pending = ordinary_permission_nodes(input_pending=True, control_pending=True)
        control_granted = ordinary_permission_nodes(input_pending=True, control_pending=False)
        self.assertEqual(general_input_action_target(both_pending, self.name), INPUT_ROW_TITLE)
        self.assertEqual(general_input_action_target(control_granted, self.name), INPUT_ROW_TITLE)
        overlapping_control_status = control_granted + [
            node("AXStaticText", value="已授权", bounds=ax_bounds(700, 380, 36, 14))
        ]
        self.assertEqual(general_input_action_target(overlapping_control_status, self.name), INPUT_ROW_TITLE)
        self.assertIsNone(general_input_action_target(
            ordinary_permission_nodes(input_pending=False, control_pending=True), self.name
        ))
        self.assertIsNone(general_input_action_target(
            ordinary_permission_nodes(input_pending=True, control_pending=True) + [
                node("AXWindow", self.name + " 首次设置", bounds=ax_bounds(10, 10, 400, 300))
            ], self.name
        ))

    def test_cold_general_flow_accepts_three_rows_when_bluetooth_is_already_granted(self):
        both_pending = ordinary_permission_nodes(
            input_pending=True, control_pending=True, bluetooth=True
        )
        self.assertEqual(general_input_action_target(both_pending, self.name), INPUT_ROW_TITLE)
        self.assertEqual(ordinary_permission_counts(both_pending, self.name), {
            "row_count": 3,
            "granted_row_count": 1,
            "rows": {"bluetooth": "granted", "device_control": "pending", "input": "pending"},
        })
        control_already_granted = ordinary_permission_nodes(
            input_pending=True, control_pending=False, bluetooth=True
        )
        self.assertEqual(general_input_action_target(control_already_granted, self.name), INPUT_ROW_TITLE)

        input_granted = ordinary_permission_nodes(
            input_pending=False, control_pending=True, bluetooth=True
        )
        self.assertEqual(ordinary_existing_input_target(input_granted, self.name), CONTROL_ROW_TITLES[0])
        self.assertEqual(existing_input_target(input_granted, self.name), CONTROL_ROW_TITLES[0])
        self.assertEqual(ordinary_permission_counts(input_granted, self.name)["granted_row_count"], 2)

    def test_preflight_log_reads_device_control_pending_state_independently_of_input(self):
        scenarios = (
            (ordinary_permission_nodes(input_pending=True, control_pending=True), True, "pending"),
            (ordinary_permission_nodes(input_pending=True, control_pending=False), False, "granted"),
            (ordinary_permission_nodes(input_pending=True, control_pending=True, bluetooth=True), True, "pending"),
        )
        for nodes, expected_pending, expected_state in scenarios:
            instance = object.__new__(Acceptance)
            instance.name = self.name
            instance.records = []
            instance.preflight_no_existing_sheet = lambda _scope: {
                "app": 101, "settings": 202, "front": 202,
            }
            instance.view = lambda _pid, current=nodes: current
            instance.record = lambda stage, values: instance.records.append((stage, values))
            instance.press_permission_row = lambda *_args, **_kwargs: (_ for _ in ()).throw(
                RuntimeError("stop after preflight")
            )
            with self.assertRaisesRegex(RuntimeError, "stop after preflight"):
                instance.run_general_permissions()
            preflight = next(values for stage, values in instance.records
                             if stage == "general_permissions_preflight")
            self.assertEqual(preflight["device_control_pending"], expected_pending)
            self.assertEqual(preflight["permission_rows"]["rows"]["device_control"], expected_state)

    def test_ungranted_extra_bluetooth_row_blocks_both_ordinary_targets(self):
        bluetooth_pending = ordinary_permission_nodes(
            input_pending=True, control_pending=True, bluetooth=True, bluetooth_pending=True
        )
        self.assertIsNone(general_input_action_target(bluetooth_pending, self.name))
        input_already_granted = ordinary_permission_nodes(
            input_pending=False, control_pending=True, bluetooth=True, bluetooth_pending=True
        )
        self.assertIsNone(ordinary_existing_input_target(input_already_granted, self.name))
        self.assertIsNone(existing_input_target(input_already_granted, self.name))

    def test_three_row_surfaces_must_match_and_unknown_route_rows_are_rejected(self):
        nodes = ordinary_permission_nodes(
            input_pending=True, control_pending=True, bluetooth=True
        )
        missing_general_row = [item for item in nodes if item.get("description") != GENERAL_BLUETOOTH_MANAGE[0]]
        missing_card_row = [item for item in nodes if item.get("description") != BLUETOOTH_ROW_TITLES[0]]
        unknown_route = nodes + [
            node("AXButton", description="在系统设置中管理未知权限", bounds=ax_bounds(1040, 340, 220, 22))
        ]
        self.assertIsNone(ordinary_permission_surfaces(missing_general_row, self.name))
        self.assertIsNone(ordinary_permission_surfaces(missing_card_row, self.name))
        self.assertIsNone(ordinary_permission_surfaces(unknown_route, self.name))

    def test_ordinary_complete_view_requires_every_actual_row_on_both_surfaces(self):
        nodes = ordinary_permission_nodes(input_pending=False, control_pending=False)
        self.assertTrue(ordinary_permission_guide_complete(nodes, self.name))
        overlapping_card_status = nodes + [
            node("AXStaticText", value="已授权", bounds=ax_bounds(700, 380, 36, 14))
        ]
        self.assertTrue(ordinary_permission_guide_complete(overlapping_card_status, self.name))
        duplicate_row_status = nodes + [
            node("AXStaticText", value="已授权", bounds=ax_bounds(500, 210, 100, 20))
        ]
        self.assertFalse(ordinary_permission_guide_complete(duplicate_row_status, self.name))
        self.assertFalse(ordinary_permission_guide_complete(nodes + [
            node("AXWindow", self.name + " 首次设置", bounds=ax_bounds(10, 500, 400, 200))
        ], self.name))
        self.assertFalse(ordinary_permission_guide_complete(
            [item for item in nodes if not (item["role"] == "AXStaticText" and item["bounds"]["x"] == 1260 and item["bounds"]["y"] == 220)],
            self.name,
        ))

    def test_ordinary_completion_counts_and_verifies_all_three_authorized_rows(self):
        nodes = ordinary_permission_nodes(
            input_pending=False, control_pending=False, bluetooth=True
        )
        self.assertTrue(ordinary_permission_guide_complete(nodes, self.name))
        self.assertEqual(ordinary_permission_counts(nodes, self.name), {
            "row_count": 3,
            "granted_row_count": 3,
            "rows": {"bluetooth": "granted", "device_control": "granted", "input": "granted"},
        })
        bluetooth_pending = ordinary_permission_nodes(
            input_pending=False, control_pending=False, bluetooth=True, bluetooth_pending=True
        )
        self.assertFalse(ordinary_permission_guide_complete(bluetooth_pending, self.name))

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


class StartupAXWindowRetryTests(unittest.TestCase):
    @staticmethod
    def ax_windows_error(pid, code=-25204):
        return RuntimeError({
            "ax_error_code": code,
            "error": "could not read root AXWindows",
            "stage": "ax_windows",
            "status": "error",
            "target_pid": pid,
        })

    def test_retries_only_current_live_pid_startup_axcannotcomplete(self):
        instance = object.__new__(Acceptance)
        calls = []
        nodes = [node("AXWindow", "MiMac Dev")]

        def view(pid):
            calls.append(pid)
            if len(calls) == 1:
                raise self.ax_windows_error(pid)
            return nodes

        instance.view = view
        with patch("permission_acceptance.pid_exists", return_value=True), \
             patch("permission_acceptance.time.sleep"):
            self.assertIs(instance.view_waiting_for_windows(23389, "Dev window tree"), nodes)
        self.assertEqual(calls, [23389, 23389])
        self.assertTrue(is_transient_ax_windows_error(self.ax_windows_error(23389), 23389))

    def test_wrong_pid_other_ax_error_or_dead_process_fails_without_retry(self):
        for error, alive in (
            (self.ax_windows_error(999), True),
            (self.ax_windows_error(23389, code=-25202), True),
            (RuntimeError({"stage": "ax_trust", "error": "Accessibility is not trusted"}), True),
            (self.ax_windows_error(23389), False),
        ):
            instance = object.__new__(Acceptance)
            calls = []
            instance.view = lambda _pid, error=error: calls.append(_pid) or (_ for _ in ()).throw(error)
            with patch("permission_acceptance.pid_exists", return_value=alive), \
                 self.assertRaises(RuntimeError):
                instance.view_waiting_for_windows(23389, "Dev window tree")
            self.assertEqual(calls, [23389])


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


class RestartFocusRecoveryTests(unittest.TestCase):
    @staticmethod
    def state(front, *, app=101, settings=202, restart=True, above=True, page=INPUT_PAGES[0]):
        return {
            "app": app, "settings": settings, "front": front, "page": page,
            "restart": restart, "system_sheet": False, "password": False,
            "settings_above_setup": above,
        }

    @staticmethod
    def acceptance(probes):
        instance = object.__new__(Acceptance)
        instance.records = []
        instance.output = Path("/tmp/permission-acceptance")
        iterator = iter(probes)
        instance.probe = lambda: next(iterator)
        instance.record = lambda stage, values: instance.records.append((stage, values))
        return instance

    def test_restart_prompt_focus_gate_requires_same_settings_frontmost(self):
        self.assertTrue(restart_prompt_focus_ready(self.state(202), 101, 202))
        self.assertFalse(restart_prompt_focus_ready(self.state(303), 101, 202))
        self.assertFalse(restart_prompt_focus_ready(self.state(202, app=303), 101, 202))
        self.assertFalse(restart_prompt_focus_ready(self.state(202, settings=404), 101, 202))

    def test_waits_for_two_settings_samples_without_activating_settings(self):
        chrome = self.state(303)
        settings = self.state(202)
        instance = self.acceptance([settings, settings])
        with patch("permission_acceptance.time.monotonic", side_effect=iter(range(100))), \
             patch("permission_acceptance.time.sleep"):
            result = instance.wait_for_restart_prompt_focus(chrome, 101, 202)
        self.assertIs(result, settings)
        self.assertEqual([stage for stage, _ in instance.records], [
            "restart_prompt_observed", "restart_prompt_focus_recovered",
        ])
        self.assertEqual(instance.records[1][1]["consecutive_settings_samples"], 2)
        self.assertFalse(instance.records[1][1]["restart_action_performed"])

    def test_persistent_external_focus_records_resumable_interruption(self):
        chrome = self.state(303)
        instance = self.acceptance([chrome] * 10)
        clock = iter(index * 0.5 for index in range(30))
        with patch("permission_acceptance.time.monotonic", side_effect=lambda: next(clock)), \
             patch("permission_acceptance.time.sleep"):
            with self.assertRaisesRegex(RuntimeError, "no restart action was sent"):
                instance.wait_for_restart_prompt_focus(chrome, 101, 202)
        stage, values = instance.records[-1]
        self.assertEqual(stage, "RESTART_FOCUS_INTERRUPTED")
        self.assertEqual(values["stage"], "restart_prompt_before_click")
        self.assertEqual(values["reason"], "settings_did_not_regain_frontmost_ownership")
        self.assertEqual(values["last_observation"]["frontmost_pid"], 303)
        self.assertFalse(values["restart_action_performed"])
        self.assertIn("--resume-input-drop", values["resume_scope"])

    def test_resumed_page_focus_wait_keeps_front_and_cover_order_assertions(self):
        chrome = self.state(303, restart=False, page=CONTROL_PAGES[0])
        settings = self.state(202, restart=False, page=CONTROL_PAGES[0])
        instance = self.acceptance([settings, settings])
        with patch("permission_acceptance.time.monotonic", side_effect=iter(range(100))), \
             patch("permission_acceptance.time.sleep"):
            result = instance.wait_for_resumed_settings_focus(chrome, 101, 202)
        self.assertIs(result, settings)
        self.assertTrue(resumed_settings_focus_ready(settings, 101, 202))
        self.assertFalse(resumed_settings_focus_ready(
            self.state(101, restart=False, page=CONTROL_PAGES[0]), 101, 202
        ))
        self.assertEqual(instance.records[0][0], "restart_resumed_focus_verified")


class CancelReentryTests(unittest.TestCase):
    def setUp(self):
        self.name = "MiMac Dev"
        self.bundle = Path("/Users/test/.mygo/dev/darwin-arm64/MiMac Dev.app")

    def test_baseline_requires_input_pending_and_all_actual_rows_readable(self):
        two_pending = ordinary_permission_nodes(input_pending=True, control_pending=True)
        self.assertEqual(cancel_reentry_permission_counts(two_pending, self.name), {
            "row_count": 2,
            "granted_row_count": 0,
            "rows": {"device_control": "pending", "input": "pending"},
        })
        three_rows = ordinary_permission_nodes(
            input_pending=True, control_pending=False, bluetooth=True, bluetooth_pending=True
        )
        self.assertEqual(cancel_reentry_permission_counts(three_rows, self.name), {
            "row_count": 3,
            "granted_row_count": 1,
            "rows": {"bluetooth": "pending", "device_control": "granted", "input": "pending"},
        })
        self.assertIsNone(cancel_reentry_permission_counts(
            ordinary_permission_nodes(input_pending=False, control_pending=True), self.name
        ))

        unreadable = two_pending + [
            node("AXStaticText", value="已授权", bounds=ax_bounds(1260, 160, 90, 22))
        ]
        self.assertIsNone(cancel_reentry_permission_counts(unreadable, self.name))

    def test_startup_and_row_predicates_treat_missing_ax_snapshot_as_not_ready(self):
        self.assertFalse(ordinary_general_startup_ready(101, None, self.name))
        self.assertFalse(ordinary_general_surface_ready(None, self.name))
        self.assertIsNone(ordinary_general_nodes(None, self.name))
        self.assertIsNone(ordinary_permission_surfaces(None, self.name))
        self.assertFalse(ordinary_permission_guide_card_present(None, self.name))
        self.assertIsNone(general_input_action_target(None, self.name))
        self.assertIsNone(ordinary_existing_input_target(None, self.name))
        self.assertIsNone(existing_input_target(None, self.name))
        self.assertIsNone(ordinary_permission_counts(None, self.name))
        self.assertIsNone(cancel_reentry_permission_counts(None, self.name))
        self.assertFalse(ordinary_permission_guide_complete(None, self.name))
        self.assertFalse(complete_permission_view(None, self.name))

    def test_cancel_and_reentry_compare_input_and_each_non_target_row(self):
        before = ordinary_permission_counts(ordinary_permission_nodes(
            input_pending=True, control_pending=True, bluetooth=True
        ), self.name)
        unchanged = dict(before)
        unchanged["rows"] = dict(before["rows"])
        self.assertTrue(permission_rows_unchanged(before, unchanged))
        self.assertTrue(non_target_permission_rows_unchanged(before, unchanged))

        input_granted = {**before, "rows": {**before["rows"], "input": "granted"}}
        input_granted["granted_row_count"] = before["granted_row_count"] + 1
        self.assertFalse(permission_rows_unchanged(before, input_granted))
        self.assertTrue(non_target_permission_rows_unchanged(before, input_granted))

        for kind, new_state in (("device_control", "granted"), ("bluetooth", "pending")):
            changed = {**before, "rows": {**before["rows"], kind: new_state}}
            self.assertFalse(permission_rows_unchanged(before, changed))
            self.assertFalse(non_target_permission_rows_unchanged(before, changed))

        missing_row = {**unchanged, "rows": {"input": "pending", "device_control": "pending"}}
        self.assertFalse(permission_rows_unchanged(before, missing_row))
        self.assertFalse(non_target_permission_rows_unchanged(before, missing_row))

    def cancel_result(self):
        return {
            "status": "CANCELLED", "action": "AXPress Cancel",
            "app": 101, "settings": 202,
            "bundle_id": "dev.mimac.app.dev", "bundle_path": str(self.bundle.resolve()),
            "authentication_bundle": "com.apple.SecurityAgent",
            "frontmost_bundle_id": "com.apple.SecurityAgent",
            "settings_page": INPUT_PAGES[0], "authentication_sheet_count": 1,
            "target_page_sheet_count": 1, "target_sheet_role": "AXSheet",
            "target_sheet_source": "verified-page-subtree", "other_window_sheet_count": 0,
            "secure_field_count": 1, "cancel_button_count": 1,
            "cancel_label": "Cancel", "text_entered": False,
        }

    def test_cancel_action_requires_exact_dev_settings_sheet_and_unique_cancel(self):
        result = self.cancel_result()
        self.assertTrue(verified_cancel_result(result, 101, 202, self.bundle))
        failures = (
            {**result, "app": 303},
            {**result, "settings": 404},
            {**result, "bundle_id": "dev.mimac.app"},
            {**result, "bundle_path": "/tmp/other.app"},
            {**result, "settings_page": CONTROL_PAGES[0]},
            {**result, "frontmost_bundle_id": "com.google.Chrome"},
            {**result, "authentication_sheet_count": 2},
            {**result, "authentication_sheet_count": True},
            {**result, "target_sheet_source": "frontmost-window"},
            {**result, "other_window_sheet_count": 1},
            {**result, "target_sheet_role": "AXWindow"},
            {**result, "secure_field_count": 2},
            {**result, "cancel_button_count": 2},
            {**result, "cancel_label": "Allow"},
            {**result, "text_entered": True},
        )
        for invalid in failures:
            with self.subTest(invalid=invalid):
                self.assertFalse(verified_cancel_result(invalid, 101, 202, self.bundle))

    def test_expected_cancel_is_reported_separately_from_permission_grant_success(self):
        nodes = ordinary_permission_nodes(
            input_pending=True, control_pending=True, bluetooth=True
        )
        general_only = nodes[:4]
        instance = object.__new__(Acceptance)
        instance.bundle = self.bundle
        instance.driver = Path("/tmp/permission-driver")
        instance.name = self.name
        instance.auth_timeout = 5
        instance.records = []
        instance.preflight_no_existing_sheet = lambda _scope: {
            "app": 101, "settings": 202, "front": 202,
        }
        initial_views = iter((None, nodes))
        instance.view_if_windows_available = lambda _pid: next(initial_views, nodes)
        views = iter((nodes, general_only, nodes))
        instance.view_waiting_for_windows = lambda *_args: next(views)
        probes = iter((
            {"app": 101, "settings": 202, "front": 202, "page": INPUT_PAGES[0],
             "restart": False, "password": True, "system_sheet": True},
            {"app": 101, "settings": 202, "front": 202, "page": INPUT_PAGES[0],
             "restart": False, "password": False, "system_sheet": False},
        ))
        instance.probe = lambda: next(probes)
        instance.press_permission_row = lambda *_args: {
            "ax_press_started_at": 1.0, "ax_press_completed_at": 1.1,
        }
        instance.wait_auth_or = lambda _stage, _ready: {"app": 101, "settings": 202}
        instance.record_window_latencies = lambda *_args: None
        instance.drag = lambda operation, **_kwargs: {"performed": operation}
        instance.press = lambda *_args: {"status": "pressed"}
        instance.record = lambda stage, values: instance.records.append((stage, values))

        def bounded_retry(test, *_args):
            for _ in range(4):
                if result := test():
                    return result
            raise AssertionError("acceptance wait predicate never became ready")

        with patch("permission_acceptance.wait_for", side_effect=bounded_retry), \
             patch("permission_acceptance.capture", return_value=self.cancel_result()):
            instance.run_cancel_reentry()

        stages = [stage for stage, _ in instance.records]
        self.assertIn("system_auth_cancelled", stages)
        self.assertIn("cancel_permission_readback", stages)
        self.assertIn("cancel_permission_card_closed", stages)
        self.assertIn("cancel_permission_card_reentered", stages)
        self.assertIn("CANCEL_REENTRY_PASS", stages)
        self.assertNotIn("GENERAL_PERMISSIONS_PASS", stages)
        passed = next(values for stage, values in instance.records if stage == "CANCEL_REENTRY_PASS")
        self.assertFalse(passed["input_monitoring_granted"])
        self.assertFalse(passed["business_grant_verified"])
        self.assertEqual(passed["permission_rows_before"], passed["permission_rows_after_cancel"])
        self.assertEqual(passed["permission_rows_before"], passed["permission_rows_after_reentry"])


if __name__ == "__main__":
    unittest.main()
