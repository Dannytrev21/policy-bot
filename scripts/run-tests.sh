#!/bin/bash

# Copyright 2018 Palantir Technologies, Inc.
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

set -e

echo "🧪 Policy Bot Event Processing Tests"
echo "===================================="

# Check if LocalStack is available
echo "🔍 Checking LocalStack availability..."
if curl -s http://localhost:4566 > /dev/null 2>&1; then
    echo "✅ LocalStack is running at http://localhost:4566"
else
    echo "❌ LocalStack is not running. Please start LocalStack first:"
    echo "   docker run --rm -p 4566:4566 localstack/localstack"
    echo ""
    echo "   Or run tests without SQS (HTTP only):"
    echo "   go test ./test/... -v -short"
    exit 1
fi

echo ""
echo "🏗️  Building test binary..."
cd "$(dirname "$0")/.."
go build -o /tmp/policy-bot-test ./scripts/test-event-processing.go

echo ""
echo "🚀 Running event processing tests..."
echo "   This will test both HTTP webhooks and SQS message processing"
echo "   Press Ctrl+C to stop early"
echo ""

# Run the test
/tmp/policy-bot-test

echo ""
echo "🧹 Cleaning up..."
rm -f /tmp/policy-bot-test

echo "✅ All tests completed!"
