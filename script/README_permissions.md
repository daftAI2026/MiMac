# MiMac build and permission SOP

## Choose the lane first

| Lane | App / Bundle ID | Configuration | Permission identity |
| --- | --- | --- | --- |
| Daily / Beta | `MiMac.app` / `dev.mimac.app` | `~/Library/Application Support/MiMac` | MiMac |
| Development | `MiMac Dev.app` / `dev.mimac.app.dev` | `~/Library/Application Support/MiMac Dev` | MiMac Dev |

Dev remains a local `.app` bundle so macOS can identify its permissions; it is not a distributed DMG or release artifact. Only the daily/Beta lane is packaged for users.

Beta uses the daily identity so its preferences and permissions carry across upgrades when macOS accepts the code identity. It is not a third testing identity. Dev launches select the fixed checkout bundle as the Launch Services default for `dev.mimac.app.dev`, so system Quit & Reopen cannot select a retired worktree copy. The builder removes only known local duplicate Dev registrations, preserves their files and the daily identity, and verifies the selected URL before launch. An out-of-scope duplicate stops with its exact path.

Dev permissions do not authorize the daily app, or vice versa; an ad-hoc rebuild may require revalidation even when the Bundle ID stays the same. Never rename a Dev bundle to make it a daily package.

## Build, test and package

1. For independent development, run `script/build_and_run.sh --dev --verify`. This builds and checks the Dev identity before a smoke launch. The default Dev lane does not acquire the remote, send keys, switch the input method or prepare live audio; it can coexist with the daily app.
2. Inspect the Dev UI with `script/build_and_run.sh --dev run -- --show-settings`. Its permission guide identifies the Dev app and manages only that bundle’s own TCC permissions. A read-only probe, an explicit grant request or opening the matching System Settings page does not acquire a device, send keys or record audio, so those actions may coexist with daily use. Do not refresh daily permissions to make a Dev test pass.
3. Hardware testing is a serial handoff: finish the daily session and stop the daily app, then use `script/build_and_run.sh --dev run -- --device-access`. The launcher and app check the daily process and recovery ownership. After a test, release the remote and quit Dev before returning to daily use. Separate Bundle IDs do not isolate the same Bluetooth device, global input source or audio route.
4. For a daily source build, use `script/build_and_run.sh`. Its identity check must pass before building and its bundle verification must pass before launch. Do not bypass a same-ID duplicate warning by resetting permissions.
5. Use a dedicated Python environment installed from `script/requirements-packaging.txt` for DMG packaging; the selected interpreter must provide both metadata dependencies before any image is created or mounted. For a Beta artifact, freeze a clean source commit, prepare the date/build version with `release_version.py`, build through MyGo, then run `verify_release.py` for the chosen component profile. Package only that verified daily `MiMac.app` with `package_dmg.py`, keeping the JSON manifest and SHA256 alongside the DMG. The single centered app is the double-click installation entry; no Applications drag target is shown. Do not package a Dev app as Beta or replace `/Applications/MiMac.app` merely to verify an archive.
6. Before any live test, bind the actual PID, canonical bundle path, Bundle ID, executable and native-library hashes. `live_acceptance.py prepare/finish` currently supports the daily source-build lane only: it resolves daily configuration and `dist/.../MiMac.app`, and may invoke the daily builder. Do not use it to test a Dev or installed Beta binary. Dev and Beta artifact tests must explicitly bind their own paths and avoid launching another lane; a successful source-build test is not artifact acceptance. Notify a tester only after the selected lane has verified current permissions, route, target and focus. A green System Settings toggle or a different build’s earlier result is not current capability evidence. Launch GUI permission checks through Launch Services (`open -a /exact/App.app --args ...`), as the builders do. Executing the bundle’s Mach-O from a terminal can attribute TCC checks to the terminal host; a successful result from that launch is not proof that the app has its own grant. When an ad-hoc build is rejected despite a checked row, compare the row’s code requirement with the frozen app before removing and re-adding only that lane’s stale entry.

Before handing off a Beta, run the native first-install query gate (`MIMAC_APPINSTALL_NATIVE_FIXTURE=1 go test ./internal/platform/appinstall -run "^TestAppinstallNative(FreshTransactionQueriesMissingTarget|MissingDestinationIsNotRunning)$" -count=1 -timeout=30s`). This read-only AppKit gate exercises the real process query with a missing destination; mocked transaction tests and a mounted-package hash check do not cover first installation. Verify an actual DMG launch through copy, installed-path startup and source exit, and check update/rollback separately. Do not remove a user’s installed app to create an empty test target.

## Inspect first-use UI

Use the isolated Dev bundle with `--show-onboarding` to reopen the three-step guide. Explicit display does not reset or create the saved first-use marker; the final action records completion. An unfinished automatic first run resumes even if tool settings were saved during the guide. Existing installations without a marker keep their normal launch behavior. `--show-settings` remains the explicit settings-only diagnostic entry.

Opening or navigating the guide does not enable a remote or begin recording. Choosing a voice tool uses the existing plugin and Current selection action; downloading its model or opening its download page is explicit. UI verification can navigate pages without choosing a tool, changing system input sources, or acquiring shared hardware.

## Refresh one exact privacy identity

This helper accepts only two exact app identities. It reads `CFBundleIdentifier` from the selected app’s `Info.plist` and uses that value to choose the matching Settings row:

| Lane | Bundle ID | Settings row label | AX identifiers |
|---|---|---|---|
| Daily / Beta | `dev.mimac.app` | `MiMac` | `MiMac_Title`, `MiMac_Toggle` |
| Dev | `dev.mimac.app.dev` | `MiMac Dev` | `MiMac Dev_Title`, `MiMac Dev_Toggle` |

Every other Bundle ID is rejected before Settings UI actions. There is no command-line override for the row identity. Only the later hardware test requires an explicit device handoff. Never pass a daily path as a workaround for a Dev denial.

From the repository root, pass the canonical `.app` path for the lane being refreshed:

```sh
script/refresh_macos_permissions.swift \
  --bundle /absolute/path/to/MiMac.app \
  --permission accessibility \
  --apply
```

For Dev, use its own canonical bundle path instead (for example, `/absolute/path/to/MiMac Dev.app`). The app’s verified `CFBundleIdentifier`, rather than the path’s display name, selects the row identity.

Use `--permission input-monitoring`, `accessibility`, or `both`. `--inspect` only reads the currently visible recognized page. Applying requires the selected target app to be stopped and the runner to already have Accessibility trust; its verified Return action also requires the separate Post Event preflight to pass. The helper does not terminate apps, enter credentials, request its own trust, or press system Allow.

The helper discovers System Settings by bundle identity. It navigates from one ordinary Settings window to the known privacy URL, then checks the exact page title and unique row label and AX identifiers for the selected lane. Unknown windows or sheets stop the run before refresh actions. macOS may call the Accessibility page Device Control and Data Access.

If a run is pending, inspect its journal and stage log under `~/Library/Caches/MiMac/permissions-refresh/`, then resume with `--resume /path/to/journal.json`. Resume is bound to the canonical bundle path, executable SHA256, and requested permissions. Completed pages are read again; an externally disabled or missing row stops the run instead of reporting an old grant.

An existing Open panel or Go To sheet can be resumed. The helper sets and reads back the absolute path through AX, waits for the exact canonical path suggestion, then sends one unmodified Return to the verified foreground sheet. It checks that the sheet closes before pressing Open. Journaled actions are not blindly repeated.

`ui_grant_requested` means the exact selected-lane System Settings row read its matching toggle identifier as `1` (`MiMac_Toggle` for Daily/Beta or `MiMac Dev_Toggle` for Dev). Verify actual input monitoring and key output using the same fixed App binary afterward; the UI result is not an independent TCC capability check.

Keep permission refresh journals tied to the same lane, canonical bundle and executable hash. Never use a broad `tccutil reset` to switch lanes. Record actual query results and any pending system authentication in the current task evidence; do not claim a grant until the fixed target binary confirms it.
