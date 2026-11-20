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
5. Use a dedicated Python environment installed from `script/requirements-packaging.txt` for DMG packaging; the selected interpreter must provide both metadata dependencies before any image is created or mounted. For a Beta artifact, freeze a clean source commit and verify that same commit and dependency selection in the Dev bundle first. Check affected native UI in a real Dev window, save the source and dependency identities in the evidence, and then prepare the date/build version with `release_version.py`, build through MyGo, then run `verify_release.py` for the chosen component profile. Package only that verified daily `MiMac.app` with `package_dmg.py`, keeping the JSON manifest and SHA256 alongside the DMG. The single centered app is the double-click installation entry; no Applications drag target is shown. Do not package a Dev app as Beta or replace `/Applications/MiMac.app` merely to verify an archive.
6. Before any live test, bind the actual PID, canonical bundle path, Bundle ID, executable and native-library hashes. Use `live_acceptance.py prepare --installed-beta-manifest /path/to/release.json` for the installed Beta. It checks `/Applications/MiMac.app` against the release manifest and strict signature before launch, and uses the saved audio configuration. The default source-build lane can build `dist/.../MiMac.app`. For Dev, bind the exact Dev path and configuration separately. Notify a tester only after the selected lane has verified current permissions, route, target and focus. A green System Settings toggle or a different build’s earlier result is not current capability evidence. Launch GUI permission checks through Launch Services (`open -a /exact/App.app --args ...`), as the builders do. Executing the bundle’s Mach-O from a terminal can attribute TCC checks to the terminal host; a successful result from that launch is not proof that the app has its own grant. When an ad-hoc build is rejected despite a checked row, compare the row’s code requirement with the frozen app before removing and re-adding only that lane’s stale entry.

Keep candidate images and their manifests in the verification directory until every acceptance item in the agreed scope has passed. Record each result against the fixed image SHA, executable SHA, source revision and dependency selection. Verify affected native UI in Dev before building the candidate. Before handoff, verify actual installation and all changed permission and voice paths on the frozen candidate. Keep each pending or failed result open, including an observed product behavior when its reference remains unconfirmed. A candidate can be mounted or installed expressly for validation; moving it into Downloads or calling it a Beta handoff requires the completed acceptance record.

Before handing off a Beta, run the native first-install query gate (`MIMAC_APPINSTALL_NATIVE_FIXTURE=1 go test ./internal/platform/appinstall -run "^TestAppinstallNative(FreshTransactionQueriesMissingTarget|MissingDestinationIsNotRunning)$" -count=1 -timeout=30s`). This read-only AppKit gate exercises the real process query with a missing destination; mocked transaction tests and a mounted-package hash check do not cover first installation. Verify an actual DMG launch through copy, installed-path startup, source exit and disk detach, and check update/rollback separately. For an ordinary fresh mount, require detachment on the first request. If the image stays mounted or needs a retry, save the exact eject log, source exit time and system disk events, and keep the gate open. Do not remove a user’s installed app to create an empty test target.

## Preserve privacy grants across Beta updates

Use the same daily Bundle ID and stable Developer ID Application signing identity across builds intended to inherit permissions. Use the MyGo build's `-sign` option with that identity, verify the nested signature, and pass the previous signed App to `verify_release.py --previous-signed-bundle`. The gate checks both designated requirements against the opposite bundle. A failed check prevents a continuity claim. Keep the ordinary ad-hoc Beta lane explicitly local. Its designated requirement is bound to the individual build hash.

When moving the daily identity from an ad-hoc build to the signed lane, refresh the affected permissions once if macOS rejects the existing grant. The exact-app helper below can remove and re-add the matching stale row after the current App exits. If System Settings requests authentication, let the user enter it in the system window. Verify input monitoring and key output before and after a signed-to-signed update on macOS prior to promising permission continuity.

## Inspect first-use UI

Use the isolated Dev bundle with `--show-onboarding` to reopen the welcome page followed by the three-step guide. Explicit display does not reset or create the saved first-use marker; the final action records completion. An unfinished automatic first run resumes even if tool settings were saved during the guide. Existing installations without a marker keep their normal launch behavior. `--show-settings` remains the explicit settings-only diagnostic entry.

Opening or navigating the guide does not enable a remote or begin recording. Choosing a voice tool uses the existing plugin and Current selection action; downloading its model or opening its download page is explicit. UI verification can navigate pages without choosing a tool, changing system input sources, or acquiring shared hardware.

## Inspect or repair a stale Dev registration

For repeated Dev tests, the permission-record repair command checks whether
macOS recently rejected the fixed Dev bundle's code signature for one service.
After a native drop and a permission check, run it from the checkout that
built the bundle:

```sh
python3 script/permission_record_repair.py --permission input-monitoring
python3 script/permission_record_repair.py --permission accessibility
```

If the result is `stale_registration`, quit the exact Dev app and rerun that
command with `--apply`. It verifies the selected bundle, confirms it is the
Launch Services default and resets only that service. Open the same Dev bundle
again, drag it from the guide, complete authorization in System Settings, and
check the app's actual permission after reopening. `no_stale_evidence` leaves
the registration alone. An explicit reset is separate from authorization.

To test speech with the same Dev app after both grants, pass its existing
`bundle-verification.json` to `live_acceptance.py prepare --fixed-dev-receipt`.
The runner validates that bundle and its hashes, performs the usual serial
device handoff, and uses the Dev tool selection. It leaves the verified
executable in place.

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

## Native first-use acceptance

Run the first-use flow with a frozen verification bundle under `~/Library/Caches/MiMac`. Give the bundle a fresh `dev.mimac.verification.*` identity and a unique `MiMac …` display and configuration name. Use the same product source and native libraries. Only the verification identity should differ. The app must use actual system permission queries.

```sh
python3 script/permission_acceptance.py \
  --bundle "$verification_app" --sha256 "$frozen_executable_sha" \
  --output "$new_cache_directory" --interact
```

The runner advances setup, uses native card drags for each permission, presses the system restart button, and checks the new PID and Settings window order. It waits during macOS authentication and resumes after the system dialog completes. Enter passwords only in the macOS dialog. It then checks both grants, the original Settings process exiting, the foreground handoff and the completed first-use record. The output contains phase receipts with no password data. After verification the runner closes only its own diagnostic process. Run during an exclusive foreground test window.

If the run stops after recording the second drag, pass `--finish` with the same identity and output directory. It verifies the existing handoff and finishes the same onboarding session. It never repeats a recorded drag. Keep the evidence and use a fresh diagnostic identity if an earlier phase must be run again. This runner does not reset existing permissions or use the daily or Dev bundle.

## Clean Finder first install

Run this gate in a clean arm64 macOS guest or on a clean test Mac with an active Finder session. Both installation paths and the test account's MiMac preferences must be absent. Use the same frozen image and executable hashes as the release receipt.

```sh
xcrun swiftc -swift-version 5 script/first_install_acceptance.swift -o first-install-acceptance
./first-install-acceptance --check "$dmg" "$dmg_sha256" "$executable_sha256"
./first-install-acceptance --wait-for-double-click "$dmg" "$dmg_sha256" "$executable_sha256" "$new_output_directory"
```

The test selects the mounted App in Finder. Double-click that App. Use `--finder-open` to send the corresponding Finder open event through macOS Automation when available. The receipt distinguishes the two entry modes. A pass requires the mounted installer PID, the different installed PID with a visible first-use window, matching code and executable identity, automatic detach and the native helper's first-attempt success receipt. The test closes the verified installed process after saving the outcome.

## Dev, Preview and Stable packaging

| Channel | Entry | Version and output |
| --- | --- | --- |
| Dev | `script/build_and_run.sh --dev --build-only` | Shanghai date and build number 1 in the local `MiMac Dev.app` |
| Preview | `script/package_preview.sh` or the manually dispatched Preview workflow | `vYYYY.MM.DD.preview_00/MiMacPreview-arm64.dmg` or `MiMacPreview-x86_64.dmg` |
| Stable | Read-only release planning | Reserved `vYYYY.MM.DD.stable_00/MiMac-arm64.dmg` or `MiMac-x86_64.dmg` |

All channels use the Shanghai date in `CFBundleShortVersionString` and a separate numeric `CFBundleVersion`. About reads the channel identity from the bundle. Use `MIMAC_VERSION_DATE=YYYY.MM.DD` to reproduce the date. Each new distribution uses a higher numeric build number. Keep the original number and source for a rerun. Dev produces a local app only.

### Preview

1. Freeze a clean, verified commit and dependency selection.
2. Run `MIMAC_PREVIEW_REVISION=00 script/package_preview.sh BUILD_NUMBER [OUTPUT_DIRECTORY]`. Start at `00` per date and channel and use the next two-digit revision for a new candidate. Omitting the revision produces a date-only local check without a release tag. The default destination is Downloads. Existing packages are refused.
3. Verify the version, numeric build number, full commit, architecture, bundle contents and image. Keep the JSON receipt and SHA-256 alongside the image.

The script freezes the source in a temporary checkout, uses the standard native and app build steps, and verifies bundle identity and the image before moving it into the version directory. Use `MIMAC_PACKAGING_PYTHON` for the locked packaging dependencies. `MIMAC_PREVIEW_PROFILE=siri-glass` includes the optional arm64 display component. A clean MyGo override can be selected with `MIMAC_PREVIEW_MYGO_SOURCE`; its revision is recorded in the receipt.

Local and CI previews are ad-hoc signed. Both package the daily `MiMac.app` identity. GitHub Actions requires an explicit candidate revision, checks existing tags and drafts, and uses the same stems for DMGs, ZIPs, checksums and receipts. Build-only is the default. The optional release is a draft prerelease.

### Stable

Plan from a clean checkout of the Dev-verified commit. Refresh the remote tags and list all releases with a token that can read drafts. Use a new output file outside the checkout.

```sh
git fetch origin --tags
gh api --paginate --slurp 'repos/{owner}/{repo}/releases?per_page=100' > "$release_history"
python3 script/release_version.py \
  --stable-revision "$candidate" --build-number "$build_number" \
  --source-revision "$(git rev-parse HEAD)" --existing-releases "$release_history" \
  --previous-build-number "$highest_build_number" --output "$stable_plan"
```

Set `highest_build_number` to the verified maximum across previous distributions and reserved candidates, including local Previews. Use `0` only when there are none. The planner checks the frozen source, candidate tags including drafts, date ordering and numeric build ordering. It writes the plan without changing app metadata or creating artifacts, tags or releases.

Stable distribution is pending. The release task will build directly from the planned source, sign the app and bundled installer for distribution, notarize and verify the image, then verify the actual installation and upgrade. Use the short Stable filenames and match the draft tag to the embedded identity and checksums. The first Stable release uses manual updates. Only published Stable builds belong in the formal update feed.
