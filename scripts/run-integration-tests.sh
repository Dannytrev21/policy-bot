#!/usr/bin/env bash

# Copyright 2025 Palantir Technologies, Inc.
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

set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration
LOCALSTACK_URL="${LOCALSTACK_URL:-http://localhost:4566}"
LOCALSTACK_WAIT_TIMEOUT="${LOCALSTACK_WAIT_TIMEOUT:-30}"

echo -e "${BLUE}======================================${NC}"
echo -e "${BLUE}Policy Bot Integration Test Runner${NC}"
echo -e "${BLUE}======================================${NC}"
echo ""

# Function to print colored messages
print_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

print_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Function to check if LocalStack is running
check_localstack() {
    print_info "Checking LocalStack availability at $LOCALSTACK_URL..."

    local timeout=$LOCALSTACK_WAIT_TIMEOUT
    local elapsed=0

    while [ $elapsed -lt $timeout ]; do
        if curl -s -f "$LOCALSTACK_URL/_localstack/health" > /dev/null 2>&1; then
            print_success "LocalStack is available"
            return 0
        fi

        if [ $elapsed -eq 0 ]; then
            print_info "Waiting for LocalStack to be ready..."
        fi

        sleep 2
        elapsed=$((elapsed + 2))
    done

    print_error "LocalStack is not available after ${timeout}s timeout"
    print_info "Please ensure LocalStack is running:"
    print_info "  docker run -d -p 4566:4566 localstack/localstack"
    print_info "  OR run: ./scripts/setup-localstack.sh start"
    return 1
}

# Function to run tests
run_tests() {
    local test_pattern="${1:-}"
    local test_args="-v"

    if [ -n "$test_pattern" ]; then
        print_info "Running integration tests matching: $test_pattern"
        test_args="$test_args -run $test_pattern"
    else
        print_info "Running all integration tests"
    fi

    # Run tests with coverage
    print_info "Executing: go test ./test $test_args -coverprofile=coverage.out"

    if go test ./test $test_args -coverprofile=coverage.out; then
        print_success "All tests passed!"

        # Display coverage summary
        if [ -f coverage.out ]; then
            print_info "Coverage summary:"
            go tool cover -func=coverage.out | tail -n 1
        fi

        return 0
    else
        print_error "Tests failed!"
        return 1
    fi
}

# Function to display usage
usage() {
    cat << EOF
Usage: $0 [OPTIONS] [TEST_PATTERN]

Run Policy Bot integration tests with LocalStack.

OPTIONS:
    -h, --help              Show this help message
    -s, --skip-localstack   Skip LocalStack availability check
    -c, --coverage          Display detailed coverage report
    -u, --unit              Run unit tests only (no integration tests)
    -a, --all               Run all tests (unit + integration)

TEST_PATTERN:
    Optional Go test pattern to filter which tests to run
    Examples:
        $0 TestComprehensive_SQSToWorkerPoolToHandlers
        $0 TestComprehensive_DualProcessing
        $0 TestIntegration_

EXAMPLES:
    # Run all integration tests (default)
    $0

    # Run specific test
    $0 TestComprehensive_CloudVsEnterpriseRouting

    # Run all comprehensive tests
    $0 TestComprehensive_

    # Run with detailed coverage
    $0 --coverage

    # Run unit tests for SQS consumer
    $0 --unit

ENVIRONMENT VARIABLES:
    LOCALSTACK_URL              LocalStack endpoint (default: http://localhost:4566)
    LOCALSTACK_WAIT_TIMEOUT     Timeout waiting for LocalStack in seconds (default: 30)

EOF
    exit 0
}

# Parse command line arguments
SKIP_LOCALSTACK=false
SHOW_COVERAGE=false
RUN_UNIT=false
RUN_ALL=false
TEST_PATTERN=""

while [[ $# -gt 0 ]]; do
    case $1 in
        -h|--help)
            usage
            ;;
        -s|--skip-localstack)
            SKIP_LOCALSTACK=true
            shift
            ;;
        -c|--coverage)
            SHOW_COVERAGE=true
            shift
            ;;
        -u|--unit)
            RUN_UNIT=true
            shift
            ;;
        -a|--all)
            RUN_ALL=true
            shift
            ;;
        -*)
            print_error "Unknown option: $1"
            usage
            ;;
        *)
            TEST_PATTERN="$1"
            shift
            ;;
    esac
done

# Main execution
main() {
    # Run unit tests if requested
    if [ "$RUN_UNIT" = true ]; then
        print_info "Running unit tests for SQS consumer..."
        if go test ./server/sqsconsumer -v -coverprofile=coverage-unit.out; then
            print_success "Unit tests passed!"

            if [ -f coverage-unit.out ]; then
                print_info "Unit test coverage:"
                go tool cover -func=coverage-unit.out | tail -n 1
            fi
        else
            print_error "Unit tests failed!"
            exit 1
        fi

        if [ "$RUN_ALL" = false ]; then
            exit 0
        fi
    fi

    # Check LocalStack availability
    if [ "$SKIP_LOCALSTACK" = false ]; then
        if ! check_localstack; then
            exit 1
        fi
    else
        print_warning "Skipping LocalStack availability check"
    fi

    # Run integration tests
    if ! run_tests "$TEST_PATTERN"; then
        exit 1
    fi

    # Show detailed coverage if requested
    if [ "$SHOW_COVERAGE" = true ] && [ -f coverage.out ]; then
        print_info "Detailed coverage report:"
        go tool cover -func=coverage.out

        print_info "Generating HTML coverage report..."
        go tool cover -html=coverage.out -o coverage.html
        print_success "HTML coverage report generated: coverage.html"
    fi

    print_success "Integration test run completed successfully!"
}

# Run main function
main
