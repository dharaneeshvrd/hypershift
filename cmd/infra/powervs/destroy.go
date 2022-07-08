package powervs

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/IBM-Cloud/power-go-client/power/models"
	"github.com/IBM/networking-go-sdk/dnsrecordsv1"
	"io/ioutil"
	"k8s.io/apimachinery/pkg/util/errors"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/IBM-Cloud/power-go-client/clients/instance"
	"github.com/IBM-Cloud/power-go-client/ibmpisession"
	"github.com/IBM/platform-services-go-sdk/resourcecontrollerv2"
	"github.com/IBM/vpc-go-sdk/vpcv1"
)

const (
	// Time duration for monitoring the resource readiness
	cloudInstanceDeletionTimeout   = time.Minute * 10
	powerVSResourceDeletionTimeout = time.Minute * 5
	vpcResourceDeletionTimeout     = time.Minute * 2
	dhcpServerDeletionTimeout      = time.Minute * 10

	// Resource desired states
	powerVSCloudInstanceRemovedState = "removed"
	powerVSJobCompletedState         = "completed"
	powerVSJobFailedState            = "failed"
	dhcpInstanceShutOffState         = "SHUTOFF"

	// Resource name prefix
	vpcLbNamePrefix = "kube"
)

// DestroyInfraOptions command line options to destroy infra created in IBMCloud for Hypershift
type DestroyInfraOptions struct {
	Name                   string
	InfraID                string
	InfrastructureJson     string
	BaseDomain             string
	CISCRN                 string
	CISDomainID            string
	ResourceGroup          string
	PowerVSRegion          string
	PowerVSZone            string
	PowerVSCloudInstanceID string
	PowerVSDhcpID          string
	PowerVSCloudConnection string
	VPCRegion              string
	VPC                    string
	Debug                  bool
}

func NewDestroyCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "powervs",
		Short:        "Destroys PowerVS infrastructure resources for a cluster",
		SilenceUsage: true,
	}

	opts := DestroyInfraOptions{}

	cmd.Flags().StringVar(&opts.Name, "name", opts.Name, "Name of the cluster")
	cmd.Flags().StringVar(&opts.InfraID, "infra-id", opts.InfraID, "Cluster ID with which to tag IBM Cloud resources")
	cmd.Flags().StringVar(&opts.InfrastructureJson, "infra-json", opts.InfrastructureJson, "Result of ./hypershift infra create powervs")
	cmd.Flags().StringVar(&opts.BaseDomain, "base-domain", opts.BaseDomain, "The ingress base domain of the cluster")
	cmd.Flags().StringVar(&opts.ResourceGroup, "resource-group", opts.ResourceGroup, "IBM Cloud Resource Group")
	cmd.Flags().StringVar(&opts.VPCRegion, "vpc-region", opts.VPCRegion, "IBM Cloud VPC Infra Region")
	cmd.Flags().StringVar(&opts.VPC, "vpc", opts.VPC, "IBM Cloud VPC")
	cmd.Flags().StringVar(&opts.PowerVSRegion, "region", opts.PowerVSRegion, "PowerVS Region")
	cmd.Flags().StringVar(&opts.PowerVSZone, "zone", opts.PowerVSZone, "IBM Cloud PowerVS Zone")
	cmd.Flags().StringVar(&opts.PowerVSCloudConnection, "cloud-connection", opts.PowerVSCloudConnection, "IBM Cloud PowerVS Cloud Connection")
	cmd.Flags().StringVar(&opts.PowerVSCloudInstanceID, "cloud-instance-id", opts.PowerVSCloudInstanceID, "IBM PowerVS Cloud Instance ID")
	cmd.Flags().BoolVar(&opts.Debug, "debug", opts.Debug, "Enabling this will result in debug logs will be printed")

	cmd.MarkFlagRequired("resource-group")
	cmd.MarkFlagRequired("infra-id")
	cmd.MarkFlagRequired("powervs-region")
	cmd.MarkFlagRequired("powervs-zone")
	cmd.MarkFlagRequired("vpc-region")

	// these options are only for development and testing purpose, user can pass these flags
	// to destroy the resource created inside these resources for hypershift infra purpose
	cmd.Flags().MarkHidden("vpc")
	cmd.Flags().MarkHidden("powervs-cloud-connection")
	cmd.Flags().MarkHidden("powervs-cloud-instance-id")

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if err := opts.Run(cmd.Context()); err != nil {
			log(opts.InfraID).Error(err, "Failed to destroy infrastructure")
			return err
		}
		log(opts.InfraID).Info("Successfully destroyed infrastructure")
		return nil
	}

	return cmd
}

// Run Hypershift Infra Destroy
func (options *DestroyInfraOptions) Run(ctx context.Context) error {
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
	if err := options.DestroyInfra(infra); err != nil {
		return err
	}

	return nil
}

// DestroyInfra infra destruction orchestration
func (options *DestroyInfraOptions) DestroyInfra(infra *Infra) error {
	log(options.InfraID).Info("Destroy Infra Started")

	// if IBMCLOUD_API_KEY is not set, infra cannot be set up.
	if cloudApiKey == "" {
		return fmt.Errorf("IBMCLOUD_API_KEY not set")
	}

	resourceGroupID, err := getResourceGroupID(options.ResourceGroup)
	if err != nil {
		return err
	}

	var powerVsCloudInstanceID string

	serviceID, servicePlanID, err := getServiceInfo(powerVSService, powerVSServicePlan)
	if err != nil {
		return err
	}

	var skipPowerVs bool
	errL := make([]error, 0)

	if err = deleteDNSRecords(options); err != nil {
		errL = append(errL, fmt.Errorf("error deleting dns record from cis domain: %w", err))
		log(options.InfraID).Error(err, "error deleting dns record from cis domain")
	}

	// getting the powervs cloud instance id
	if infra != nil && infra.PowerVSCloudInstanceID != "" {
		powerVsCloudInstanceID = infra.PowerVSCloudInstanceID
	} else if options.PowerVSCloudInstanceID != "" {
		if _, err := validateCloudInstanceByID(options.PowerVSCloudInstanceID); err != nil {
			errL = append(errL, err)
			skipPowerVs = true
		}
		powerVsCloudInstanceID = options.PowerVSCloudInstanceID
	} else {
		cloudInstanceName := fmt.Sprintf("%s-%s", options.InfraID, cloudInstanceNameSuffix)
		cloudInstance, err := validateCloudInstanceByName(cloudInstanceName, resourceGroupID, options.PowerVSZone, serviceID, servicePlanID)
		if err != nil {
			errL = append(errL, err)
			skipPowerVs = true
		}
		if cloudInstance != nil {
			powerVsCloudInstanceID = *cloudInstance.GUID
		}
	}

	var session *ibmpisession.IBMPISession
	if !skipPowerVs {
		session, err = createPowerVSSession(options.PowerVSRegion, options.PowerVSZone, options.Debug)
		if err != nil {
			return err
		}

		if err = destroyPowerVsCloudConnection(options, infra, powerVsCloudInstanceID, session); err != nil {
			errL = append(errL, fmt.Errorf("error destroying powervs cloud connection: %w", err))
			log(options.InfraID).Error(err, "error destroying powervs cloud connection")
		}
	}

	v1, err := createVpcService(options.VPCRegion, options.InfraID)
	if err != nil {
		return err
	}

	if err = destroyVpcSubnet(options, infra, resourceGroupID, v1); err != nil {
		errL = append(errL, fmt.Errorf("error destroying vpc subnet: %w", err))
		log(options.InfraID).Error(err, "error destroying vpc subnet")
	}

	if err = destroyVpc(options, infra, resourceGroupID, v1, options.InfraID); err != nil {
		errL = append(errL, fmt.Errorf("error destroying vpc: %w", err))
		log(options.InfraID).Error(err, "error destroying vpc")
	}

	if !skipPowerVs {
		if err = destroyPowerVsCloudInstance(options, infra, powerVsCloudInstanceID, session); err != nil {
			errL = append(errL, fmt.Errorf("error destroying powervs cloud instance: %w", err))
			log(options.InfraID).Error(err, "error destroying powervs cloud instance")
		}
	}

	log(options.InfraID).Info("Destroy Infra Completed")

	if err := errors.NewAggregate(errL); err != nil {
		return fmt.Errorf("error in destroying infra: %w", err)
	}

	return nil
}

// deleteDNSRecords deletes DNS records from CIS domain
func deleteDNSRecords(options *DestroyInfraOptions) error {

	if options.CISCRN == "" || options.CISDomainID == "" {
		if options.BaseDomain == "" || options.Name == "" {
			log(options.InfraID).Info("Skipping DNS record deletion. DNS Record deletion requires --name & --base-domain")
			return nil
		} else {
			var err error
			options.CISCRN, options.CISDomainID, err = getCisDomainDetails(options.BaseDomain)
			if err != nil {
				return fmt.Errorf("error retrieving cis domain details %w", err)
			}
		}
	}

	dnsRecordsV1, err := dnsrecordsv1.NewDnsRecordsV1(&dnsrecordsv1.DnsRecordsV1Options{Crn: &options.CISCRN, ZoneIdentifier: &options.CISDomainID, Authenticator: getIAMAuth()})
	if err != nil {
		return fmt.Errorf("error creating dns record service %w", err)
	}

	recordName := fmt.Sprintf("*.apps.%s.%s", options.Name, options.BaseDomain)
	listDnsRecordsOpt := &dnsrecordsv1.ListAllDnsRecordsOptions{Name: &recordName}

	dnsRecordsL, _, err := dnsRecordsV1.ListAllDnsRecords(listDnsRecordsOpt)
	if err != nil {
		return err
	}

	if len(dnsRecordsL.Result) <= 0 {
		log(options.InfraID).Info("No matching DNS Records present in CIS Domain")
		return nil
	}

	record := dnsRecordsL.Result[0]
	log(options.InfraID).Info("Deleting DNS", "record", recordName)
	deleteRecordOpt := &dnsrecordsv1.DeleteDnsRecordOptions{DnsrecordIdentifier: record.ID}
	if _, _, err = dnsRecordsV1.DeleteDnsRecord(deleteRecordOpt); err != nil {
		return err
	}

	return nil
}

// destroyPowerVsDhcpServer destroying powervs dhcp server
func destroyPowerVsDhcpServer(infra *Infra, cloudInstanceID string, session *ibmpisession.IBMPISession, infraID string) error {
	client := instance.NewIBMPIDhcpClient(context.Background(), session, cloudInstanceID)
	if infra != nil && infra.PowerVSDhcpID != "" {
		log(infraID).Info("Deleting DHCP server", "id", infra.PowerVSDhcpID)
		return client.Delete(infra.PowerVSDhcpID)
	}

	dhcpServers, err := client.GetAll()
	if err != nil {
		return err
	}

	if dhcpServers == nil || len(dhcpServers) < 1 {
		return fmt.Errorf("no dhcp servers available to delete in powervs")
	}

	dhcpID := *dhcpServers[0].ID
	log(infraID).Info("Deleting DHCP server", "id", dhcpID)
	err = client.Delete(dhcpID)
	if err != nil {
		return err
	}

	instanceClient := instance.NewIBMPIInstanceClient(context.Background(), session, cloudInstanceID)

	// TO-DO: need to replace the logic of waiting for dhcp service deletion by using jobReference.
	// jobReference is not yet added in SDK
	f := func() (bool, error) {
		dhcpInstance, err := instanceClient.Get(dhcpID)
		if err != nil {
			errMsg := err.Error()
			// when instance becomes does not exist, infra destroy can proceed
			if strings.Contains(errMsg, "pvm-instance does not exist") {
				return true, nil
			}
			return false, err
		}

		if dhcpInstance == nil {
			return false, fmt.Errorf("dhcpInstance is nil")
		}

		log(infraID).Info("Waiting for DhcpServer to destroy", "id", *dhcpInstance.PvmInstanceID, "status", *dhcpInstance.Status)
		if *dhcpInstance.Status == dhcpInstanceShutOffState || *dhcpInstance.Status == dhcpServiceErrorState {
			return true, nil
		}

		return false, nil
	}

	err = wait.PollImmediate(pollingInterval, dhcpServerDeletionTimeout, f)

	return err
}

// destroyPowerVsCloudInstance destroying powervs cloud instance
func destroyPowerVsCloudInstance(options *DestroyInfraOptions, infra *Infra, cloudInstanceID string, session *ibmpisession.IBMPISession) error {
	rcv2, err := resourcecontrollerv2.NewResourceControllerV2(&resourcecontrollerv2.ResourceControllerV2Options{Authenticator: getIAMAuth()})
	if err != nil {
		return err
	}

	if options.PowerVSCloudInstanceID != "" {
		// In case of user provided cloud instance delete only DHCP server
		err = destroyPowerVsDhcpServer(infra, cloudInstanceID, session, options.InfraID)
	} else {
		for retry := 0; retry < 5; retry++ {
			log(options.InfraID).Info("Deleting PowerVS cloud instance", "id", cloudInstanceID)
			if _, err = rcv2.DeleteResourceInstance(&resourcecontrollerv2.DeleteResourceInstanceOptions{ID: &cloudInstanceID}); err != nil {
				log(options.InfraID).Error(err, "error in deleting powervs cloud instance")
				continue
			}

			f := func() (bool, error) {
				resourceInst, resp, err := rcv2.GetResourceInstance(&resourcecontrollerv2.GetResourceInstanceOptions{ID: &cloudInstanceID})
				if err != nil {
					log(options.InfraID).Error(err, "error in querying deleted cloud instance", "resp", resp.String())
					return false, err
				}

				if resp.StatusCode >= 400 {
					err = fmt.Errorf("retrying due to resp code is %d and message is %s", resp.StatusCode, resp.String())
					return false, err
				}
				if resourceInst != nil {
					if *resourceInst.State == powerVSCloudInstanceRemovedState {
						return true, nil
					}

					log(options.InfraID).Info("Waiting for PowerVS cloud instance deletion", "status", *resourceInst.State, "lastOp", resourceInst.LastOperation)
				}

				return false, nil
			}

			if err = wait.PollImmediate(pollingInterval, cloudInstanceDeletionTimeout, f); err == nil {
				break
			}
			log(options.InfraID).Info("Retrying cloud instance deletion ...")
		}
	}

	return err
}

// monitorPowerVsJob monitoring the submitted deletion job
func monitorPowerVsJob(id string, client *instance.IBMPIJobClient, infraID string, timeout time.Duration) error {

	f := func() (bool, error) {
		job, err := client.Get(id)
		if err != nil {
			return false, err
		}
		if job == nil {
			return false, fmt.Errorf("job returned for %s is nil", id)
		}
		log(infraID).Info("Waiting for PowerVS job to complete", "id", id, "status", job.Status.State, "operation_action", *job.Operation.Action, "operation_target", *job.Operation.Target)

		if *job.Status.State == powerVSJobCompletedState || *job.Status.State == powerVSJobFailedState {
			return true, nil
		}
		return false, nil
	}

	return wait.PollImmediate(pollingInterval, timeout, f)
}

// destroyPowerVsCloudConnection destroying powervs cloud connection
func destroyPowerVsCloudConnection(options *DestroyInfraOptions, infra *Infra, cloudInstanceID string, session *ibmpisession.IBMPISession) error {
	var err error

	client := instance.NewIBMPICloudConnectionClient(context.Background(), session, cloudInstanceID)
	jobClient := instance.NewIBMPIJobClient(context.Background(), session, cloudInstanceID)

	var cloudConnName string
	// Destroying resources created for Hypershift infra creation
	if options.PowerVSCloudConnection != "" {
		cloudConnName = options.PowerVSCloudConnection
		var cloudConnL *models.CloudConnections
		cloudConnL, err = client.GetAll()
		if err != nil || cloudConnL == nil {
			return err
		}

		if len(cloudConnL.CloudConnections) < 1 {
			return fmt.Errorf("no cloud connection available to unset in powervs")
		}

		for _, cloudConn := range cloudConnL.CloudConnections {
			if *cloudConn.Name == cloudConnName {
				// De-linking the VPC in cloud connection
				var vpc *models.CloudConnectionEndpointVPC
				var vpcL []*models.CloudConnectionVPC
				if cloudConn.Vpc != nil && cloudConn.Vpc.Enabled {
					for _, v := range cloudConn.Vpc.Vpcs {
						vpcName := fmt.Sprintf("%s-%s", options.InfraID, vpcNameSuffix)
						if (options.VPC != "" && v.Name == options.VPC) || v.Name == vpcName {
							continue
						}
						vpcL = append(vpcL, v)
					}
					vpc.Enabled = true
					vpc.Vpcs = vpcL
				}

				if _, _, err = client.Update(*cloudConn.CloudConnectionID, &models.CloudConnectionUpdate{Name: cloudConn.Name, Vpc: vpc}); err != nil {
					return err
				}

				// Removing the DHCP network from cloud connection
				if cloudConn.Networks != nil {
					for _, nw := range cloudConn.Networks {
						nwName := strings.ToLower(*nw.Name)
						if strings.Contains(nwName, "dhcp") && strings.Contains(nwName, "private") {
							if _, _, err = client.DeleteNetwork(*cloudConn.CloudConnectionID, *nw.NetworkID); err != nil {
								return err
							}
						}
					}
				}
			}
		}
	} else {
		if infra != nil && infra.PowerVSCloudConnectionID != "" {
			return deletePowerVsCloudConnection(options, infra.PowerVSCloudConnectionID, client, jobClient)
		}
		var cloudConnL *models.CloudConnections
		cloudConnL, err = client.GetAll()
		if err != nil || cloudConnL == nil {
			return err
		}

		if len(cloudConnL.CloudConnections) < 1 {
			return fmt.Errorf("no cloud connection available to delete in powervs")
		}

		cloudConnName = fmt.Sprintf("%s-%s", options.InfraID, cloudConnNameSuffix)

		for _, cloudConn := range cloudConnL.CloudConnections {
			if *cloudConn.Name == cloudConnName {
				return deletePowerVsCloudConnection(options, *cloudConn.CloudConnectionID, client, jobClient)
			}
		}
	}

	return nil
}

// deletePowerVsCloudConnection deletes cloud connection id passed
func deletePowerVsCloudConnection(options *DestroyInfraOptions, id string, client *instance.IBMPICloudConnectionClient, jobClient *instance.IBMPIJobClient) error {
	log(options.InfraID).Info("Deleting cloud connection", "id", id)
	deleteJob, err := client.Delete(id)
	if err != nil {
		return err
	}
	if deleteJob == nil {
		return fmt.Errorf("error while deleting cloud connection, delete job returned is nil")
	}
	return monitorPowerVsJob(*deleteJob.ID, jobClient, options.InfraID, powerVSResourceDeletionTimeout)
}

// destroyVpc destroying vpc
func destroyVpc(options *DestroyInfraOptions, infra *Infra, resourceGroupID string, v1 *vpcv1.VpcV1, infraID string) error {
	if infra != nil && infra.VPCID != "" {
		return deleteVpc(infra.VPCID, v1, infraID)
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

		// Will attempt VPC deletion only if the VPC is created by this script by matching the VPC naming convention followed by this script
		vpcName := fmt.Sprintf("%s-%s", options.InfraID, vpcNameSuffix)

		for _, vpc := range vpcL.Vpcs {
			if *vpc.Name == vpcName && strings.Contains(*vpc.CRN, options.VPCRegion) {
				err = deleteVpc(*vpc.ID, v1, infraID)
				return true, "", nil
			}
		}
		if vpcL.Next != nil && *vpcL.Next.Href != "" {
			return false, *vpcL.Next.Href, nil
		}

		return true, "", nil
	}

	return pagingHelper(f)
}

// deleteVpc deletes the vpc id passed
func deleteVpc(id string, v1 *vpcv1.VpcV1, infraID string) error {
	log(infraID).Info("Deleting VPC", "id", id)
	_, err := v1.DeleteVPC(&vpcv1.DeleteVPCOptions{ID: &id})
	return err
}

// destroyVpcSubnet destroying vpc subnet
func destroyVpcSubnet(options *DestroyInfraOptions, infra *Infra, resourceGroupID string, v1 *vpcv1.VpcV1) error {
	if infra != nil && infra.VPCSubnetID != "" {
		return deleteVpcSubnet(infra.VPCSubnetID, v1, options)
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

		// Consider deleting only if the subnet created by this script by matching the VPC naming convention followed by this script
		vpcName := fmt.Sprintf("%s-%s", options.InfraID, vpcNameSuffix)

		for _, subnet := range subnetL.Subnets {
			if *subnet.VPC.Name == vpcName && strings.Contains(*subnet.Zone.Name, options.VPCRegion) {
				err = deleteVpcSubnet(*subnet.ID, v1, options)
				return true, "", nil
			}
		}

		// For paging over next set of resources getting the start token and passing it for next iteration
		if subnetL.Next != nil && *subnetL.Next.Href != "" {
			return false, *subnetL.Next.Href, nil
		}

		return true, "", nil
	}

	return pagingHelper(f)
}

// deleteVpcSubnet deletes the subnet id passed and LB attached to it
func deleteVpcSubnet(id string, v1 *vpcv1.VpcV1, options *DestroyInfraOptions) error {
	// deleting the load balancer before proceeding to subnet deletion, since LB is an attached resource to the subnet
	if err := destroyVpcLB(options, id, v1); err != nil {
		log(options.InfraID).Error(err, "error destroying VPC Load Balancer")
		return fmt.Errorf("error destroying VPC Load Balancer %w", err)
	}

	log(options.InfraID).Info("Deleting VPC subnet", "subnetId", id)

	if _, err := v1.DeleteSubnet(&vpcv1.DeleteSubnetOptions{ID: &id}); err != nil {
		return err
	}

	f := func() (bool, error) {
		if _, _, err := v1.GetSubnet(&vpcv1.GetSubnetOptions{ID: &id}); err != nil {
			if strings.Contains(err.Error(), "Subnet not found") {
				return true, nil
			} else {
				return false, err
			}
		}
		return false, nil
	}

	err := wait.PollImmediate(pollingInterval, vpcResourceDeletionTimeout, f)
	return err
}

// destroyVpcLB destroys VPC Load Balancer
func destroyVpcLB(options *DestroyInfraOptions, subnetID string, v1 *vpcv1.VpcV1) error {

	f := func(start string) (bool, string, error) {

		listLBOpt := vpcv1.ListLoadBalancersOptions{}
		if start != "" {
			listLBOpt.Start = &start
		}
		loadBalancerL, _, err := v1.ListLoadBalancers(&listLBOpt)
		if err != nil {
			return false, "", err
		}

		if loadBalancerL == nil || len(loadBalancerL.LoadBalancers) <= 0 {
			log(options.InfraID).Info("No Load balancers available to delete ...")
			return true, "", nil
		}

		// Consider deleting LB which starting with 'kube-<cluster-name>'
		// Which are provisioned by cloud controller manager. Below is the code ref for the naming convention.
		// https://github.com/openshift/cloud-provider-powervs/blob/master/pkg/vpcctl/vpc_provider.go#L235
		lbName := fmt.Sprintf("%s-%s", vpcLbNamePrefix, options.Name)

		for _, lb := range loadBalancerL.LoadBalancers {
			for _, subnet := range lb.Subnets {
				if *subnet.ID == subnetID {
					if strings.Contains(*lb.Name, lbName) {
						err = deleteVpcLB(*lb.ID, v1, options.InfraID)
						if err != nil {
							return false, "", err
						}
					}
				}
			}
		}

		// For paging over next set of resources getting the start token and passing it for next iteration
		if loadBalancerL.Next != nil && *loadBalancerL.Next.Href != "" {
			return false, *loadBalancerL.Next.Href, nil
		}

		return true, "", nil
	}

	return pagingHelper(f)
}

// deleteVpcLB deletes the load balancer id passed
func deleteVpcLB(id string, v1 *vpcv1.VpcV1, infraID string) error {
	log(infraID).Info("Deleting VPC LoadBalancer:", "id", id)
	if _, err := v1.DeleteLoadBalancer(&vpcv1.DeleteLoadBalancerOptions{ID: &id}); err != nil {
		return err
	}

	f := func() (bool, error) {
		if _, _, err := v1.GetLoadBalancer(&vpcv1.GetLoadBalancerOptions{ID: &id}); err != nil {
			if strings.Contains(err.Error(), "cannot be found") {
				return true, nil
			} else {
				return false, err
			}
		}
		return false, nil
	}

	err := wait.PollImmediate(pollingInterval, vpcResourceDeletionTimeout, f)
	return err
}
