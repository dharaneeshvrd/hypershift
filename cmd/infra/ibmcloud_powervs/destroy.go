package ibmcloud_powervs

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/IBM-Cloud/power-go-client/clients/instance"
	"github.com/IBM-Cloud/power-go-client/ibmpisession"
	"github.com/IBM/platform-services-go-sdk/resourcecontrollerv2"
	"github.com/IBM/vpc-go-sdk/vpcv1"
	
	"github.com/openshift/hypershift/cmd/log"
)

const (
	// Time duration for monitoring the resource readiness
	cloudInstanceDeletionTimeout   = time.Minute * 5
	powerVSResourceDeletionTimeout = time.Minute * 5

	// Resource desired states
	powerVSCloudInstanceRemovedState = "removed"
	powerVSJobCompletedState         = "completed"
	powerVSJobFailedState            = "failed"
)

type DestroyInfraOptions struct {
	InfraID                string
	InfrastructureJson     string
	ResourceGroup          string
	PowerVSRegion          string
	PowerVSZone            string
	PowerVSCloudInstanceID string
	PowerVSDhcpID          string
	PowerVSCloudConnection string
	VpcRegion              string
	Vpc                    string
}

func NewDestroyCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "powervs",
		Short:        "Destroys PowerVS infrastructure resources for a cluster",
		SilenceUsage: true,
	}

	opts := DestroyInfraOptions{}

	cmd.Flags().StringVar(&opts.InfraID, "infra-id", opts.InfraID, "Cluster ID with which to tag AWS resources (required)")
	cmd.Flags().StringVar(&opts.InfrastructureJson, "infrastructure-json", opts.InfrastructureJson, "Result of ./hypershift infra create powervs")
	cmd.Flags().StringVar(&opts.ResourceGroup, "resource-group", opts.ResourceGroup, "IBM Cloud Resource Group")
	cmd.Flags().StringVar(&opts.VpcRegion, "vpc-region", opts.VpcRegion, "IBM Cloud VPC Infra Region")
	cmd.Flags().StringVar(&opts.Vpc, "vpc", opts.Vpc, "IBM Cloud VPC")
	cmd.Flags().StringVar(&opts.PowerVSRegion, "powervs-region", opts.PowerVSRegion, "PowerVS Region")
	cmd.Flags().StringVar(&opts.PowerVSZone, "powervs-zone", opts.PowerVSZone, "IBM Cloud PowerVS Zone")
	cmd.Flags().StringVar(&opts.PowerVSCloudConnection, "powervs-cloud-connection", opts.PowerVSCloudConnection, "IBM Cloud PowerVS Cloud Connection")
	cmd.Flags().StringVar(&opts.PowerVSCloudInstanceID, "powervs-cloud-instance-id", opts.PowerVSCloudInstanceID, "IBM PowerVS Cloud Instance ID")

	cmd.MarkFlagRequired("resource-group")
	cmd.MarkFlagRequired("infra-id")
	cmd.MarkFlagRequired("powervs-region")
	cmd.MarkFlagRequired("powervs-zone")
	cmd.MarkFlagRequired("vpc-region")

	// these options are only for development and testing purpose, user can destroy these resources by passing these flags
	cmd.Flags().MarkHidden("vpc")
	cmd.Flags().MarkHidden("powervs-cloud-connection")
	cmd.Flags().MarkHidden("powervs-cloud-instance-id")

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if err := opts.Run(cmd.Context()); err != nil {
			log.Log.Error(err, "Failed to destroy infrastructure")
			return err
		}
		log.Log.Info("Successfully destroyed infrastructure")
		return nil
	}

	return cmd
}

func (options *DestroyInfraOptions) Run(ctx context.Context) (err error) {
	var infra *Infra
	if len(options.InfrastructureJson) > 0 {
		rawInfra, err := ioutil.ReadFile(options.InfrastructureJson)
		if err != nil {
			return fmt.Errorf("failed to read infra json file: %w", err)
		}
		infra = &Infra{}
		if err = json.Unmarshal(rawInfra, infra); err != nil {
			return fmt.Errorf("failed to load infra json: %w", err)
		}
	}
	err = options.destroyInfra(infra)
	if err != nil {
		return err
	}
	return nil
}

// destroyInfra ...
// infra destruction orchestration
func (options *DestroyInfraOptions) destroyInfra(infra *Infra) (err error) {
	log.Log.Info("Destroy Infra Started")

	resourceGroupID, err := getResourceGroupID(options.ResourceGroup)
	if err != nil {
		return err
	}

	v1, err := createVpcService(options.VpcRegion)
	if err != nil {
		return err
	}

	err = destroyVpcSubnet(options, infra, resourceGroupID, v1)
	if err != nil {
		log.Log.Error(err, "error destroying vpc subnet")
	}

	err = destroyVpc(options, infra, resourceGroupID, v1)
	if err != nil {
		log.Log.Error(err, "error destroying vpc")
	}

	var powerVsCloudInstanceID string

	serviceID, servicePlanID, err := getServiceInfo(powerVSService, powerVSServicePlan)
	if err != nil {
		return err
	}

	// getting the powervs cloud instance id
	if infra != nil && infra.PowerVSCloudInstanceID != "" {
		powerVsCloudInstanceID = infra.PowerVSCloudInstanceID
	} else if options.PowerVSCloudInstanceID != "" {
		_, err := validateCloudInstanceByID(options.PowerVSCloudInstanceID)
		if err != nil {
			return err
		}
		powerVsCloudInstanceID = options.PowerVSCloudInstanceID
	} else {
		cloudInstance, err := validateCloudInstanceByName(fmt.Sprintf("%s-%s", options.InfraID, cloudInstanceNameSuffix), resourceGroupID, options.PowerVSZone, serviceID, servicePlanID)
		if err != nil {
			return err
		}
		powerVsCloudInstanceID = *cloudInstance.GUID
	}

	session, err := createPowerVSSession(options.PowerVSRegion, options.PowerVSZone)

	err = destroyPowerVsDhcpServer(infra, powerVsCloudInstanceID, session)
	if err != nil {
		log.Log.Error(err, "error destroying powervs dhcp server")
	}

	err = destroyPowerVsCloudConnection(options, infra, powerVsCloudInstanceID, session)
	if err != nil {
		log.Log.Error(err, "error destroying powervs cloud connection")
	}

	err = destroyPowerVsCloudInstance(powerVsCloudInstanceID)
	if err != nil {
		log.Log.Error(err, "error destroying powervs cloud instance")
	}

	log.Log.Info("Destroy Infra Completed")

	return nil
}

// destroyPowerVsCloudInstance ...
// destroying powervs cloud instance
func destroyPowerVsCloudInstance(cloudInstanceID string) (err error) {
	rcv2, err := resourcecontrollerv2.NewResourceControllerV2(&resourcecontrollerv2.ResourceControllerV2Options{Authenticator: getIAMAuth()})
	if err != nil {
		return err
	}

	log.Log.Info("Deleting PowerVS cloud instance", "id", cloudInstanceID)
	_, err = rcv2.DeleteResourceInstance(&resourcecontrollerv2.DeleteResourceInstanceOptions{ID: &cloudInstanceID})
	if err != nil {
		return err
	}
	f := func() (bool, error) {
		resourceInst, resp, err := rcv2.GetResourceInstance(&resourcecontrollerv2.GetResourceInstanceOptions{ID: &cloudInstanceID})
		if err != nil {
			log.Log.Error(err, "error in querying deleted cloud instance", "resp", resp.String())
			return false, err
		}

		if *resourceInst.State == powerVSCloudInstanceRemovedState {
			return true, nil
		}

		return false, nil
	}

	err = wait.PollImmediate(pollingInterval, cloudInstanceDeletionTimeout, f)
	return err
}

// monitorPowerVsJob ...
// monitoring the submitted deletion job
func monitorPowerVsJob(id string, client *instance.IBMPIJobClient) (err error) {

	f := func() (bool, error) {
		job, err := client.Get(id)
		if err != nil {
			return false, err
		}
		if *job.Status.State == powerVSJobCompletedState || *job.Status.State == powerVSJobFailedState {
			return true, nil
		}
		return false, nil
	}

	err = wait.PollImmediate(pollingInterval, powerVSResourceDeletionTimeout, f)
	return err
}

// destroyPowerVsCloudConnection ...
// destroying powervs cloud connection
func destroyPowerVsCloudConnection(options *DestroyInfraOptions, infra *Infra, cloudInstanceID string, session *ibmpisession.IBMPISession) (err error) {
	client := instance.NewIBMPICloudConnectionClient(context.Background(), session, cloudInstanceID)
	jobClient := instance.NewIBMPIJobClient(context.Background(), session, cloudInstanceID)

	if infra != nil && infra.PowerVSCloudConnectionID != "" {
		log.Log.Info("Deleting cloud connection", "id", infra.PowerVSCloudConnectionID)
		deleteJob, err := client.Delete(infra.PowerVSCloudConnectionID)
		if err != nil {
			return err
		}
		return monitorPowerVsJob(*deleteJob.ID, jobClient)
	}

	cloudConnL, err := client.GetAll()
	if err != nil || cloudConnL == nil {
		return err
	}

	if len(cloudConnL.CloudConnections) < 1 {
		return fmt.Errorf("no cloud connection available to delete in powervs")
	}
	var cloudConnName string
	if options.Vpc != "" {
		cloudConnName = options.PowerVSCloudConnection
	} else {
		cloudConnName = fmt.Sprintf("%s-%s", options.InfraID, cloudConnNameSuffix)
	}

	for _, cloudConn := range cloudConnL.CloudConnections {
		if *cloudConn.Name == cloudConnName {
			log.Log.Info("Deleting cloud connection", "id", *cloudConn.CloudConnectionID)
			deleteJob, err := client.Delete(*cloudConn.CloudConnectionID)
			if err != nil {
				return err
			}
			return monitorPowerVsJob(*deleteJob.ID, jobClient)
		}
	}
	return nil
}

// destroyPowerVsDhcpServer ...
// destroying powervs dhcp server
func destroyPowerVsDhcpServer(infra *Infra, cloudInstanceID string, session *ibmpisession.IBMPISession) (err error) {
	client := instance.NewIBMPIDhcpClient(context.Background(), session, cloudInstanceID)
	if infra != nil && infra.PowerVSDhcpID != "" {
		log.Log.Info("Deleting DHCP server", "id", infra.PowerVSDhcpID)
		return client.Delete(infra.PowerVSDhcpID)
	}

	dhcpServers, err := client.GetAll()
	if err != nil {
		return err
	}

	if dhcpServers == nil || len(dhcpServers) < 1 {
		return fmt.Errorf("no dhcp servers available to delete in powervs")
	}

	for _, dhcp := range dhcpServers {
		log.Log.Info("Deleting DHCP server", "id", *dhcp.ID)
		return client.Delete(*dhcp.ID)
	}
	return nil
}

// destroyVpc ...
// destroying vpc
func destroyVpc(options *DestroyInfraOptions, infra *Infra, resourceGroupID string, v1 *vpcv1.VpcV1) (err error) {
	if infra != nil && infra.VpcID != "" {
		return deleteVpc(infra.VpcID, v1)
	}

	f := func(start string) (bool, string, error) {
		vpcListOpt := vpcv1.ListVpcsOptions{ResourceGroupID: &resourceGroupID}
		if start != "" {
			vpcListOpt.Start = &start
		}

		vpcL, _, err := v1.ListVpcs(&vpcListOpt)
		if err != nil {
			return false, "", err
		}

		if vpcL == nil || len(vpcL.Vpcs) <= 0 {
			return false, "", fmt.Errorf("no vpcs available")
		}

		var vpcName string
		if options.Vpc != "" {
			vpcName = options.Vpc
		} else {
			vpcName = fmt.Sprintf("%s-%s", options.InfraID, vpcNameSuffix)
		}

		for _, vpc := range vpcL.Vpcs {
			if *vpc.Name == vpcName && strings.Contains(*vpc.CRN, options.VpcRegion) {
				err = deleteVpc(*vpc.ID, v1)
				return true, "", err
			}
		}

		return false, *vpcL.Next.Href, nil
	}

	err = pagingHelper(f)

	return err
}

// deleteVpc ...
// deletes the vpc id passed
func deleteVpc(id string, v1 *vpcv1.VpcV1) (err error) {
	log.Log.Info("Deleting VPC", "id", id)
	_, err = v1.DeleteVPC(&vpcv1.DeleteVPCOptions{ID: &id})
	return err
}

// destroyVpcSubnet ...
// destroying vpc subnet
func destroyVpcSubnet(options *DestroyInfraOptions, infra *Infra, resourceGroupID string, v1 *vpcv1.VpcV1) (err error) {
	if infra != nil && infra.VpcSubnetID != "" {
		return deleteVpcSubnet(infra.VpcSubnetID, v1)
	}

	f := func(start string) (bool, string, error) {

		listSubnetOpt := vpcv1.ListSubnetsOptions{ResourceGroupID: &resourceGroupID}
		if start != "" {
			listSubnetOpt.Start = &start
		}
		subnetL, _, err := v1.ListSubnets(&listSubnetOpt)
		if err != nil {
			return false, "", err
		}

		if subnetL == nil || len(subnetL.Subnets) <= 0 {
			return false, "", fmt.Errorf("no subnets available")
		}

		var vpcName string
		if options.Vpc != "" {
			vpcName = options.Vpc
		} else {
			vpcName = fmt.Sprintf("%s-%s", options.InfraID, vpcNameSuffix)
		}

		for _, subnet := range subnetL.Subnets {
			if *subnet.VPC.Name == vpcName && strings.Contains(*subnet.Zone.Name, options.VpcRegion) {
				err = deleteVpcSubnet(*subnet.ID, v1)
				return true, "", err
			}
		}

		// For paging over next set of resources getting the start token and passing it for next iteration
		if subnetL.Next != nil && *subnetL.Next.Href != "" {
			return false, *subnetL.Next.Href, nil
		}
		return true, "", nil
	}

	err = pagingHelper(f)

	return err
}

// deleteVpcSubnet ...
// deletes the subnet id passed
func deleteVpcSubnet(id string, v1 *vpcv1.VpcV1) (err error) {
	log.Log.Info("Deleting VPC subnet", "subnetId", id)
	_, err = v1.DeleteSubnet(&vpcv1.DeleteSubnetOptions{ID: &id})
	return err
}
