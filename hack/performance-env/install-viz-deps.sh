#!/usr/bin/env bash
# Copyright 2026 The Karmada Authors.
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

set -o errexit
set -o nounset
set -o pipefail

echo "Checking and installing dependencies for visualization script..."

# Check if running as root
if [ "$EUID" -ne 0 ]; then
  echo "Please run as root or use sudo"
  exit 1
fi

# Install system dependencies (Debian/Ubuntu)
if command -v apt-get &> /dev/null; then
    echo "Updating package list..."
    apt-get update
    
    echo "Installing Python3 and pip..."
    apt-get install -y python3 python3-pip
else
    echo "Error: apt-get not found. This script currently supports Debian/Ubuntu systems."
    echo "Please install python3 and python3-pip manually."
    exit 1
fi

# Install Python libraries
echo "Installing matplotlib..."
# Try installing via apt first (more stable for system python)
if apt-get install -y python3-matplotlib; then
    echo "Successfully installed python3-matplotlib via apt"
else
    echo "Falling back to pip install..."
    # Use --break-system-packages if on newer Debian/Ubuntu versions that enforce PEP 668
    if pip3 install matplotlib --break-system-packages 2>/dev/null; then
        echo "Successfully installed matplotlib via pip (--break-system-packages)"
    else
        pip3 install matplotlib
    fi
fi

# Verify installation
echo "Verifying installation..."
if python3 -c "import matplotlib; print('matplotlib version:', matplotlib.__version__)" 2>/dev/null; then
    echo "✅ Dependencies installed successfully!"
    echo "You can now run the visualization script:"
    echo "python3 hack/performance-env/visualize-metrics.py --baseline <file> --current <file>"
else
    echo "❌ Verification failed. Please check the error messages above."
    exit 1
fi
