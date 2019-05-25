package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/hashicorp/packer/helper/config"
	"github.com/hashicorp/packer/packer"
	"github.com/hashicorp/packer/packer/plugin"
	"github.com/hashicorp/packer/template/interpolate"
)

// GossConfig holds the config data coming in from the packer template
type GossConfig struct {
	// Deno version to download; NOTE: currently only the latest is available
	Version      string
	// Deno arch; NOTE: currently only amd64 is supported
	Arch         string

	// DownloadPath is the target location for the installer script right now, but should
	// eventually be our target location for deno installation itself.
	DownloadPath string
	Username     string
	Password     string
	SkipInstall  bool

	// Enable debug for goss (defaults to false)
	Debug bool

	// A slice of scripts to compile and run.
	Scripts []string

	// Use Sudo
	UseSudo bool `mapstructure:"use_sudo"`

	// skip ssl check flag
	SkipSSLChk   bool `mapstructure:"skip_ssl"`

	// The --gossfile flag
	GossFile string `mapstructure:"goss_file"`

	// The remote folder where the goss tests will be uploaded to.
	// This should be set to a pre-existing directory, it defaults to /tmp
	RemoteFolder string `mapstructure:"remote_folder"`

	// The remote path where the goss tests will be uploaded.
	// This defaults to remote_folder/goss
	RemotePath string `mapstructure:"remote_path"`

	// The format to use for test output
	// Available: [documentation json json_oneline junit nagios nagios_verbose rspecish silent tap]
	// Default:   rspecish
	Format string `mapstructure:"format"`

	ctx interpolate.Context
}

var validFormats = []string{"documentation", "json", "json_oneline", "junit", "nagios", "nagios_verbose", "rspecish", "silent", "tap"}

// Provisioner implements a packer Provisioner
type Provisioner struct {
	config GossConfig
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

	if p.config.Version == "" {
		p.config.Version = "0.6.0"
	}

	if p.config.Arch == "" {
		p.config.Arch = "amd64"
	}

	if p.config.DownloadPath == "" {
		p.config.DownloadPath = fmt.Sprintf("/tmp/deno")
	}

	if p.config.RemoteFolder == "" {
		p.config.RemoteFolder = "/tmp"
	}

	if p.config.RemotePath == "" {
		p.config.RemotePath = fmt.Sprintf("%s/.deno", p.config.RemoteFolder)
	}

	if p.config.Scripts == nil {
		p.config.Scripts = make([]string, 0)
	}

	if p.config.GossFile != "" {
		p.config.GossFile = fmt.Sprintf("--gossfile %s", p.config.GossFile)
	}

	var errs *packer.MultiError
	if p.config.Format != "" {
		valid := false
		for _, candidate := range validFormats {
			if p.config.Format == candidate {
				valid = true
				break
			}
		}
		if !valid {
			errs = packer.MultiErrorAppend(errs,
				fmt.Errorf("Invalid format choice %s. Valid options: %v",
					p.config.Format, validFormats))
		}
	}

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
	// Once built in bundling is available, this will become a lot easier:
	// https://github.com/denoland/deno/issues/2357

	ui.Say("Uploading deno scripts...")
	if err := p.createDir(ctx, ui, comm, p.config.RemotePath); err != nil {
		return fmt.Errorf("error creating remote directory: %s", err)
	}

	for _, src := range p.config.Scripts {
		s, err := os.Stat(src)
		if err != nil {
			return fmt.Errorf("stat error: %s", err)
		}

		if s.Mode().IsRegular() {
			ui.Message(fmt.Sprintf("Uploading %s", src))
			dst := filepath.ToSlash(filepath.Join(p.config.RemotePath, filepath.Base(src)))
			if err := p.uploadFile(ctx, ui, comm, dst, src); err != nil {
				return fmt.Errorf("error uploading deno script: %s", err)
			}
		} else if s.Mode().IsDir() {
			return fmt.Errorf("%s is a directory", src)
			//ui.Message(fmt.Sprintf("Uploading Dir %s", src))
			//dst := filepath.ToSlash(filepath.Join(p.config.RemotePath, filepath.Base(src)))
			//if err := p.uploadDir(ui, comm, dst, src); err != nil {
			//	return fmt.Errorf("error uploading deno script: %s", err)
			//}
		} else {
			ui.Message(fmt.Sprintf("Ignoring %s... not a regular file", src))
		}
	}

	ui.Say("Running goss tests...")
	if err := p.runGoss(ui, comm); err != nil {
		return fmt.Errorf("error running deno: %s", err)
	}

	return nil
}

// Cancel just exists when provision is cancelled
func (p *Provisioner) Cancel() {
	os.Exit(0)
}

// installDeno installs deno on the remote host.
func (p *Provisioner) installDeno(ctx context.Context, ui packer.Ui, comm packer.Communicator) error {

	var cmd packer.RemoteCmd
	cmd = packer.RemoteCmd{
		Command: "apt-get update",
	}
	ui.Message("Installing curl")
	if err := execRemoteCommand(ctx, comm, &cmd, ui, "installing curl"); err != nil {
		return err
	}

	cmd = packer.RemoteCmd{
		Command: "apt-get install -y curl",
	}
	ui.Message("Installing curl")
    if err := execRemoteCommand(ctx, comm, &cmd, ui, "installing curl"); err != nil {
    	return err
	}

	// Command to emulate:
	// curl -fsSL https://deno.land/x/install/install.sh | sh
	bootstrapURL := "https://deno.land/x/install/install.sh"
	cmd = packer.RemoteCmd{
		Command: fmt.Sprintf("curl -L %s -o %s %s", "curl", p.config.DownloadPath, bootstrapURL),
	}
	ui.Message(fmt.Sprintf("Downloading deno installer script to %s", p.config.DownloadPath))
	if err := execRemoteCommand(ctx, comm, &cmd, ui,"downloading installer script"); err != nil {
		return err
	}

	cmd = packer.RemoteCmd{
		Command: fmt.Sprintf("sh -c '%s'", p.config.DownloadPath),
	}

	if err := execRemoteCommand(ctx, comm, &cmd, ui,"executing installer script"); err != nil {
		return err
	}

	return nil
}

func execRemoteCommand(ctx context.Context, comm packer.Communicator, cmd *packer.RemoteCmd, ui packer.Ui, msg string) error {
	if err := cmd.RunWithUi(ctx, comm, ui); err != nil {
		return fmt.Errorf("error %s: %v",msg,  err)
	}
	if code := cmd.ExitStatus(); code != 0 {
		return fmt.Errorf("%s non-zero exit status: %v", msg, code)
	}
	return nil
}

func printReader(r io.Reader) string {
	b, err := ioutil.ReadAll(r)
	if err != nil {
		panic(err)
	}
	return string(b)
}



// runGoss runs the Goss tests
func (p *Provisioner) runGoss(ui packer.Ui, comm packer.Communicator) error {
	//goss := fmt.Sprintf("%s", p.config.DownloadPath)
	//cmd := &packer.RemoteCmd{
	//	Command: fmt.Sprintf(
	//		"cd %s && %s %s %s %s %s validate %s",
	//		p.config.RemotePath, p.enableSudo(), goss, p.config.GossFile, p.vars(), p.debug(), p.format()),
	//}
	//if err := cmd.StartWithUi(comm, ui); err != nil {
	//	return err
	//}
	//if cmd.ExitStatus != 0 {
	//	return fmt.Errorf("goss non-zero exit status")
	//}
	//ui.Say(fmt.Sprintf("Goss tests ran successfully"))
	return nil
}

// debug returns the debug flag if debug is configured
func (p *Provisioner) debug() string {
	if p.config.Debug {
		return "-d"
	}
	return ""
}

func (p *Provisioner) format() string {
	if p.config.Format != "" {
		return fmt.Sprintf("-f %s", p.config.Format)
	}
	return ""
}

// enable sudo if required
func (p *Provisioner) enableSudo() string {
	if p.config.UseSudo {
		return "sudo"
	}
	return ""
}

// createDir creates a directory on the remote server
func (p *Provisioner) createDir(ctx context.Context, ui packer.Ui, comm packer.Communicator, dir string) error {
	ui.Message(fmt.Sprintf("Creating directory: %s", dir))
	cmd := packer.RemoteCmd{
		Command: fmt.Sprintf("mkdir -p '%s'", dir),
	}

	if err := comm.Start(ctx, &cmd); err != nil {
		return err
	}

	cmd.Wait()

	if cmd.ExitStatus() != 0 {
		return fmt.Errorf("creating dir: non-zero exit status")
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

	if src[len(src)-1] != '/' {
		src = src + "/"
	}
	return comm.UploadDir(dst, src, ignore)
}
