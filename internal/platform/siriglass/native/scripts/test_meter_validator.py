#!/usr/bin/env python3
"""Exercise the production C meter guard with synthetic Swift-array storage.

This links only the static validator from glass_make.m; unused renderer/SwiftUI
entrypoints are dead-stripped. It does not compose a Glass value or launch an
app. Run on macOS with the Xcode Command Line Tools installed.
"""

from pathlib import Path
import subprocess
import sys
import tempfile


if sys.platform != "darwin":
    raise SystemExit("native meter validator regression requires macOS")

source_dir = Path(__file__).resolve().parent.parent / "Sources/CGlassPrivateABI"
glass_make = source_dir / "glass_make.m"
source = f"""
#include <math.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>
#include "{glass_make}"

typedef struct {{ uint64_t header[4]; float values[64]; }} Fixture;

static void fill(Fixture *fixture, uint64_t count, const float *values) {{
    memset(fixture, 0, sizeof(*fixture));
    fixture->header[2] = count;
    memcpy(fixture->values, values, count * sizeof(float));
}}

int main(void) {{
    Fixture amplitudes, frequencies;
    const float stop[] = {{200.0f}};
    float value = 1.25f;
    fill(&amplitudes, 1, &value);
    fill(&frequencies, 1, stop);
    if (!validate_meter_values(&amplitudes, &frequencies)) {{
        fprintf(stderr, "rejected 1.25: %s\\n", g_error);
        return 1;
    }}

    const float invalid[] = {{-0.01f, NAN, INFINITY, -INFINITY}};
    for (unsigned index = 0; index < sizeof(invalid) / sizeof(invalid[0]); ++index) {{
        value = invalid[index];
        fill(&amplitudes, 1, &value);
        if (validate_meter_values(&amplitudes, &frequencies)) {{
            fprintf(stderr, "accepted invalid amplitude at index %u\\n", index);
            return 2;
        }}
    }}

    puts("native meter validator accepted 1.25 and rejected negative/NaN/+Inf/-Inf");
    return 0;
}}
"""

with tempfile.TemporaryDirectory(prefix="mimac-meter-validator-") as temporary:
    temporary = Path(temporary)
    test_source = temporary / "validator_test.m"
    executable = temporary / "validator_test"
    test_source.write_text(source)
    compile_command = [
        "clang",
        "-x",
        "objective-c",
        "-fobjc-arc",
        "-ffunction-sections",
        "-fdata-sections",
        "-I",
        str(source_dir / "include"),
        str(test_source),
        "-framework",
        "Foundation",
        "-Wl,-dead_strip",
        "-Wl,-undefined,dynamic_lookup",
        "-o",
        str(executable),
    ]
    built = subprocess.run(compile_command, text=True, capture_output=True)
    if built.returncode:
        sys.stdout.write(built.stdout)
        sys.stderr.write(built.stderr)
        raise SystemExit(built.returncode)

    result = subprocess.run([str(executable)], text=True, capture_output=True)
    sys.stdout.write(result.stdout)
    sys.stderr.write(result.stderr)
    raise SystemExit(result.returncode)
