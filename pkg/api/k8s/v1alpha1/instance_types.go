package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// InstanceSpec defines the desired state of Instance
type InstanceSpec struct {
	// PackageAddr defines the original remote package address
	// +operator-sdk:csv:customresourcedefinitions:type=spec
	PackageRaddr string `json:"packageRaddr,omitempty"`

	// NodeName is the name of the node that the instance should run on
	// +operator-sdk:csv:customresourcedefinitions:type=spec
	NodeName string `json:"nodeName,omitempty"`
}

// InstanceStatus defines the observed state of Instance
type InstanceStatus struct {
	// PackageAddr defines the current remote package address
	// +operator-sdk:csv:customresourcedefinitions:type=status
	PackageRaddr string `json:"packageRaddr,omitempty"`

	// NodeName is the name of the node that the instance is currently running on
	// +operator-sdk:csv:customresourcedefinitions:type=status
	NodeName string `json:"nodeName,omitempty"`
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
