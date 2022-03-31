package fixtures

import (
	corev1 "k8s.io/api/core/v1"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

type ExampleIBMCloudPowerVSOptions struct {
	ApiKey                 string
	AccountID              string
	ResourceGroup          string
	PowerVSRegion          string
	PowerVSZone            string
	PowerVSCloudInstanceID string
	PowerVSSubnetID        string
	PowerVSCloudConnection string
	VpcRegion              string
	Vpc                    string
	VpcSubnet              string

	// nodepool related options
	SysType    string
	ProcType   string
	Processors string
	Memory     string
}

type ExampleIBMCloudPowerVSResources struct {
	KubeCloudControllerIBMCloudPowerVSCreds  *corev1.Secret
	NodePoolManagementIBMCloudPowerVSCreds   *corev1.Secret
	ControlPlaneOperatorIBMCloudPowerVSCreds *corev1.Secret
}

func (o *ExampleIBMCloudPowerVSResources) AsObjects() []crclient.Object {
	var objects []crclient.Object
	if o.KubeCloudControllerIBMCloudPowerVSCreds != nil {
		objects = append(objects, o.KubeCloudControllerIBMCloudPowerVSCreds)
	}
	if o.NodePoolManagementIBMCloudPowerVSCreds != nil {
		objects = append(objects, o.NodePoolManagementIBMCloudPowerVSCreds)
	}
	if o.ControlPlaneOperatorIBMCloudPowerVSCreds != nil {
		objects = append(objects, o.ControlPlaneOperatorIBMCloudPowerVSCreds)
	}
	return objects
}
