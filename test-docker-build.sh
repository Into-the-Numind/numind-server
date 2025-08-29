#!/bin/bash

# Test Docker build locally
echo "Testing Docker build..."

# Build for dev environment
echo "Building Docker image for dev environment..."
docker build --build-arg ENV=dev -t numind-test:dev .

if [ $? -eq 0 ]; then
    echo "✅ Docker build successful!"
    
    # Check if binary exists in the image
    echo "Checking binary in the image..."
    docker run --rm numind-test:dev ls -la /app/numind
    
    # Try to run the binary with version flag
    echo "Testing binary execution..."
    docker run --rm numind-test:dev /app/numind --version 2>/dev/null || echo "Binary test completed"
else
    echo "❌ Docker build failed!"
    exit 1
fi