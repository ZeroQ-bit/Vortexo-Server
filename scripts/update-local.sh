#!/bin/bash
# Update local Vortexo Server installation

echo "🔄 Updating Vortexo Server..."
echo ""

# Pull latest code
echo "📥 Fetching latest code..."
git fetch origin
git pull origin main

# Rebuild with version info
echo ""
echo "🔨 Rebuilding container with version info..."
./docker-build.sh

# Restart services
echo ""
echo "🚀 Restarting services..."
docker-compose up -d

# Wait for service to be ready
echo ""
echo "⏳ Waiting for service to start..."
sleep 10

# Show current version
echo ""
echo "✅ Update complete!"
echo ""
echo "Current version:"
curl -s http://localhost:8080/api/v1/version | jq .

echo ""
echo "🎉 Vortexo Server is now running the latest version!"
