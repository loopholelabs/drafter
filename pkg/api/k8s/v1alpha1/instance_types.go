package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// InstanceSpec defines the desired state of Instance
type InstanceSpec struct {
	// PackageAddr defines the package address
	// +operator-sdk:csv:customresourcedefinitions:type=spec
	PackageRaddr string `json:"packageRaddr,omitempty"`

	// NodeName is the name of the node that the instance should run on
	// +operator-sdk:csv:customresourcedefinitions:type=spec
	NodeName string `json:"nodeName,omitempty"`
}

// InstanceStatus defines the observed state of Instance
type InstanceStatus struct {
	// PackageLaddr describes the listen package address that this instance is being seeded on
	// +operator-sdk:csv:customresourcedefinitions:type=status
	PackageLaddr string `json:"packageLaddr,omitempty"`

	// NodeName is the name of the node that the instance is currently running on
	// +operator-sdk:csv:customresourcedefinitions:type=status
	NodeName string `json:"nodeName,omitempty"`

	// State describes the current state of the instance
	// +operator-sdk:csv:customresourcedefinitions:type=status
	State string `json:"state,omitempty"`

	// LeechedRaddr describes the remote package address that this instance was leeched from
	// +operator-sdk:csv:customresourcedefinitions:type=status
	LeechedRaddr string `json:"leechedRaddr,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// Instance is the Schema for the instances API
// +kubebuilder:subresource:status
type Instance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InstanceSpec   `json:"spec,omitempty"`
	Status InstanceStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// InstanceList contains a list of Instance
type InstanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Instance `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Instance{}, &InstanceList{})
}
