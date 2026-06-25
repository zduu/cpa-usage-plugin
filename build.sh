#!/bin/bash
set -e

echo "Building CPA Usage Statistics Plugin..."

# Build Go plugin
cd go
CGO_ENABLED=1 go build -buildmode=c-shared -buildvcs=false -o ../usage-statistics.so main.go
cd ..
rm -f usage-statistics.h

echo "Plugin built successfully: usage-statistics.so"
echo ""
echo "To install:"
echo "1. Copy usage-statistics.so to your CLIProxyAPI plugins directory"
echo "2. Enable the plugin in config.yaml:"
echo ""
echo "   plugins:"
echo "     enabled: true"
echo "     configs:"
echo "       usage-statistics:"
echo "         enabled: true"
echo ""
echo "3. Restart CLIProxyAPI"
