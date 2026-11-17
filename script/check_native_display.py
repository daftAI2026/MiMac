#!/usr/bin/env python3
"""Check the candidate display library's installed-ABI admission without UI."""
from __future__ import annotations

import argparse
import ctypes
import hashlib
import json
from pathlib import Path
import sys


def inspect_library(library: Path) -> dict:
    library = library.resolve(strict=True)
    digest = hashlib.sha256(library.read_bytes()).hexdigest()
    try:
        native = ctypes.CDLL(str(library))
        version = native.mimac_siri_glass_abi_version
        version.argtypes = []
        version.restype = ctypes.c_uint32
        if version() != 1:
            raise ValueError("native display ABI version is unsupported")
        check = native.mimac_siri_glass_check_compatibility
        check.argtypes = []
        check.restype = ctypes.c_int32
        if check() != 1:
            error = native.mimac_siri_glass_last_error
            error.argtypes = [ctypes.c_void_p, ctypes.c_int32]
            error.restype = ctypes.c_int32
            buffer = ctypes.create_string_buffer(2048)
            error(buffer, len(buffer))
            raise ValueError(buffer.value.decode("utf-8", errors="replace") or "native display capability check failed")
    except (OSError, AttributeError) as error:
        raise ValueError(f"native display library cannot be validated: {error}") from error
    return {"abi_version": 1, "compatible": True, "library_sha256": digest}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("library", type=Path)
    args = parser.parse_args()
    try:
        result = inspect_library(args.library)
    except (OSError, ValueError) as error:
        parser.exit(1, f"Native display check failed: {error}\n")
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
