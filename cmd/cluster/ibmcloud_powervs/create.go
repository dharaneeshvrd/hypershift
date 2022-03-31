package ibmcloud_powervs

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/signal"
	"syscall"

	apifixtures "github.com/openshift/hypershift/api/fixtures"
	"github.com/openshift/hypershift/cmd/cluster/core"
	powervsinfra "github.com/openshift/hypershift/cmd/infra/ibmcloud_powervs"
	"github.com/openshift/hypershift/cmd/log"
	"github.com/openshift/hypershift/support/infraid"
	"github.com/spf13/cobra"
)

const (
	defaultCIDRBlock = "10.0.0.0/16"
)

func NewCreateCommand(opts *core.CreateOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:          "powervs",
		Short:        "Creates basic functional HostedCluster resources on IBMCloudPowerVS PowerVS",
		SilenceUsage: true,
	}

	opts.IBMCloudPowerVSPlatform = core.IBMCloudPowerVSPlatformOptions{
		APIKey:        os.Getenv("IBMCLOUD_API_KEY"),
		PowerVSRegion: "us-south",
		PowerVSZone:   "us-south",
		VpcRegion:     "us-south",
		SysType:       "s922",
		ProcType:      "shared",
		Processors:    "0.5",
		Memory:        "32",
	}

	cmd.Flags().StringVar(&opts.IBMCloudPowerVSPlatform.ResourceGroup, "resource-group", "", "IBM Cloud Resource group")
	cmd.Flags().StringVar(&opts.IBMCloudPowerVSPlatform.PowerVSRegion, "powervs-region", opts.IBMCloudPowerVSPlatform.PowerVSRegion, "IBM Cloud PowerVS region")
	cmd.Flags().StringVar(&opts.IBMCloudPowerVSPlatform.PowerVSZone, "powervs-zone", opts.IBMCloudPowerVSPlatform.PowerVSZone, "IBM Cloud PowerVS zone")
	cmd.Flags().StringVar(&opts.IBMCloudPowerVSPlatform.PowerVSCloudInstanceID, "powervs-cloud-instance-id", "", "IBM PowerVS Cloud Instance ID")
	cmd.Flags().StringVar(&opts.IBMCloudPowerVSPlatform.PowerVSCloudConnection, "powervs-cloud-connection", "", "IBM Cloud PowerVS Cloud Connection")
	cmd.Flags().StringVar(&opts.IBMCloudPowerVSPlatform.VpcRegion, "vpc-region", opts.IBMCloudPowerVSPlatform.VpcRegion, "Name region")
	cmd.Flags().StringVar(&opts.IBMCloudPowerVSPlatform.Vpc, "vpc", "", "Name Name")
	cmd.Flags().StringVar(&opts.IBMCloudPowerVSPlatform.SysType, "sys-type", opts.IBMCloudPowerVSPlatform.SysType, "System type used to host the instance(e.g: s922, e980, e880)")
	cmd.Flags().StringVar(&opts.IBMCloudPowerVSPlatform.ProcType, "proc-type", opts.IBMCloudPowerVSPlatform.ProcType, "Processor type (dedicated, shared, capped)")
	cmd.Flags().StringVar(&opts.IBMCloudPowerVSPlatform.Processors, "processors", opts.IBMCloudPowerVSPlatform.Processors, "Number of processors allocated")
	cmd.Flags().StringVar(&opts.IBMCloudPowerVSPlatform.Memory, "memory", opts.IBMCloudPowerVSPlatform.Memory, "Amount of memory allocated (in GB)")

	cmd.MarkFlagRequired("resource-group")

	// these options are only for development and testing purpose,
	// can use these to reuse the existing resources, so hiding it.
	// for using these flags, the connection b/w all the resources should be pre-set up properly
	// e.g. cloud instance should contain a cloud connection attached to the dhcp server and provided vpc
	cmd.Flags().MarkHidden("powervs-cloud-instance-id")
	cmd.Flags().MarkHidden("powervs-cloud-connection")
	cmd.Flags().MarkHidden("vpc")

	cmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		if opts.BaseDomain == "" {
			return fmt.Errorf("--base-domain can't be empty")
		}
		return nil
	}
	cmd.Run = func(cmd *cobra.Command, args []string) {
		ctx, cancel := context.WithCancel(context.Background())
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGINT)
		go func() {
			<-sigs
			cancel()
		}()

		if err := CreateCluster(ctx, opts); err != nil {
			log.Log.Error(err, "Failed to create cluster")
			os.Exit(1)
		}
	}

	return cmd
}

func CreateCluster(ctx context.Context, opts *core.CreateOptions) error {
	if err := validate(opts); err != nil {
		return err
	}
	if err := core.Validate(ctx, opts); err != nil {
		return err
	}
	return core.CreateCluster(ctx, opts, applyPlatformSpecificsValues)
}

func validate(opts *core.CreateOptions) error {
	if opts.BaseDomain == "" {
		return fmt.Errorf("--base-domain can't be empty")
	}

	if opts.IBMCloudPowerVSPlatform.APIKey == "" {
		return fmt.Errorf("IBMCLOUD_API_KEY not set")
	}
	return nil
}

func applyPlatformSpecificsValues(ctx context.Context, exampleOptions *apifixtures.ExampleOptions, opts *core.CreateOptions) (err error) {
	infraID := opts.InfraID
	if len(infraID) == 0 {
		infraID = infraid.New(opts.Name)
	}

	// Load or create infrastructure for the cluster
	var infra *powervsinfra.Infra
	if len(opts.InfrastructureJSON) > 0 {
		rawInfra, err := ioutil.ReadFile(opts.InfrastructureJSON)
		if err != nil {
			return fmt.Errorf("failed to read infra json file: %w", err)
		}
		infra = &powervsinfra.Infra{}
		if err = json.Unmarshal(rawInfra, infra); err != nil {
			return fmt.Errorf("failed to load infra json: %w", err)
		}
	}

	if infra == nil {
		if len(infraID) == 0 {
			infraID = infraid.New(opts.Name)
		}
		opt := &powervsinfra.CreateInfraOptions{
			BaseDomain:             opts.BaseDomain,
			ResourceGroup:          opts.IBMCloudPowerVSPlatform.ResourceGroup,
			InfraID:                infraID,
			PowerVSRegion:          opts.IBMCloudPowerVSPlatform.PowerVSRegion,
			PowerVSZone:            opts.IBMCloudPowerVSPlatform.PowerVSZone,
			PowerVSCloudInstanceID: opts.IBMCloudPowerVSPlatform.PowerVSCloudInstanceID,
			PowerVSCloudConnection: opts.IBMCloudPowerVSPlatform.PowerVSCloudConnection,
			VpcRegion:              opts.IBMCloudPowerVSPlatform.VpcRegion,
			Vpc:                    opts.IBMCloudPowerVSPlatform.Vpc,
		}
		infra = &powervsinfra.Infra{}
		err = infra.SetupInfra(opt)
		if err != nil {
			return fmt.Errorf("failed to create infra: %w", err)
		}
	}

	exampleOptions.BaseDomain = opts.BaseDomain
	exampleOptions.ComputeCIDR = defaultCIDRBlock
	exampleOptions.PrivateZoneID = infra.CisDomainID
	exampleOptions.PublicZoneID = infra.CisDomainID
	exampleOptions.InfraID = infraID
	exampleOptions.IBMCloudPowerVS = &apifixtures.ExampleIBMCloudPowerVSOptions{
		ApiKey:                 opts.IBMCloudPowerVSPlatform.APIKey,
		AccountID:              infra.AccountID,
		ResourceGroup:          opts.IBMCloudPowerVSPlatform.ResourceGroup,
		PowerVSRegion:          opts.IBMCloudPowerVSPlatform.PowerVSRegion,
		PowerVSZone:            opts.IBMCloudPowerVSPlatform.PowerVSZone,
		PowerVSCloudInstanceID: infra.PowerVSCloudInstanceID,
		PowerVSSubnetID:        infra.PowerVSDhcpSubnetID,
		VpcRegion:              opts.IBMCloudPowerVSPlatform.VpcRegion,
		Vpc:                    infra.VpcID,
		VpcSubnet:              infra.VpcSubnetID,
		SysType:                opts.IBMCloudPowerVSPlatform.SysType,
		ProcType:               opts.IBMCloudPowerVSPlatform.ProcType,
		Processors:             opts.IBMCloudPowerVSPlatform.Processors,
		Memory:                 opts.IBMCloudPowerVSPlatform.Memory,
	}
	return nil
}
