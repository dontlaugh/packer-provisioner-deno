#!/bin/bash

set -e

# During development, we symlink from our packer plugins folder to our GOPATH
# /bin directory. This let's us make edits, re-run `go install`, and packer
# will pick them up.

# Since GOPATH vars can vary widely, you'll need to make the symlink yourself
# until we figure out a nicer way to develop. Something like this will work 
# after an initial `go install`:
#
#    ln -s $GOPATH/bin/packer-provisioner-deno $HOME/.packer.d/plugins/packer-provisioner-deno

# $GOPATH/bin should be on your $PATH
go install

(
  cd examples
  packer build docker-ubuntu.json
)


