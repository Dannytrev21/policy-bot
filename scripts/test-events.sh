#!/bin/bash

# Test script for HTTP and SQS event processing
# This script helps test both webhook and SQS event paths

set -e

echo "🚀 Policy Bot Event Processing Test"
echo "======================================"
echo

# Check if LocalStack is needed
USE_LOCALSTACK=${USE_LOCALSTACK:-true}

if [ "$USE_LOCALSTACK" = "true" ]; then
    echo "📦 Checking LocalStack..."
    
    # Check if LocalStack is running
    if curl -s http://localhost:4566 > /dev/null 2>&1; then
        echo "✅ LocalStack is running"
    else
        echo "❌ LocalStack is not running"
        echo "   Starting LocalStack..."
        echo "   Run: docker run --rm -d -p 4566:4566 --name localstack localstack/localstack"
        echo
        
        # Try to start LocalStack
        if command -v docker > /dev/null 2>&1; then
            echo "🐳 Starting LocalStack with Docker..."
            docker run --rm -d -p 4566:4566 --name localstack localstack/localstack
            
            # Wait for LocalStack to be ready
            echo "⏳ Waiting for LocalStack to start..."
            for i in {1..30}; do
                if curl -s http://localhost:4566 > /dev/null 2>&1; then
                    echo "✅ LocalStack is ready"
                    break
                fi
                sleep 2
                echo -n "."
            done
            echo
        else
            echo "⚠️  Docker not found. Install Docker and run:"
            echo "   docker run --rm -d -p 4566:4566 --name localstack localstack/localstack"
            echo
            echo "Or disable LocalStack testing with: USE_LOCALSTACK=false $0"
            exit 1
        fi
    fi
fi

echo "🔨 Building test application..."
cd "$(dirname "$0")/.."

# Build the test application
go build -o test-event-processing ./scripts/test-event-processing.go

echo "✅ Build complete"
echo

echo "🎯 Starting test server..."
echo "   - HTTP webhooks: http://localhost:8080/api/github/hook"
if [ "$USE_LOCALSTACK" = "true" ]; then
    echo "   - SQS queues: LocalStack at http://localhost:4566"
fi
echo
echo "Press Ctrl+C to stop the server"
echo

# Run the test application
./test-event-processing

# Cleanup
echo
echo "🧹 Cleaning up..."
rm -f test-event-processing

if [ "$USE_LOCALSTACK" = "true" ] && command -v docker > /dev/null 2>&1; then
    echo "🐳 Stopping LocalStack..."
    docker stop localstack 2>/dev/null || true
fi

echo "✅ Done"
