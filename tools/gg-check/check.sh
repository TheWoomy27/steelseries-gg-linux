#!/bin/sh
# Checks a SteelSeries GG installer against what gg-linux handles:
#
#   - gg-setup imports it (into OUTDIR/data);
#   - gg-linux's client patches still match;
#   - every device declaration loads, and the database migrations apply;
#   - the API calls the client makes, and the routes the Windows Engine
#     and GG core serve, compared with known/ (the version ggd was last
#     checked against): new ones are what ggd may need to answer.
#
# Usage: tools/gg-check/check.sh [--update] INSTALLER OUTDIR
#
# Writes OUTDIR/report.md. Exits 0 when everything passes and nothing is
# new, 1 when something needs a look. Once ggd handles a new version,
# --update records its lists in known/ to check later versions against.
# Nothing from the installer goes outside OUTDIR.
set -eu

update=0
if [ "${1:-}" = --update ]; then
    update=1
    shift
fi
[ $# = 2 ] || { echo "usage: $0 [--update] INSTALLER OUTDIR" >&2; exit 2; }
installer=$(cd "$(dirname "$1")" && pwd)/$(basename "$1")
out=$2
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
known=$here/known
mkdir -p "$out"
out=$(cd "$out" && pwd)
rm -rf "$out/data" "$out/exe" "$out/lists"
mkdir -p "$out/bin" "$out/exe" "$out/lists"
export LC_ALL=C

(cd "$repo" && go build -o "$out/bin/" ./cmd/gg-setup ./tools/gg-check ./tools/gorev)

failed=0
attention=0
rows=""
details=""
row() { rows="$rows| $1 | $2 |
"; }
# detail TITLE FILE: a collapsed section with a file's text.
detail() {
    details="$details
<details><summary>$1</summary>

\`\`\`
$(cat "$2")
\`\`\`

</details>
"
}

# Import.
if GG_LINUX_DATA="$out/data" "$out/bin/gg-setup" "$installer" >"$out/import.log" 2>&1; then
    version=$(sed -n 's/.*"ggVersion": *"\([^"]*\)".*/\1/p' "$out/data/import.json")
    specs=$(sed -n 's/.*"specs": *\([0-9]*\).*/\1/p' "$out/data/import.json")
    row "Import" "Pass: $specs device specification files decrypted"
else
    version=unknown
    failed=1
    row "Import" "**Fail**"
fi
detail "gg-setup" "$out/import.log"

if [ "$failed" = 0 ]; then
    check() { # check NAME LOGFILE COMMAND...: a pass/fail row from a command
        name=$1 log=$2
        shift 2
        if "$@" >"$log" 2>&1; then
            row "$name" "Pass: $(tail -n 1 "$log")"
        else
            failed=1
            row "$name" "**Fail**: $(tail -n 1 "$log")"
        fi
        detail "$name" "$log"
    }
    check "Client patches" "$out/patches.log" node "$here/patches.js" "$out/data/client/app"
    check "Device specifications" "$out/specs.log" "$out/bin/gg-check" specs "$out/data"
    check "Databases" "$out/databases.log" "$out/bin/gg-check" databases "$out/data"

    # What the client calls, and what the Engine and GG core serve.
    node "$here/calls.js" "$out/data/client/app" >"$out/lists/client-calls.txt"
    sevenzip=$(command -v 7zz || command -v 7z || command -v 7za)
    (cd "$out/exe" && "$sevenzip" e -y "$installer" apps/engine/SteelSeriesEngine.exe SteelSeriesGG.exe >/dev/null)
    # gorev prints METHOD PATH HANDLER; either of the first two can be
    # missing. Handler names (func43...) change between builds.
    routes() {
        "$out/bin/gorev" routes "$1" | awk '{
            m = "*"; p = "-"
            for (i = 1; i < NF; i++) if ($i ~ /^\//) p = $i; else if (i == 1) m = $i
            print m, p }' | sort -u
    }
    routes "$out/exe/SteelSeriesEngine.exe" >"$out/lists/engine-routes.txt"
    routes "$out/exe/SteelSeriesGG.exe" >"$out/lists/core-routes.txt"
    for list in client-calls engine-routes core-routes; do
        case $list in
        client-calls) name="Client API calls" ;;
        engine-routes) name="Engine routes" ;;
        core-routes) name="GG core routes" ;;
        esac
        if [ ! -s "$out/lists/$list.txt" ]; then
            failed=1
            row "$name" "**Fail**: none found"
            continue
        fi
        if [ ! -f "$known/$list.txt" ]; then
            row "$name" "$(wc -l <"$out/lists/$list.txt") (nothing known to compare with)"
            continue
        fi
        comm -13 "$known/$list.txt" "$out/lists/$list.txt" >"$out/lists/$list.new"
        comm -23 "$known/$list.txt" "$out/lists/$list.txt" >"$out/lists/$list.gone"
        new=$(wc -l <"$out/lists/$list.new")
        gone=$(wc -l <"$out/lists/$list.gone")
        if [ "$new" = 0 ] && [ "$gone" = 0 ]; then
            row "$name" "Pass: no changes"
        else
            [ "$new" = 0 ] || attention=1
            row "$name" "**$new new**, $gone gone"
            [ "$new" = 0 ] || detail "$name: new" "$out/lists/$list.new"
            [ "$gone" = 0 ] || detail "$name: gone" "$out/lists/$list.gone"
        fi
    done
fi

against=$(cat "$known/version" 2>/dev/null || echo "nothing")
if [ "$failed" = 1 ]; then
    verdict="Something failed: gg-linux may not work with GG $version as it is."
elif [ "$attention" = 1 ]; then
    verdict="The checks pass, but GG $version calls or serves something new: see whether ggd needs to answer it."
else
    verdict="Everything passes and nothing is new."
fi
{
    echo "## GG $version"
    echo
    echo "$verdict"
    echo
    echo "Checked $(basename "$installer") against what gg-linux handles (known: GG $against)."
    echo
    echo "| Check | Result |"
    echo "| --- | --- |"
    printf %s "$rows"
    printf %s "$details"
    echo
    echo "Once ggd handles this version, record it: \`tools/gg-check/check.sh --update $(basename "$installer") OUTDIR\`, then commit \`tools/gg-check/known\`."
} >"$out/report.md"
echo "Report: $out/report.md"
echo "$verdict"

if [ "$update" = 1 ] && [ "$failed" = 0 ]; then
    mkdir -p "$known"
    cp "$out/lists/client-calls.txt" "$out/lists/engine-routes.txt" "$out/lists/core-routes.txt" "$known/"
    printf '%s\n' "$version" >"$known/version"
    echo "Recorded GG $version in $known"
fi
[ "$failed" = 0 ] && [ "$attention" = 0 ]
