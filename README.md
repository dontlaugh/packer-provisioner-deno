# packer-provisioner-deno
WIP: run deno scripts to provision stuff with packer


## Installation

This provisioner is a Packer plugin. https://www.packer.io/docs/extending/plugins.html#installing-plugins

Build or download this plugin and place in

```
$HOME/.packer.d/plugins
```

You may need to create the plugins directory


## Tests

Packer has a docker `builder`. This is a convenient way to test without launching
a cloud vm.

```
cd examples
packer build docker.json
```
