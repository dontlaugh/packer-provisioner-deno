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
			return fmt.Errorf("Error installing Goss: %s", err)
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
	ui.Message(fmt.Sprintf("Installing deno"))

	// Command to emulate:
	// curl -fsSL https://deno.land/x/install/install.sh | sh
	bootstrapURL := "https://deno.land/x/install/install.sh"
	cmd := packer.RemoteCmd{
		// Fallback on wget if curl failed for any reason (such as not being installed)
		Command: fmt.Sprintf(
			"curl -L %s %s -o %s %s || wget %s %s -O %s %s",
			p.sslFlag("curl"), p.userPass("curl"), p.config.DownloadPath, bootstrapURL,
			p.sslFlag("wget"), p.userPass("wget"), p.config.DownloadPath, bootstrapURL),
	}
	ui.Message(fmt.Sprintf("Downloading deno installer script to %s", p.config.DownloadPath))
	if err := comm.Start(ctx, &cmd); err != nil {
		return fmt.Errorf("installer script download: %v", err)
	}
	cmd.Wait()
	if cmd.ExitStatus() != 0 {
		return fmt.Errorf("downloading installer script: non-zero exit status")
	}

	cmd = packer.RemoteCmd{
		Command: fmt.Sprintf("sh -c '%s'", p.config.DownloadPath),
	}
	if err := comm.Start(ctx, &cmd); err != nil {
		return fmt.Errorf("installer script execute: %v", err)
	}
	cmd.Wait()
	if cmd.ExitStatus() != 0 {
		return fmt.Errorf("installer script execute: non-zero exit status")
	}

	return nil
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

func (p *Provisioner) sslFlag(cmdType string) string {
	if p.config.SkipSSLChk {
		switch(cmdType) {
		case "curl":
			return "-k"
		case "wget":
			return "--no-check-certificate"
		default:
			return ""
		}
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

// Deal with curl & wget username and password
func (p *Provisioner) userPass(cmdType string) string {
	if p.config.Username != "" {
		switch(cmdType) {
		case "curl":
			if p.config.Password == "" {
				return fmt.Sprintf("-u %s", p.config.Username)
			}
			return fmt.Sprintf("-u %s:%s", p.config.Username, p.config.Password)
		case "wget":
			if p.config.Password == "" {
				return fmt.Sprintf("--user=%s", p.config.Username)
			}
			return fmt.Sprintf("--user=%s --password=%s", p.config.Username, p.config.Password)
		default:
			return  ""
		}
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
