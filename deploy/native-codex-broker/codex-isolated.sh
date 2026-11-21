#!/bin/sh
set -eu

release_dir=${0%/*}
state=/home/codex005/.local/state/sub2api-native-egress/codex-0.171.0
exec /usr/bin/bwrap --die-with-parent --ro-bind / / --proc /proc --dev /dev --tmpfs /tmp \
    --bind "$state" "$state" \
    --ro-bind /home/codex005/.codex/auth.json "$state/auth.json" \
    --setenv CODEX_HOME "$state" --chdir "$state" \
    -- "$release_dir/codex" "$@"
