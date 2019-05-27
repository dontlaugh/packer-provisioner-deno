package main

var installCurlScript = `#!/bin/sh

# Run some OS detection first, maybe we do more sophisticated
# stuff with this later.

if [ -f /etc/os-release ]; then
    # freedesktop.org and systemd
    . /etc/os-release
    export OS=$NAME
    export VER=$VERSION_ID
elif type lsb_release >/dev/null 2>&1; then
    # linuxbase.org
    export OS=$(lsb_release -si)
    export VER=$(lsb_release -sr)
elif [ -f /etc/lsb-release ]; then
    # For some versions of Debian/Ubuntu without lsb_release command
    . /etc/lsb-release
    export OS=$DISTRIB_ID
    export VER=$DISTRIB_RELEASE
elif [ -f /etc/debian_version ]; then
    # Older Debian/Ubuntu/etc.
    export OS=Debian
    export VER=$(cat /etc/debian_version)
else
    # Fall back to uname, e.g. "Linux <version>", also works for BSD, etc.
    OS=$(uname -s)
    VER=$(uname -r)
fi

echo "OS DETECTED: $OS $VER"
echo ""

# Use our corny method of installing curl: look for
# well-known package managers and call them.

if ! [ -x "$(command -v curl)" ]; then
  echo "curl executable not detected"
  if [ -x "$(command -v apt-get)" ]; then
    echo 'using apt-get'
    apt-get update
    apt-get install -y curl
  elif [ -x "$(command -v yum)" ]; then
    echo "using yum"
    yum update
    yum install -y curl
  elif [ -x "$(command -v apk)" ]; then
    echo "using apk"
    apk add --no-cache curl
  else
    echo "package manager not detected"
    exit 1
  fi
fi

if ! [ -x "$(command -v curl)" ]; then
  echo "curl installed, but not available to our process"
  exit 1
fi

`

