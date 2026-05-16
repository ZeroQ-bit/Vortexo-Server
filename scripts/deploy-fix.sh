#!/bin/bash

# Vortexo Server - Quick Deploy to Cloud Server
# Fixes Apple TV Chillio IPTV playback hanging issue

set -e  # Exit on any error

SERVER="root@77.42.16.119"
REMOTE_PATH="/root/streamarr-pro"

echo "🚀 Vortexo Server - Deploying Playback Fix to Cloud Server"
echo "=================================================="
echo ""

# Build for Linux
echo "📦 Building server binary for Linux..."
GOOS=linux GOARCH=amd64 go build -o bin/server-linux cmd/server/main.go

if [ ! -f "bin/server-linux" ]; then
    echo "❌ Build failed - binary not created"
    exit 1
fi

echo "✓ Build successful"
echo ""

# Upload to server
echo "📤 Uploading binary to $SERVER..."
scp bin/server-linux "$SERVER:$REMOTE_PATH/bin/server"

if [ $? -ne 0 ]; then
    echo "❌ Upload failed"
    exit 1
fi

echo "✓ Upload successful"
echo ""

# Restart service on server
echo "🔄 Restarting Vortexo Server service on server..."
ssh "$SERVER" << 'ENDSSH'
    cd /root/streamarr
    
    # Check if running in Docker
    if docker ps | grep -q streamarr; then
        echo "  → Detected Docker deployment"
        docker-compose restart
    # Check if using systemd
    elif systemctl is-active --quiet streamarr; then
        echo "  → Detected systemd service"
        sudo systemctl restart streamarr
    # Manual process restart
    else
        echo "  → Killing existing process"
        pkill -f "bin/server" || true
        sleep 2
        echo "  → Starting new process"
        nohup ./bin/server > logs/server.log 2>&1 &
    fi
    
    echo "  ✓ Service restarted"
    
    # Wait for service to start
    sleep 3
    
    # Check if server is running
    if pgrep -f "bin/server" > /dev/null || docker ps | grep -q streamarr; then
        echo "  ✓ Server is running"
    else
        echo "  ⚠️  Warning: Could not confirm server is running"
    fi
ENDSSH

echo ""
echo "=================================================="
echo "✅ Deployment Complete!"
echo ""
echo "📊 Monitoring logs (Ctrl+C to exit):"
echo "   ssh $SERVER 'tail -f $REMOTE_PATH/logs/server.log | grep --color=always \"\\[PLAY\\]\"'"
echo ""
echo "🧪 Test playback from your Chillio IPTV app now"
echo "   Expected: Stream should start within 30-120 seconds"
echo ""
