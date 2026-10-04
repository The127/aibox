#!/usr/bin/env bash
# Nothing but the license texts contains an em dash.
set -euo pipefail

emdash=$(printf '\342\200\224')
if git grep -n "$emdash" -- ':!LICENSE' ':!LICENSES/'; then
    echo "em dashes found, replace them"
    exit 1
fi
