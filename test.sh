#!/bin/bash

go install
(
  cd examples
  packer build docker.json
)
