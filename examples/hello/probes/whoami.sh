#!/bin/bash
# Read what the box is, and say so on the structured channel.
#
# TWO CHANNELS, and the difference between them is the whole design.
#
#   fd 3        FACTS. Tab-separated verbs the runner parses:
#                 fact<TAB>key<TAB>value           a reading that was taken
#                 no-reading<TAB>key<TAB>reason    one that could not be
#               A judge reads these and nothing else.
#
#   stdout      The CLAIM. Testimony: what the probe says about itself, kept
#               verbatim and never parsed for a fact. A judge that read this
#               would be taking the probe's word for its own result.
#
# The exit code is the third thing, and it is the step's own: the manifest's
# `expect.rc` is checked against it.
set -eu

fact()       { printf 'fact\t%s\t%s\n' "$1" "$2" >&3; }
no_reading() { printf 'no-reading\t%s\t%s\n' "$1" "$2" >&3; }

fact os.kernel "$(uname -s | tr '[:upper:]' '[:lower:]')"
fact os.arch "$(uname -m)"

# A PROBE THAT CANNOT ASK SAYS SO. Recording "unknown" here would be a value,
# and a judge cannot tell an invented value from a measured one - so a box with
# no os-release would pass on the strength of a reading nobody took.
if [ -r /etc/os-release ]; then
    . /etc/os-release
    fact os.id "${ID:-}"
    fact os.version "${VERSION_ID:-}"
else
    no_reading os.id "this image has no /etc/os-release"
fi

echo "this box says it is $(uname -s) $(uname -r)"
