// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.

package cli

import (
	"encoding"
	"fmt"
	"strconv"
	"strings"

	"github.com/aws/amazon-ecs-cli-v2/internal/pkg/aws/session"
	"github.com/aws/amazon-ecs-cli-v2/internal/pkg/config"
	"github.com/aws/amazon-ecs-cli-v2/internal/pkg/deploy/cloudformation"
	"github.com/aws/amazon-ecs-cli-v2/internal/pkg/docker/dockerfile"
	"github.com/aws/amazon-ecs-cli-v2/internal/pkg/manifest"
	"github.com/aws/amazon-ecs-cli-v2/internal/pkg/term/color"
	"github.com/aws/amazon-ecs-cli-v2/internal/pkg/term/log"
	termprogress "github.com/aws/amazon-ecs-cli-v2/internal/pkg/term/progress"
	"github.com/aws/amazon-ecs-cli-v2/internal/pkg/term/prompt"
	"github.com/aws/amazon-ecs-cli-v2/internal/pkg/workspace"
	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var (
	svcInitSvcTypePrompt        = "Which type of " + color.Emphasize("infrastructure pattern") + " best represents your service?"
	fmtSvcInitSvcTypeHelpPrompt = `A %s is a public, internet-facing, HTTP server that's behind a load balancer. 
To learn more see: https://git.io/JfIpv

A %s is a private, non internet-facing service.
To learn more see: https://git.io/JfIpT`

	fmtSvcInitSvcNamePrompt     = "What do you want to " + color.Emphasize("name") + " this %s?"
	fmtSvcInitSvcNameHelpPrompt = `The name will uniquely identify this service within your app %s.
Deployed resources (such as your service, logs) will contain this service's name and be tagged with it.`

	fmtSvcInitDockerfilePrompt  = "Which Dockerfile would you like to use for %s?"
	svcInitDockerfileHelpPrompt = "Dockerfile to use for building your service's container image."

	svcInitSvcPortPrompt     = "Which port do you want customer traffic sent to?"
	svcInitSvcPortHelpPrompt = `The port will be used by the load balancer to route incoming traffic to this service.
You should set this to the port which your Dockerfile uses to communicate with the internet.`
)

const (
	fmtAddSvcToAppStart    = "Creating ECR repositories for service %s."
	fmtAddSvcToAppFailed   = "Failed to create ECR repositories for service %s."
	fmtAddSvcToAppComplete = "Created ECR repositories for service %s."
)

const (
	fmtParsePortFromDockerfileStart    = "Parsing dockerfile at path %s for service %s...\n"
	parseFromDockerfileTooManyPorts    = "It looks like your Dockerfile exposes more than one port.\n"
	fmtParseFromDockerfileNoPort       = "Couldn't find an exposed port in dockerfile for service %s.\n"
	fmtParsePortFromDockerfileComplete = "It looks like your Dockerfile exposes port %s. We'll use that to route traffic to your container from your load balancer.\n"
)

const (
	defaultSvcPortString = "80"
)

type initSvcVars struct {
	*GlobalOpts
	ServiceType    string
	Name           string
	DockerfilePath string
	Port           uint16
}

type initSvcOpts struct {
	initSvcVars

	// Interfaces to interact with dependencies.
	fs          afero.Fs
	ws          svcManifestWriter
	store       store
	appDeployer appDeployer
	prog        progress
	df          dockerfileParser

	// Outputs stored on successful actions.
	manifestPath string

	// sets up Dockerfile parser using fs and input path
	setupParser func(*initSvcOpts)
}

func newInitSvcOpts(vars initSvcVars) (*initSvcOpts, error) {
	store, err := config.NewStore()
	if err != nil {
		return nil, fmt.Errorf("couldn't connect to config store: %w", err)
	}

	ws, err := workspace.New()
	if err != nil {
		return nil, fmt.Errorf("workspace cannot be created: %w", err)
	}

	p := session.NewProvider()
	sess, err := p.Default()
	if err != nil {
		return nil, err
	}

	return &initSvcOpts{
		initSvcVars: vars,

		fs:          &afero.Afero{Fs: afero.NewOsFs()},
		store:       store,
		ws:          ws,
		appDeployer: cloudformation.New(sess),
		prog:        termprogress.NewSpinner(),

		setupParser: func(o *initSvcOpts) {
			o.df = dockerfile.New(o.fs, o.DockerfilePath)
		},
	}, nil
}

// Validate returns an error if the flag values passed by the user are invalid.
func (o *initSvcOpts) Validate() error {
	if o.AppName() == "" {
		return errNoAppInWorkspace
	}
	if o.ServiceType != "" {
		if err := validateSvcType(o.ServiceType); err != nil {
			return err
		}
	}
	if o.Name != "" {
		if err := validateSvcName(o.Name); err != nil {
			return err
		}
	}
	if o.DockerfilePath != "" {
		if _, err := o.fs.Stat(o.DockerfilePath); err != nil {
			return err
		}
	}
	if o.Port != 0 {
		if err := validateSvcPort(o.Port); err != nil {
			return err
		}
	}
	return nil
}

// Ask prompts for fields that are required but not passed in.
func (o *initSvcOpts) Ask() error {
	if err := o.askSvcType(); err != nil {
		return err
	}
	if err := o.askSvcName(); err != nil {
		return err
	}
	if err := o.askDockerfile(); err != nil {
		return err
	}
	if err := o.askSvcPort(); err != nil {
		return err
	}

	return nil
}

// Execute writes the service's manifest file and stores the service in SSM.
func (o *initSvcOpts) Execute() error {
	app, err := o.store.GetApplication(o.AppName())
	if err != nil {
		return fmt.Errorf("get application %s: %w", o.AppName(), err)
	}

	manifestPath, err := o.createManifest()
	if err != nil {
		return err
	}
	o.manifestPath = manifestPath

	o.prog.Start(fmt.Sprintf(fmtAddSvcToAppStart, o.Name))
	if err := o.appDeployer.AddServiceToApp(app, o.Name); err != nil {
		o.prog.Stop(log.Serrorf(fmtAddSvcToAppFailed, o.Name))
		return fmt.Errorf("add service %s to application %s: %w", o.Name, o.AppName(), err)
	}
	o.prog.Stop(log.Ssuccessf(fmtAddSvcToAppComplete, o.Name))

	if err := o.store.CreateService(&config.Service{
		App:  o.AppName(),
		Name: o.Name,
		Type: o.ServiceType,
	}); err != nil {
		return fmt.Errorf("saving service %s: %w", o.Name, err)
	}
	return nil
}

func (o *initSvcOpts) createManifest() (string, error) {
	manifest, err := o.newManifest()
	if err != nil {
		return "", err
	}
	var manifestExists bool
	manifestPath, err := o.ws.WriteServiceManifest(manifest, o.Name)
	if err != nil {
		e, ok := err.(*workspace.ErrFileExists)
		if !ok {
			return "", err
		}
		manifestExists = true
		manifestPath = e.FileName
	}
	manifestPath, err = relPath(manifestPath)
	if err != nil {
		return "", err
	}

	log.Infoln()
	manifestMsgFmt := "Wrote the manifest for service %s at %s\n"
	if manifestExists {
		manifestMsgFmt = "Manifest file for service %s already exists at %s, skipping writing it.\n"
	}
	log.Successf(manifestMsgFmt, color.HighlightUserInput(o.Name), color.HighlightResource(manifestPath))
	log.Infoln("Your manifest contains configurations like your container size and ports.")
	log.Infoln()

	return manifestPath, nil
}

func (o *initSvcOpts) newManifest() (encoding.BinaryMarshaler, error) {
	switch o.ServiceType {
	case manifest.LoadBalancedWebServiceType:
		return o.newLoadBalancedWebServiceManifest()
	case manifest.BackendServiceType:
		return o.newBackendServiceManifest()
	default:
		return nil, fmt.Errorf("service type %s doesn't have a manifest", o.ServiceType)
	}
}

func (o *initSvcOpts) newLoadBalancedWebServiceManifest() (*manifest.LoadBalancedWebService, error) {
	props := &manifest.LoadBalancedWebServiceProps{
		ServiceProps: &manifest.ServiceProps{
			Name:       o.Name,
			Dockerfile: o.DockerfilePath,
		},
		Port: o.Port,
		Path: "/",
	}
	existingSvcs, err := o.store.ListServices(o.AppName())
	if err != nil {
		return nil, err
	}
	// We default to "/" for the first service, but if there's another
	// Load Balanced Web Service, we use the svc name as the default, instead.
	for _, existingSvc := range existingSvcs {
		if existingSvc.Type == manifest.LoadBalancedWebServiceType && existingSvc.Name != o.Name {
			props.Path = o.Name
			break
		}
	}
	return manifest.NewLoadBalancedWebService(props), nil
}

func (o *initSvcOpts) newBackendServiceManifest() (*manifest.BackendService, error) {
	return manifest.NewBackendService(manifest.BackendServiceProps{
		ServiceProps: manifest.ServiceProps{
			Name:       o.Name,
			Dockerfile: o.DockerfilePath,
		},
		Port: o.Port,
	}), nil
}

func (o *initSvcOpts) askSvcType() error {
	if o.ServiceType != "" {
		return nil
	}

	help := fmt.Sprintf(fmtSvcInitSvcTypeHelpPrompt,
		manifest.LoadBalancedWebServiceType,
		manifest.BackendServiceType,
	)
	t, err := o.prompt.SelectOne(svcInitSvcTypePrompt, help, manifest.ServiceTypes)
	if err != nil {
		return fmt.Errorf("select service type: %w", err)
	}
	o.ServiceType = t
	return nil
}

func (o *initSvcOpts) askSvcName() error {
	if o.Name != "" {
		return nil
	}

	name, err := o.prompt.Get(
		fmt.Sprintf(fmtSvcInitSvcNamePrompt, color.HighlightUserInput(o.ServiceType)),
		fmt.Sprintf(fmtSvcInitSvcNameHelpPrompt, o.AppName()),
		validateSvcName)
	if err != nil {
		return fmt.Errorf("get service name: %w", err)
	}
	o.Name = name
	return nil
}

// askDockerfile prompts for the Dockerfile by looking at sub-directories with a Dockerfile.
// If the user chooses to enter a custom path, then we prompt them for the path.
func (o *initSvcOpts) askDockerfile() error {
	if o.DockerfilePath != "" {
		return nil
	}

	// TODO https://github.com/aws/amazon-ecs-cli-v2/issues/206
	dockerfiles, err := listDockerfiles(o.fs, ".")
	if err != nil {
		return err
	}

	sel, err := o.prompt.SelectOne(
		fmt.Sprintf(fmtSvcInitDockerfilePrompt, color.HighlightUserInput(o.Name)),
		svcInitDockerfileHelpPrompt,
		dockerfiles,
	)
	if err != nil {
		return fmt.Errorf("select Dockerfile: %w", err)
	}

	o.DockerfilePath = sel

	return nil
}

func (o *initSvcOpts) askSvcPort() error {
	// Use flag before anything else
	if o.Port != 0 {
		return nil
	}

	log.Infof(fmtParsePortFromDockerfileStart,
		color.HighlightUserInput(o.DockerfilePath),
		color.HighlightUserInput(o.Name),
	)

	o.setupParser(o)
	ports, err := o.df.GetExposedPorts()
	// Ignore any errors in dockerfile parsing--we'll use the default instead.
	if err != nil {
		log.Debugln(err.Error())
	}
	var defaultPort = defaultSvcPortString
	switch len(ports) {
	case 0:
		log.Infof(fmtParseFromDockerfileNoPort,
			color.HighlightUserInput(o.Name),
		)
	case 1:
		o.Port = ports[0]
		log.Successf(fmtParsePortFromDockerfileComplete,
			color.HighlightUserInput(strconv.Itoa(int(o.Port))),
		)
		return nil
	default:
		defaultPort = strconv.Itoa(int(ports[0]))
		log.Infoln(parseFromDockerfileTooManyPorts)
	}

	port, err := o.prompt.Get(
		fmt.Sprintf(svcInitSvcPortPrompt),
		fmt.Sprintf(svcInitSvcPortHelpPrompt),
		validateSvcPort,
		prompt.WithDefaultInput(defaultPort),
	)
	if err != nil {
		return fmt.Errorf("get port: %w", err)
	}

	portUint, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return fmt.Errorf("parse port string: %w", err)
	}

	o.Port = uint16(portUint)

	return nil
}

// RecommendedActions returns follow-up actions the user can take after successfully executing the command.
func (o *initSvcOpts) RecommendedActions() []string {
	return []string{
		fmt.Sprintf("Update your manifest %s to change the defaults.", color.HighlightResource(o.manifestPath)),
		fmt.Sprintf("Run %s to deploy your service to a %s environment.",
			color.HighlightCode(fmt.Sprintf("copilot svc deploy --name %s --env %s", o.Name, defaultEnvironmentName)),
			defaultEnvironmentName),
	}
}

// BuildSvcInitCmd build the command for creating a new service.
func BuildSvcInitCmd() *cobra.Command {
	vars := initSvcVars{
		GlobalOpts: NewGlobalOpts(),
	}
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Creates a new service in an application.",
		Long: `Creates a new service in an application.
This command is also run as part of "copilot init".`,
		Example: `
  Create a "frontend" load balanced web service.
  /code $ copilot svc init --name frontend --svc-type "Load Balanced Web Service" --dockerfile ./frontend/Dockerfile

  Create a "subscribers" backend service.
  /code $ copilot svc init --name subscribers --svc-type "Backend Service"`,
		RunE: runCmdE(func(cmd *cobra.Command, args []string) error {
			opts, err := newInitSvcOpts(vars)
			if err != nil {
				return err
			}
			if err := opts.Validate(); err != nil { // validate flags
				return err
			}
			log.Warningln("It's best to run this command in the root of your workspace.")
			if err := opts.Ask(); err != nil {
				return err
			}
			if err := opts.Execute(); err != nil {
				return err
			}
			log.Infoln("Recommended follow-up actions:")
			for _, followup := range opts.RecommendedActions() {
				log.Infof("- %s\n", followup)
			}
			return nil
		}),
	}
	cmd.Flags().StringVarP(&vars.Name, nameFlag, nameFlagShort, "", svcFlagDescription)
	cmd.Flags().StringVarP(&vars.ServiceType, svcTypeFlag, svcTypeFlagShort, "", svcTypeFlagDescription)
	cmd.Flags().StringVarP(&vars.DockerfilePath, dockerFileFlag, dockerFileFlagShort, "", dockerFileFlagDescription)
	cmd.Flags().Uint16Var(&vars.Port, svcPortFlag, 0, svcPortFlagDescription)

	// Bucket flags by service type.
	requiredFlags := pflag.NewFlagSet("Required Flags", pflag.ContinueOnError)
	requiredFlags.AddFlag(cmd.Flags().Lookup(nameFlag))
	requiredFlags.AddFlag(cmd.Flags().Lookup(svcTypeFlag))
	requiredFlags.AddFlag(cmd.Flags().Lookup(dockerFileFlag))

	lbWebSvcFlags := pflag.NewFlagSet(manifest.LoadBalancedWebServiceType, pflag.ContinueOnError)
	lbWebSvcFlags.AddFlag(cmd.Flags().Lookup(svcPortFlag))

	backendSvcFlags := pflag.NewFlagSet(manifest.BackendServiceType, pflag.ContinueOnError)
	backendSvcFlags.AddFlag(cmd.Flags().Lookup(svcPortFlag))

	cmd.Annotations = map[string]string{
		// The order of the sections we want to display.
		"sections":                          fmt.Sprintf(`Required,%s`, strings.Join(manifest.ServiceTypes, ",")),
		"Required":                          requiredFlags.FlagUsages(),
		manifest.LoadBalancedWebServiceType: lbWebSvcFlags.FlagUsages(),
		manifest.BackendServiceType:         lbWebSvcFlags.FlagUsages(),
	}
	cmd.SetUsageTemplate(`{{h1 "Usage"}}{{if .Runnable}}
  {{.UseLine}}{{end}}{{$annotations := .Annotations}}{{$sections := split .Annotations.sections ","}}{{if gt (len $sections) 0}}

{{range $i, $sectionName := $sections}}{{h1 (print $sectionName " Flags")}}
{{(index $annotations $sectionName) | trimTrailingWhitespaces}}{{if ne (inc $i) (len $sections)}}

{{end}}{{end}}{{end}}{{if .HasAvailableInheritedFlags}}

{{h1 "Global Flags"}}
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasExample}}

{{h1 "Examples"}}{{code .Example}}{{end}}
`)
	return cmd
}
