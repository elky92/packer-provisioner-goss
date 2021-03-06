//go:generate mapstructure-to-hcl2 -type Config

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
    "strings"

	"github.com/hashicorp/hcl/v2/hcldec"
	"github.com/hashicorp/packer-plugin-sdk/packer"
	"github.com/hashicorp/packer-plugin-sdk/plugin"
	"github.com/hashicorp/packer-plugin-sdk/template/config"
	"github.com/hashicorp/packer-plugin-sdk/template/interpolate"
)

const (
    linux             = "Linux"
    windows           = "Windows"
    version           = "1.0.0"
    arch              = "amd64"
)

// Config holds the config data coming in from the packer template
type Config struct {
	// Goss installation
	Version      string
	Arch         string
	URL          string
	Username     string
	Password     string
    Debug        bool
    Inspect      bool
	Tests        []string

    DownloadPath string  `mapstructure:"download_path"`
    TargetOs     string  `mapstructure:"target_os"`

    // Goss options for retry and timeouts
    RetryTimeout string `mapstructure:"retry_timeout"`
    Sleep        string `mapstructure:"sleep"`

    // Skip Install
    SkipInstall  bool    `mapstructure:"skip_install"`

	// Use Sudo
	UseSudo bool `mapstructure:"use_sudo"`

	// skip ssl check flag
	SkipSSLChk bool `mapstructure:"skip_ssl"`

	// The --gossfile flag
	GossFile string `mapstructure:"goss_file"`

	// The --vars flag
	// Optional file containing variables, used within GOSS templating.
	// Must be one of the files contained in the Tests array.
	// Can be YAML or JSON.
	VarsFile string `mapstructure:"vars_file"`

    // Optional env variables
    VarsEnv map[string]string `mapstructure:"vars_env"`

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

    // The format options to use for printing test output
    // Available: [perfdata verbose pretty]
    // Default:   verbose
    FormatOptions string `mapstructure:"format_options"`

	ctx interpolate.Context
}

var validFormats = []string{"documentation", "json", "json_oneline", "junit", "nagios", "nagios_verbose", "rspecish", "silent", "tap"}
var validFormatOptions = []string{"perfdata", "verbose", "pretty"}

// Provisioner implements a packer Provisioner
type GossProvisioner struct {
	config Config
}

func main() {
	server, err := plugin.Server()
	if err != nil {
		panic(err)
	}
	server.RegisterProvisioner(new(GossProvisioner))
	server.Serve()
}

func (b *GossProvisioner) ConfigSpec() hcldec.ObjectSpec {
    return b.config.FlatMapstructure().HCL2Spec()
}

// Prepare gets the Goss Privisioner ready to run
func (p *GossProvisioner) Prepare(raws ...interface{}) error {
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
		p.config.Version = "0.3.9"
	}

	if p.config.Arch == "" {
		p.config.Arch = "amd64"
	}

    if p.config.TargetOs == "" {
        p.config.TargetOs = linux
    }

    if p.config.URL == "" {
        p.config.URL = p.getDownloadUrl()
    }

	if p.config.DownloadPath == "" {
        os := strings.ToLower(p.config.TargetOs)
        if p.config.URL != "" {
            p.config.DownloadPath = fmt.Sprintf("/tmp/goss-%s-%s-%s", version, os, arch)
        } else {
		    p.config.DownloadPath = fmt.Sprintf("/tmp/goss-%s-linux-%s", p.config.Version, p.config.Arch)
	    }
    }

	if p.config.RemoteFolder == "" {
		p.config.RemoteFolder = "/tmp"
	}

	if p.config.RemotePath == "" {
		p.config.RemotePath = fmt.Sprintf("%s/goss", p.config.RemoteFolder)
	}

	if p.config.Tests == nil {
		p.config.Tests = make([]string, 0)
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

    if p.config.FormatOptions != "" {
        valid := false
        for _, candidate := range validFormatOptions {
            if p.config.FormatOptions == candidate {
                valid = true
                break
            }
        }
        if !valid {
            errs = packer.MultiErrorAppend(errs,
                fmt.Errorf("Invalid format options choice %s. Valid options: %v",
                    p.config.FormatOptions, validFormatOptions))
        }
    }

	if len(p.config.Tests) == 0 {
		errs = packer.MultiErrorAppend(errs,
			errors.New("tests must be specified"))
	}

	for _, path := range p.config.Tests {
		if _, err := os.Stat(path); err != nil {
			errs = packer.MultiErrorAppend(errs,
				fmt.Errorf("Bad test '%s': %s", path, err))
		}
	}

    if p.config.TargetOs != "" {
        p.config.TargetOs = linux
    }

	if errs != nil && len(errs.Errors) > 0 {
		return errs
	}

	return nil
}

// Provision runs the Goss GossProvisioner
func (p *GossProvisioner) Provision(ctx context.Context, ui packer.Ui, comm packer.Communicator, generatedData map[string]interface{}) error {
	ui.Say("Provisioning with Goss")
    ui.Say(fmt.Sprintf("Configured to run on %s", string(p.config.TargetOs)))

	if !p.config.SkipInstall {
		if err := p.installGoss(ui, comm); err != nil {
			return fmt.Errorf("Error installing Goss: %s", err)
		}
	} else {
		ui.Message("Skipping Goss installation")
	}

	ui.Say("Uploading goss tests...")
	if err := p.createDir(ui, comm, p.config.RemotePath); err != nil {
		return fmt.Errorf("Error creating remote directory: %s", err)
	}

	if p.config.VarsFile != "" {
		vf, err := os.Stat(p.config.VarsFile)
		if err != nil {
			return fmt.Errorf("Error stating file: %s", err)
		}
		if vf.Mode().IsRegular() {
			ui.Message(fmt.Sprintf("Uploading vars file %s", p.config.VarsFile))
			varsDest := filepath.ToSlash(filepath.Join(p.config.RemotePath, filepath.Base(p.config.VarsFile)))
			if err := p.uploadFile(ui, comm, varsDest, p.config.VarsFile); err != nil {
				return fmt.Errorf("Error uploading vars file: %s", err)
			}
		}
	}

    if len(p.config.VarsEnv) != 0 {
        ui.Message(fmt.Sprintf("Env variables are %s", p.envVars()))
    }

	for _, src := range p.config.Tests {
		s, err := os.Stat(src)
		if err != nil {
			return fmt.Errorf("Error stating file: %s", err)
		}

		if s.Mode().IsRegular() {
			ui.Message(fmt.Sprintf("Uploading %s", src))
			dst := filepath.ToSlash(filepath.Join(p.config.RemotePath, filepath.Base(src)))
			if err := p.uploadFile(ui, comm, dst, src); err != nil {
				return fmt.Errorf("Error uploading goss test: %s", err)
			}
		} else if s.Mode().IsDir() {
			ui.Message(fmt.Sprintf("Uploading Dir %s", src))
			dst := filepath.ToSlash(filepath.Join(p.config.RemotePath, filepath.Base(src)))
			if err := p.uploadDir(ui, comm, dst, src); err != nil {
				return fmt.Errorf("Error uploading goss test: %s", err)
			}
		} else {
			ui.Message(fmt.Sprintf("Ignoring %s... not a regular file", src))
		}
	}

	ui.Say("Running goss tests...")
	if err := p.runGoss(ui, comm); err != nil {
		return fmt.Errorf("Error running Goss: %s", err)
	}

	return nil
}

// Cancel just exists when provision is cancelled
func (p *GossProvisioner) Cancel() {
	os.Exit(0)
}

// installGoss downloads the Goss binary on the remote host
func (p *GossProvisioner) installGoss(ui packer.Ui, comm packer.Communicator) error {
	ui.Message(fmt.Sprintf("Installing Goss from, %s", p.config.URL))
	ctx := context.TODO()

	cmd := &packer.RemoteCmd{
		// Fallback on wget if curl failed for any reason (such as not being installed)
		Command: fmt.Sprintf(
			"curl -L %s %s -o %s %s || wget %s %s -O %s %s",
			p.sslFlag("curl"), p.userPass("curl"), p.config.DownloadPath, p.config.URL,
			p.sslFlag("wget"), p.userPass("wget"), p.config.DownloadPath, p.config.URL),
	}
	ui.Message(fmt.Sprintf("Downloading Goss to %s", p.config.DownloadPath))
	if err := cmd.RunWithUi(ctx, comm, ui); err != nil {
		return fmt.Errorf("Unable to download Goss: %s", err)
	}
	cmd = &packer.RemoteCmd{
		Command: fmt.Sprintf("chmod 555 %s && %s --version", p.config.DownloadPath, p.config.DownloadPath),
	}
	if err := cmd.RunWithUi(ctx, comm, ui); err != nil {
		return fmt.Errorf("Unable to install Goss: %s", err)
	}

	return nil
}

// runGoss runs the Goss tests
func (p *GossProvisioner) runGoss(ui packer.Ui, comm packer.Communicator) error {
	goss := fmt.Sprintf("%s", p.config.DownloadPath)
	ctx := context.TODO()

	cmd := &packer.RemoteCmd{
		Command: fmt.Sprintf(
			"cd %s && %s %s %s %s %s validate %s",
			p.config.RemotePath, p.enableSudo(), goss, p.config.GossFile, p.vars(), p.debug(), p.format()),
	}
	if err := cmd.RunWithUi(ctx, comm, ui); err != nil {
		return err
	}
	if cmd.ExitStatus() != 0 {
		return fmt.Errorf("goss non-zero exit status")
	}
	ui.Say(fmt.Sprintf("Goss tests ran successfully"))
	return nil
}

// runGoss tests and render goss commands.
func (p *GossProvisioner) runGossCmd(ui packer.Ui, comm packer.Communicator, cmd *packer.RemoteCmd, message string) error {
    ctx := context.TODO()
    if err := cmd.RunWithUi(ctx, comm, ui); err != nil {
        return err
    }
    if cmd.ExitStatus() != 0 {
        // Inspect mode is on. Report failure but don't fail.
        if p.config.Inspect {
            ui.Say(fmt.Sprintf("Goss %s failed", message))
            ui.Say(fmt.Sprintf("Inpect mode on : proceeding without failing Packer"))
        } else {
            return fmt.Errorf("goss non-zero exit status")
        }
    } else {
        ui.Say(fmt.Sprintf("Goss %s ran successfully", message))
    }
    return nil
}

func (p *GossProvisioner) retryTimeout() string {
    if p.config.RetryTimeout == "" {
        return "0s" // goss default
    }
    return p.config.RetryTimeout
}

func (p *GossProvisioner) sleep() string {
    if p.config.Sleep == "" {
        return "1s" // goss default
    }
    return p.config.Sleep
}

// debug returns the debug flag if debug is configured
func (p *GossProvisioner) debug() string {
	if p.config.Debug {
		return "-d"
	}
	return ""
}

func (p *GossProvisioner) format() string {
	if p.config.Format != "" {
		return fmt.Sprintf("-f %s", p.config.Format)
	}
	return ""
}

func (p *GossProvisioner) formatOptions() string {
    if p.config.FormatOptions != "" {
        return fmt.Sprintf("-o %s", p.config.FormatOptions)
    }
    return ""
}

func (p *GossProvisioner) vars() string {
	if p.config.VarsFile != "" {
		return fmt.Sprintf("--vars %s", filepath.ToSlash(filepath.Join(p.config.RemotePath, filepath.Base(p.config.VarsFile))))
	}
	return ""
}

func (p *GossProvisioner) sslFlag(cmdType string) string {
	if p.config.SkipSSLChk {
		switch cmdType {
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

func (p *GossProvisioner) getDownloadUrl() string {
    os := strings.ToLower(string(p.config.TargetOs))
    filename := fmt.Sprintf("goss-%s-%s", os, p.config.Arch)

    if p.isGossAlpha() {
        filename = fmt.Sprintf("goss-alpha-%s-%s", os, p.config.Arch)
    }

    if p.config.TargetOs == windows {
        filename = fmt.Sprintf("%s.exe", filename)
    }

    return fmt.Sprintf("https://github.com/aelsabbahy/goss/releases/download/v%s/%s", p.config.Version, filename)
}

func (p *GossProvisioner) isGossAlpha() bool {
    return p.config.VarsEnv["GOSS_USE_ALPHA"] == "1"
}

func (p *GossProvisioner) envVars() string {
    var sb strings.Builder
    for env_var, value := range p.config.VarsEnv {
        switch p.config.TargetOs {
        case windows:
            // Windows requires a call to "set" as separate command seperated by && for each env variable
            sb.WriteString(fmt.Sprintf("set \"%s=%s\" && ", env_var, value))
        default:
            sb.WriteString(fmt.Sprintf("%s=\"%s\" ", env_var, value))
        }

    }
    return sb.String()
}

// enable sudo if required
func (p *GossProvisioner) enableSudo() string {
	if p.config.UseSudo {
		return "sudo"
	}
	return ""
}

// Deal with curl & wget username and password
func (p *GossProvisioner) userPass(cmdType string) string {
	if p.config.Username != "" {
		switch cmdType {
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
			return ""
		}
	}
	return ""
}

func (p *GossProvisioner) mkDir(dir string) string {
    switch p.config.TargetOs {
    case windows:
        return fmt.Sprintf("powershell /c mkdir -p '%s'", dir)
    default:
        return fmt.Sprintf("mkdir -p '%s'", dir)
    }
}

// createDir creates a directory on the remote server
func (p *GossProvisioner) createDir(ui packer.Ui, comm packer.Communicator, dir string) error {
	ui.Message(fmt.Sprintf("Creating directory: %s", dir))
	ctx := context.TODO()

	cmd := &packer.RemoteCmd{
		Command: fmt.Sprintf("mkdir -p '%s'", dir),
	}
	if err := cmd.RunWithUi(ctx, comm, ui); err != nil {
		return err
	}
	if cmd.ExitStatus() != 0 {
		return fmt.Errorf("non-zero exit status")
	}
	return nil
}

// uploadFile uploads a file
func (p *GossProvisioner) uploadFile(ui packer.Ui, comm packer.Communicator, dst, src string) error {
	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("Error opening: %s", err)
	}
	defer f.Close()

	if err = comm.Upload(dst, f, nil); err != nil {
		return fmt.Errorf("Error uploading %s: %s", src, err)
	}
	return nil
}

// uploadDir uploads a directory
func (p *GossProvisioner) uploadDir(ui packer.Ui, comm packer.Communicator, dst, src string) error {
	var ignore []string
	if err := p.createDir(ui, comm, dst); err != nil {
		return err
	}

	if src[len(src)-1] != '/' {
		src = src + "/"
	}
	return comm.UploadDir(dst, src, ignore)
}
