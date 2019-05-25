package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hashicorp/packer/helper/config"
	"github.com/hashicorp/packer/packer"
	"github.com/hashicorp/packer/packer/plugin"
	"github.com/hashicorp/packer/template/interpolate"
)

// DenoConfig maps the config data from the Packer provisioner stanza.
type DenoConfig struct {
	Username    string
	Password    string
	SkipInstall bool
	// For testing purposes, we can skip provisioning and just look at how deno was installed
	SkipProvision bool `mapstructure:"skip_provision"`

	// The destination folder for uploaded Deno scripts.
	RemoteFolder string `mapstructure:"remote_folder"`

	// A slice of scripts to compile and run.
	Scripts []string

	// path to the deno executable
	denoExecutable string

	ctx interpolate.Context
}

// Provisioner implements a Packer Provisioner
type Provisioner struct {
	config DenoConfig
}

func main() {
	server, err := plugin.Server()
	if err != nil {
		panic(err)
	}
	err = server.RegisterProvisioner(new(Provisioner))
	if err != nil {
		panic(err)
	}
	server.Serve()
}

// Prepare parses and validates our provisioner config.
func (p *Provisioner) Prepare(raws ...interface{}) error {
	err := config.Decode(&p.config, &config.DecodeOpts{
		Interpolate:        true,
		InterpolateContext: &p.config.ctx,
		InterpolateFilter: &interpolate.RenderFilter{
			Exclude: []string{},
		},
	}, raws...)
	if err != nil {
		return err
	}

	if p.config.RemoteFolder == "" {
		p.config.RemoteFolder = "/tmp/packer-deno"
	}

	if p.config.Scripts == nil {
		p.config.Scripts = make([]string, 0)
	}

	// TODO find a way to install deno to different places/users/globally
	p.config.denoExecutable = "/root/.deno/bin/deno"

	var errs *packer.MultiError

	if len(p.config.Scripts) == 0 {
		errs = packer.MultiErrorAppend(errs,
			errors.New("at least one script must be specified"))
	}

	for _, path := range p.config.Scripts {
		if _, err := os.Stat(path); err != nil {
			errs = packer.MultiErrorAppend(errs,
				fmt.Errorf("bad script '%s': %s", path, err))
		}
	}

	if errs != nil && len(errs.Errors) > 0 {
		return errs
	}

	return nil
}

// Provision runs the Deno Provisioner.
func (p *Provisioner) Provision(ctx context.Context, ui packer.Ui, comm packer.Communicator) error {
	ui.Say("Provisioning with Deno")

	if !p.config.SkipInstall {
		if err := p.installDeno(ctx, ui, comm); err != nil {
			return fmt.Errorf("error installing deno: %s", err)
		}
	} else {
		ui.Message("Skipping Deno installation")
	}

	// TODO: compile deno bundles locally, before upload
	// Once built-in bundling is available, this will become a lot easier:
	// https://github.com/denoland/deno/issues/2357

	ui.Say("Uploading deno scripts...")
	if err := p.createDir(ctx, ui, comm, p.config.RemoteFolder); err != nil {
		return fmt.Errorf("error creating remote directory: %s", err)
	}

	var remoteScripts []string

	for _, src := range p.config.Scripts {
		s, err := os.Stat(src)
		if err != nil {
			return fmt.Errorf("stat error: %s", err)
		}

		if s.Mode().IsRegular() {
			ui.Message(fmt.Sprintf("Uploading %s", src))
			dst := filepath.ToSlash(filepath.Join(p.config.RemoteFolder, filepath.Base(src)))
			if err := p.uploadFile(ctx, ui, comm, dst, src); err != nil {
				return fmt.Errorf("error uploading deno script: %s", err)
			}
			remoteScripts = append(remoteScripts, dst)
		} else if s.Mode().IsDir() {
			return fmt.Errorf("%s is a directory, expected deno script", src)
		} else {
			return fmt.Errorf("%s is not a regular file", src)
		}
	}
	if !p.config.SkipProvision {

		ui.Say("Running provisioning scripts")
		for _, script := range remoteScripts {
			if err := p.runDeno(ctx, ui, comm, script); err != nil {
				return fmt.Errorf("error running deno: %s", err)
			}
		}
	} else {
		ui.Say("Skipping provisioning scripts")
	}

	return nil
}

// Cancel just exists when provision is cancelled
func (p *Provisioner) Cancel() {
	os.Exit(0)
}

// installDeno installs deno on the remote host using the public installer script.
func (p *Provisioner) installDeno(ctx context.Context, ui packer.Ui, comm packer.Communicator) error {

	var cmd packer.RemoteCmd
	cmd = packer.RemoteCmd{Command: "apt-get update"}
	ui.Message("Update package cache")
	if err := execRemoteCommand(ctx, comm, &cmd, ui, "update package cache"); err != nil {
		return err
	}

	// TODO: handle other systems when deno binaries for them are available
	cmd = packer.RemoteCmd{Command: "apt-get install -y curl"}
	ui.Message("Installing curl")
	if err := execRemoteCommand(ctx, comm, &cmd, ui, "installing curl"); err != nil {
		return err
	}

	bootstrapURL := "https://deno.land/x/install/install.sh"
	cmd = packer.RemoteCmd{Command: fmt.Sprintf("curl -fsSL %s | sh", bootstrapURL)}
	ui.Message("Downloading and executing deno installer script")
	if err := execRemoteCommand(ctx, comm, &cmd, ui, "installer script"); err != nil {
		return err
	}

	return nil
}

func execRemoteCommand(ctx context.Context, comm packer.Communicator, cmd *packer.RemoteCmd, ui packer.Ui, msg string) error {
	if err := cmd.RunWithUi(ctx, comm, ui); err != nil {
		return fmt.Errorf("error %s: %v", msg, err)
	}
	if code := cmd.ExitStatus(); code != 0 {
		return fmt.Errorf("%s non-zero exit status: %v", msg, code)
	}
	return nil
}

// runDeno runs deno with our uploaded scripts
func (p *Provisioner) runDeno(ctx context.Context, ui packer.Ui, comm packer.Communicator, scriptPath string) error {
	commandString := fmt.Sprintf("%s run -A %s", p.config.denoExecutable, scriptPath)
	cmd := packer.RemoteCmd{
		Command: commandString}
	if err := execRemoteCommand(ctx, comm, &cmd, ui, commandString); err != nil {
		return err
	}
	return nil
}

// createDir creates a directory on the remote server
func (p *Provisioner) createDir(ctx context.Context, ui packer.Ui, comm packer.Communicator, dir string) error {
	ui.Message(fmt.Sprintf("Creating directory: %s", dir))
	cmd := packer.RemoteCmd{Command: fmt.Sprintf("mkdir -p '%s'", dir)}

	if err := execRemoteCommand(ctx, comm, &cmd, ui, "create dir"); err != nil {
		return err
	}

	return nil
}

// uploadFile uploads a file.
func (p *Provisioner) uploadFile(ctx context.Context, ui packer.Ui, comm packer.Communicator, dst, src string) error {
	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("error opening: %s", err)
	}
	if err = comm.Upload(dst, f, nil); err != nil {
		return fmt.Errorf("error uploading %s: %s", src, err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	return nil
}

// uploadDir uploads a directory
func (p *Provisioner) uploadDir(ctx context.Context, ui packer.Ui, comm packer.Communicator, dst, src string) error {
	var ignore []string
	if err := p.createDir(ctx, ui, comm, dst); err != nil {
		return err
	}

	// TODO: support Windows '\'
	if src[len(src)-1] != '/' {
		src = src + "/"
	}
	return comm.UploadDir(dst, src, ignore)
}
