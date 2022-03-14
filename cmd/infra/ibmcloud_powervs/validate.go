package ibmcloud_powervs

import (
	"fmt"

	"github.com/IBM-Cloud/power-go-client/clients/instance"
	"github.com/IBM/platform-services-go-sdk/resourcecontrollerv2"
	"github.com/IBM/vpc-go-sdk/vpcv1"
)

// validateCloudInstanceByID ...
// validates cloud instance's existence by id
func validateCloudInstanceByID(cloudInstanceID string) (resourceInstance *resourcecontrollerv2.ResourceInstance, err error) {
	rcv2, err := resourcecontrollerv2.NewResourceControllerV2(&resourcecontrollerv2.ResourceControllerV2Options{Authenticator: getIAMAuth()})
	if err != nil {
		return nil, err
	}

	resourceInstance, _, err = rcv2.GetResourceInstance(&resourcecontrollerv2.GetResourceInstanceOptions{ID: &cloudInstanceID})
	if err != nil {
		return nil, err
	}

	if resourceInstance == nil {
		return nil, fmt.Errorf("%s cloud instance not found", cloudInstanceID)
	}

	if *resourceInstance.State != "active" {
		return nil, fmt.Errorf("provided cloud instance id is not in active state, current state: %s", *resourceInstance.State)
	}
	return resourceInstance, err
}

// validateCloudInstanceByName ...
// validates cloud instance's existence by name
func validateCloudInstanceByName(cloudInstance string, resourceGroupID string, powerVsZone string, serviceID string, servicePlanID string) (resourceInstance *resourcecontrollerv2.ResourceInstance, err error) {
	rcv2, err := resourcecontrollerv2.NewResourceControllerV2(&resourcecontrollerv2.ResourceControllerV2Options{Authenticator: getIAMAuth()})
	if err != nil {
		return nil, err
	}

	f := func(start string) (bool, string, error) {
		listResourceInstOpt := resourcecontrollerv2.ListResourceInstancesOptions{
			Name:            &cloudInstance,
			ResourceGroupID: &resourceGroupID,
			ResourceID:      &serviceID,
			ResourcePlanID:  &servicePlanID}

		if start != "" {
			listResourceInstOpt.Start = &start
		}

		resourceInstanceL, _, err := rcv2.ListResourceInstances(&listResourceInstOpt)

		if err != nil {
			return false, "", err
		}

		for _, resourceIns := range resourceInstanceL.Resources {
			if *resourceIns.Name == cloudInstance && *resourceIns.RegionID == powerVsZone {
				resourceInstance = &resourceIns
				return true, "", nil
			}
		}

		// For paging over next set of resources getting the start token
		if resourceInstanceL.NextURL != nil && *resourceInstanceL.NextURL != "" {
			return false, *resourceInstanceL.NextURL, nil
		}

		return true, "", nil
	}

	err = pagingHelper(f)
	if err != nil {
		return nil, err
	}

	if resourceInstance == nil {
		return nil, fmt.Errorf("%s cloud instance not found", cloudInstance)
	}

	if *resourceInstance.State != "active" {
		return nil, fmt.Errorf("provided cloud instance id is not in active state, current state: %s", *resourceInstance.State)
	}
	return resourceInstance, err
}

// validateVpc ...
// validates vpc's existence by name
func validateVpc(vpcName string, resourceGroupID string, v1 *vpcv1.VpcV1) (vpc *vpcv1.VPC, err error) {
	f := func(start string) (bool, string, error) {
		vpcListOpt := vpcv1.ListVpcsOptions{ResourceGroupID: &resourceGroupID}
		if start != "" {
			vpcListOpt.Start = &start
		}
		vpcList, _, err := v1.ListVpcs(&vpcListOpt)
		if err != nil {
			return false, "", err
		}
		for _, v := range vpcList.Vpcs {
			if *v.Name == vpcName {
				vpc = &v
				return true, "", nil
			}
		}

		if vpcList.Next != nil && *vpcList.Next.Href != "" {
			return false, *vpcList.Next.Href, nil
		}

		return true, "", nil
	}
	err = pagingHelper(f)
	if err != nil {
		return nil, err
	}

	if vpc != nil {
		return vpc, nil
	}

	return nil, fmt.Errorf("%s vpc not found", vpcName)
}

// validateCloudConnection ...
// validates cloud connection's existence by name
func validateCloudConnection(cloudConnName string, client *instance.IBMPICloudConnectionClient) (cloudConnID string, err error) {
	cloudConnL, err := client.GetAll()
	if err != nil {
		return "", err
	}

	if cloudConnL != nil {
		if len(cloudConnL.CloudConnections) == 2 {
			return "", fmt.Errorf("Two Cloud connections per cloud instance iss not supported currently")
		}

		for _, cc := range cloudConnL.CloudConnections {
			if cc != nil && *cc.Name == cloudConnName {
				cloudConnID = *cc.CloudConnectionID
				break
			}
		}

		if len(cloudConnL.CloudConnections) == 1 {
			if cloudConnID != "" {
				return cloudConnID, nil
			} else {
				return "", fmt.Errorf("given cloud connection not found or powervs zone has more than one cloud connection, make sure only one cloud connection present per PowerVS zone")
			}
		}
	}

	return "", nil
}
