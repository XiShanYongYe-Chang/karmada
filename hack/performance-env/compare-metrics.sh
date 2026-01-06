#!/usr/bin/env bash

# Copyright 2026 The Karmada Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -o errexit
set -o nounset
set -o pipefail

# Check dependencies
if ! command -v jq &> /dev/null; then
    echo "Error: jq is required but not installed."
    exit 1
fi

BEFORE_FILE=""
AFTER_FILE=""
THRESHOLD=0

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --threshold|-t)
            THRESHOLD="$2"
            shift 2
            ;;
        *)
            if [ -z "$BEFORE_FILE" ]; then
                BEFORE_FILE="$1"
            elif [ -z "$AFTER_FILE" ]; then
                AFTER_FILE="$1"
            else
                echo "Unknown argument: $1"
                exit 1
            fi
            shift
            ;;
    esac
done

if [ -z "$BEFORE_FILE" ] || [ -z "$AFTER_FILE" ]; then
    echo "Usage: $0 <before-json> <after-json> [--threshold <percent>]"
    echo "Example: $0 metrics-1.json metrics-2.json --threshold 5"
    exit 1
fi

# Create temp files
TMP_BEFORE=$(mktemp)
TMP_AFTER=$(mktemp)
# Ensure cleanup on exit
trap 'rm -f "$TMP_BEFORE" "$TMP_AFTER"' EXIT

# Function to flatten JSON to "key<tab>value" format and sort by key
function flatten_json() {
    local file="$1"
    # jq explanation:
    # paths(scalars) gets paths to all leaf values
    # join(".") joins path array into dot.notation string
    # getpath($p) gets the actual value
    # tostring ensures value is string
    # @tsv formats as tab-separated values
    jq -r 'paths(scalars) as $p | [($p|join(".")), (getpath($p)|tostring)] | @tsv' "$file" | sort
}

# Flatten both files
flatten_json "$BEFORE_FILE" > "$TMP_BEFORE"
flatten_json "$AFTER_FILE" > "$TMP_AFTER"

# Print Header
# Key width 60, numbers width 15, percent width 10
printf "%-60s | %15s | %15s | %15s | %10s\n" "Metric" "Before" "After" "Diff" "Change %"
printf "%s\n" "$(printf -- '-%.0s' {1..125})"

# Join files on key (field 1) and compare using awk
# join: -t $'\t' (tab separator), -a 1 -a 2 (full outer join), -e "NULL" (fill missing), -o ... (output fields)
join -t $'\t' -a 1 -a 2 -e "NULL" -o 0,1.2,2.2 "$TMP_BEFORE" "$TMP_AFTER" | \
awk -F'\t' -v threshold="$THRESHOLD" '
BEGIN {
    # Ignore metadata fields
    ignored["metadata.timestamp"] = 1
    ignored["metadata.collection_time"] = 1
    ignored["metadata.prometheus_endpoint"] = 1
    
    max_key_len = 20 # Minimum width
    count = 0
}

{
    key = $1
    v1_str = $2
    v2_str = $3

    # Skip ignored keys
    if (key in ignored) next;
    
    # Handle NULL (missing keys) - treat as 0 for metrics, or "NULL" string for others
    if (v1_str == "NULL") v1_str = "0";
    if (v2_str == "NULL") v2_str = "0";

    # Skip if identical
    if (v1_str == v2_str) next;

    # Check if values look like numbers
    # Regex allows: optional sign, digits, optional dot+digits, optional scientific notation
    num_regex = "^[+-]?([0-9]+([.][0-9]*)?|[.][0-9]+)([eE][+-]?[0-9]+)?$"
    
    is_num = (v1_str ~ num_regex && v2_str ~ num_regex)

    if (is_num) {
        v1 = v1_str + 0
        v2 = v2_str + 0
        diff = v2 - v1
        
        pct = 0
        pct_str = ""

        if (v1 != 0) {
            pct = (diff / (v1 < 0 ? -v1 : v1)) * 100
        } else {
            if (v2 != 0) {
                # From 0 to non-0 is infinite change
                pct = (v2 > 0) ? 999999999 : -999999999 
                pct_str = (v2 > 0) ? "+inf%" : "-inf%"
            } else {
                pct = 0
            }
        }

        # Filter logic
        abs_pct = (pct < 0 ? -pct : pct)
        
        # If threshold is set
        if (threshold > 0) {
            # If changed from 0 to 0 (very small diff), skip
            if (v1 == 0 && (diff < 1e-9 && diff > -1e-9)) next;
            # If percentage change is below threshold, skip (unless it was 0 -> non-0)
            if (v1 != 0 && abs_pct < threshold) next;
        }

        if (pct_str == "") {
            pct_str = sprintf("%+.2f%%", pct)
        }

        # Store data
        count++
        keys[count] = key
        v1s[count] = v1
        v2s[count] = v2
        diffs[count] = diff
        pcts[count] = pct_str
        is_nums[count] = 1
        
        if (length(key) > max_key_len) max_key_len = length(key)

    } else {
        # Non-numeric change (strings)
        # Only show if threshold is 0 (show all changes)
        if (threshold == 0) {
             count++
             keys[count] = key
             v1s_str[count] = v1_str
             v2s_str[count] = v2_str
             is_nums[count] = 0
             if (length(key) > max_key_len) max_key_len = length(key)
        }
    }
}

END {
    if (count == 0) {
        print "No changes found matching the criteria."
        exit
    }
    
    # Cap max length at 120 to avoid extreme width, but generally allow long keys
    if (max_key_len > 120) max_key_len = 120

    # Format strings
    fmt_header = sprintf("%%-%ds | %%15s | %%15s | %%15s | %%10s\n", max_key_len)
    fmt_row_num = sprintf("%%-%ds | %%15.4f | %%15.4f | %%15.4f | %%10s\n", max_key_len)
    fmt_row_str = sprintf("%%-%ds | %%15s | %%15s | %%15s | %%10s\n", max_key_len)

    # Header
    printf fmt_header, "Metric", "Before", "After", "Diff", "Change %"
    
    # Separator
    sep_len = max_key_len + 65
    for (i=0; i<sep_len; i++) printf "-"
    printf "\n"

    for (i=1; i<=count; i++) {
        disp_key = keys[i]
        if (length(disp_key) > max_key_len) disp_key = substr(disp_key, 1, max_key_len-3) "..."
        
        if (is_nums[i]) {
            printf fmt_row_num, disp_key, v1s[i], v2s[i], diffs[i], pcts[i]
        } else {
            d_v1 = v1s_str[i]
            d_v2 = v2s_str[i]
            if (length(d_v1) > 15) d_v1 = substr(d_v1, 1, 12) "..."
            if (length(d_v2) > 15) d_v2 = substr(d_v2, 1, 12) "..."
            
            printf fmt_row_str, disp_key, d_v1, d_v2, "N/A", "N/A"
        }
    }
}
'
