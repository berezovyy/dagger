#!/bin/bash
# Verification script for container control examples
# This script validates that all examples are syntactically correct

set -e

echo "=== Verifying Container Control Examples ==="
echo ""

EXAMPLES_DIR="$(dirname "$0")"
cd "$EXAMPLES_DIR/.."

echo "1. Checking TypeScript compilation..."
npx tsc --noEmit --skipLibCheck \
  examples/streaming-logs.ts \
  examples/status-monitoring.ts \
  examples/container-control.ts \
  examples/lifecycle-events.ts \
  examples/combined-features.ts

echo "✓ All examples compile successfully"
echo ""

echo "2. Checking example file structure..."
for example in streaming-logs status-monitoring container-control lifecycle-events combined-features; do
  if [ -f "examples/${example}.ts" ]; then
    echo "✓ examples/${example}.ts exists"
  else
    echo "✗ examples/${example}.ts missing"
    exit 1
  fi
done
echo ""

echo "3. Checking documentation..."
for doc in CONTAINER_CONTROL_GETTING_STARTED CONTAINER_CONTROL_API CONTAINER_CONTROL_TROUBLESHOOTING; do
  if [ -f "docs/${doc}.md" ]; then
    echo "✓ docs/${doc}.md exists"
  else
    echo "✗ docs/${doc}.md missing"
    exit 1
  fi
done
echo ""

echo "4. Checking test suite..."
if [ -f "src/api/test/container-grpc.spec.ts" ]; then
  echo "✓ src/api/test/container-grpc.spec.ts exists"
else
  echo "✗ src/api/test/container-grpc.spec.ts missing"
  exit 1
fi
echo ""

echo "5. Checking README files..."
for readme in examples/README.md CONTAINER_CONTROL_COMPLETE.md; do
  if [ -f "$readme" ]; then
    echo "✓ $readme exists"
  else
    echo "✗ $readme missing"
    exit 1
  fi
done
echo ""

echo "=== All verification checks passed! ==="
echo ""
echo "To run the examples:"
echo "  npx tsx examples/streaming-logs.ts"
echo "  npx tsx examples/status-monitoring.ts"
echo "  npx tsx examples/container-control.ts"
echo "  npx tsx examples/lifecycle-events.ts"
echo "  npx tsx examples/combined-features.ts"
echo ""
echo "To run the tests:"
echo "  npm test -- --grep 'Container gRPC'"
echo ""
