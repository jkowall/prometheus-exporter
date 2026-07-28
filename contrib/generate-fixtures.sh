#!/usr/bin/env bash
#
# Regenerates the notification policy output fixtures under contrib/testdata by
# evaluating the policy with Open Policy Agent.
#
# The Go receiver's contract test (webhook/policy_contract_test.go) asserts
# against these, so they are what keeps the Rego policy and the Go payload
# structs in agreement. CI runs this and fails if the result differs from what
# is committed.
#
# Requires opa on PATH: go install github.com/open-policy-agent/opa@latest

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

for input in contrib/testdata/policy-input-*.json; do
	variant="$(basename "$input" .json)"
	variant="${variant#policy-input-}"
	output="contrib/testdata/policy-output-${variant}.json"

	opa eval \
		--data contrib/notification-policy.rego \
		--input "$input" \
		--format json \
		'data.spacelift.webhook' |
		python3 -c '
import json, sys

result = json.load(sys.stdin)["result"][0]["expressions"][0]["value"]
print(json.dumps(result, indent=2, sort_keys=True))
' >"$output"

	echo "wrote ${output}"
done
