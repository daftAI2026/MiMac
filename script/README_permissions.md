# Refresh MiMac privacy entries

From the repository root, use the canonical app bundle path:

```sh
script/refresh_macos_permissions.swift \
  --bundle /absolute/path/to/MiMac.app \
  --permission accessibility \
  --apply
```

Use `--permission input-monitoring`, `accessibility`, or `both`. `--inspect` only reads the currently visible recognized page. Applying requires MiMac to be stopped and the runner to already have Accessibility trust. The helper does not terminate apps, enter credentials, request its own trust, or press system Allow.

The helper discovers System Settings by bundle identity. It navigates from one ordinary Settings window to the known privacy URL, then checks the exact page title and unique MiMac row. Unknown windows or sheets stop the run before refresh actions. macOS may call the Accessibility page Device Control and Data Access.

If a run is pending, inspect its journal and stage log under `~/Library/Caches/MiMac/permissions-refresh/`, then resume with `--resume /path/to/journal.json`. Resume is bound to the canonical bundle path, executable SHA256, and requested permissions. Completed pages are read again; an externally disabled or missing row stops the run instead of reporting an old grant.

An existing Open panel or Go To sheet can be resumed. The helper sets and reads back the absolute path through AX, waits for the exact canonical path suggestion, then sends one unmodified Return to the verified foreground sheet. It checks that the sheet closes before pressing Open. Journaled actions are not blindly repeated.

`ui_grant_requested` means the exact System Settings row read `MiMac_Toggle=1`. Verify actual input monitoring and key output using the same fixed App binary afterward; the UI result is not an independent TCC capability check.
