#!/bin/bash
# Load Testing Script for Rate Limiting (Phase 3)
#
# This script runs comprehensive load tests to validate:
# 1. 200 events/sec capability
# 2. Adaptive vs static performance
# 3. Burst traffic handling
# 4. Memory and CPU profiling

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Configuration
TEST_DURATION="${TEST_DURATION:-10m}"
TARGET_RATE="${TARGET_RATE:-200}"
ENABLE_PROFILING="${ENABLE_PROFILING:-false}"
PROFILE_DIR="${PROFILE_DIR:-$PROJECT_ROOT/profiles}"

echo "========================================"
echo "Rate Limiting Load Test"
echo "========================================"
echo "Target Rate: ${TARGET_RATE} events/sec"
echo "Duration: ${TEST_DURATION}"
echo "Profiling: ${ENABLE_PROFILING}"
echo "========================================"
echo ""

cd "$PROJECT_ROOT"

# Create profile directory if profiling is enabled
if [ "$ENABLE_PROFILING" = "true" ]; then
    mkdir -p "$PROFILE_DIR"
    echo "Profiles will be saved to: $PROFILE_DIR"
fi

# Test 1: Basic 200 events/sec load test
echo -e "${YELLOW}[TEST 1]${NC} Running 200 events/sec sustained load test..."
if go test -v ./test/load -run TestLoadTest_200EventsPerSecond -timeout 30m; then
    echo -e "${GREEN}✓${NC} Test 1 PASSED: System handles 200 events/sec"
else
    echo -e "${RED}✗${NC} Test 1 FAILED: System cannot handle 200 events/sec"
    exit 1
fi
echo ""

# Test 2: Burst traffic test
echo -e "${YELLOW}[TEST 2]${NC} Running burst traffic test (50 → 200 → 50 events/sec)..."
if go test -v ./test/load -run TestLoadTest_BurstTraffic -timeout 15m; then
    echo -e "${GREEN}✓${NC} Test 2 PASSED: System handles burst traffic"
else
    echo -e "${RED}✗${NC} Test 2 FAILED: System cannot handle burst traffic"
    exit 1
fi
echo ""

# Test 3: Adaptive vs Static comparison
echo -e "${YELLOW}[TEST 3]${NC} Running adaptive vs static comparison..."
if go test -v ./test/load -run TestLoadTest_AdaptiveVsStatic -timeout 20m; then
    echo -e "${GREEN}✓${NC} Test 3 PASSED: Adaptive vs static comparison complete"
else
    echo -e "${RED}✗${NC} Test 3 FAILED: Comparison test failed"
    exit 1
fi
echo ""

# Test 4: Run performance benchmarks
echo -e "${YELLOW}[TEST 4]${NC} Running performance benchmarks..."
if go test -v ./test -bench=BenchmarkRateLimiter -benchmem -benchtime=10s > "$PROJECT_ROOT/benchmark_results.txt"; then
    echo -e "${GREEN}✓${NC} Test 4 PASSED: Benchmarks complete"
    echo "Results saved to: benchmark_results.txt"
    echo ""
    echo "Benchmark Summary:"
    grep "Benchmark" "$PROJECT_ROOT/benchmark_results.txt" | head -10
else
    echo -e "${RED}✗${NC} Test 4 FAILED: Benchmarks failed"
    exit 1
fi
echo ""

# Test 5: Memory and CPU profiling (if enabled)
if [ "$ENABLE_PROFILING" = "true" ]; then
    echo -e "${YELLOW}[TEST 5]${NC} Running CPU and memory profiling..."

    # CPU Profile
    echo "  - Running CPU profiling..."
    go test -v ./test/load -run TestLoadTest_200EventsPerSecond \
        -cpuprofile="$PROFILE_DIR/cpu.prof" \
        -timeout 30m > /dev/null 2>&1

    # Memory Profile
    echo "  - Running memory profiling..."
    go test -v ./test/load -run TestLoadTest_200EventsPerSecond \
        -memprofile="$PROFILE_DIR/mem.prof" \
        -timeout 30m > /dev/null 2>&1

    # Generate reports
    echo "  - Generating profile reports..."
    go tool pprof -text -output="$PROFILE_DIR/cpu_profile.txt" "$PROFILE_DIR/cpu.prof"
    go tool pprof -text -output="$PROFILE_DIR/mem_profile.txt" "$PROFILE_DIR/mem.prof"
    go tool pprof -alloc_space -text -output="$PROFILE_DIR/alloc_profile.txt" "$PROFILE_DIR/mem.prof"

    echo -e "${GREEN}✓${NC} Test 5 PASSED: Profiling complete"
    echo "CPU Profile: $PROFILE_DIR/cpu_profile.txt"
    echo "Memory Profile: $PROFILE_DIR/mem_profile.txt"
    echo "Allocation Profile: $PROFILE_DIR/alloc_profile.txt"
    echo ""

    # Show top 10 CPU hot functions
    echo "Top 10 CPU-intensive functions:"
    head -20 "$PROFILE_DIR/cpu_profile.txt" | grep -v "^$"
    echo ""

    # Show top 10 memory allocators
    echo "Top 10 memory allocation hot spots:"
    head -20 "$PROFILE_DIR/alloc_profile.txt" | grep -v "^$"
    echo ""
fi

# Summary
echo "========================================"
echo -e "${GREEN}ALL TESTS PASSED${NC}"
echo "========================================"
echo ""
echo "Phase 3 Load Testing Summary:"
echo "  ✓ 200 events/sec sustained load: PASS"
echo "  ✓ Burst traffic handling: PASS"
echo "  ✓ Adaptive vs static comparison: PASS"
echo "  ✓ Performance benchmarks: PASS"
if [ "$ENABLE_PROFILING" = "true" ]; then
    echo "  ✓ CPU & memory profiling: PASS"
fi
echo ""
echo "System is ready for Phase 3 production rollout."
echo ""
echo "Next steps:"
echo "  1. Review benchmark results in benchmark_results.txt"
if [ "$ENABLE_PROFILING" = "true" ]; then
    echo "  2. Analyze profiles in $PROFILE_DIR/"
fi
echo "  3. Enable adaptive rate limiting in staging"
echo "  4. Monitor metrics for 24-48 hours"
echo "  5. Gradual production rollout (10% → 50% → 100%)"
echo ""
