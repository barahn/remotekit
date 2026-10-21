#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright (C) 2026 Fabrintek Engenharia Digital Ltda

set -euo pipefail

RUNS="${1:-20}"
echo "Running ./stream ${RUNS} times under taskset -c 0,1 -race -cpu=2..."

failed=0
passed=0
declare -A failures_per_test

for i in $(seq 1 "${RUNS}"); do
    echo -n "Run ${i}/${RUNS}... "
    output_file=$(mktemp)
    if taskset -c 0,1 go test -count=1 -race -cpu=2 -v ./stream > "${output_file}" 2>&1; then
        echo "PASS"
        passed=$((passed + 1))
    else
        echo "FAIL"
        failed=$((failed + 1))
        # Extract failed test names
        while IFS= read -r line; do
            testname=$(echo "${line}" | awk '{print $3}')
            failures_per_test["${testname}"]=$(( ${failures_per_test["${testname}"]:-0} + 1 ))
            echo "   Failed: ${testname}"
        done < <(grep "^--- FAIL:" "${output_file}" || true)
    fi
    rm -f "${output_file}"
done

echo ""
echo "Summary: ${passed} passed, ${failed} failed out of ${RUNS} runs."
if [ ${failed} -gt 0 ]; then
    echo "Failures per test:"
    for t in "${!failures_per_test[@]}"; do
        echo "  - ${t}: ${failures_per_test[$t]} failure(s)"
    done
    exit 1
fi
