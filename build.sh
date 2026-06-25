#!/bin/bash
set -e

echo "Building CPA Usage Statistics Plugin..."

# Build Go plugin
cd go
go build -buildmode=plugin -o ../usage-statistics.so main.go
cd ..

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
