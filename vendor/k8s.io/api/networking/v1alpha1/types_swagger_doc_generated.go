/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

// This file contains a collection of methods that can be used from go-restful to
// generate Swagger API documentation for its models. Please read this PR for more
// information on the implementation: https://github.com/emicklei/go-restful/pull/215
//
// TODOs are ignored from the parser (e.g. TODO(andronat):... || TODO:...) if and only if
// they are on one line! For multiple line or blocks that you want to ignore use ---.
// Any context after a --- is ignored.
//
// Those methods can be generated by using hack/update-codegen.sh

// AUTO-GENERATED FUNCTIONS START HERE. DO NOT EDIT.
var map_IPAddress = map[string]string{
	"":         "IPAddress represents a single IP of a single IP Family. The object is designed to be used by APIs that operate on IP addresses. The object is used by the Service core API for allocation of IP addresses. An IP address can be represented in different formats, to guarantee the uniqueness of the IP, the name of the object is the IP address in canonical format, four decimal digits separated by dots suppressing leading zeros for IPv4 and the representation defined by RFC 5952 for IPv6. Valid: 192.168.1.5 or 2001:db8::1 or 2001:db8:aaaa:bbbb:cccc:dddd:eeee:1 Invalid: 10.01.2.3 or 2001:db8:0:0:0::1",
	"metadata": "Standard object's metadata. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#metadata",
	"spec":     "spec is the desired state of the IPAddress. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#spec-and-status",
}

func (IPAddress) SwaggerDoc() map[string]string {
	return map_IPAddress
}

var map_IPAddressList = map[string]string{
	"":         "IPAddressList contains a list of IPAddress.",
	"metadata": "Standard object's metadata. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#metadata",
	"items":    "items is the list of IPAddresses.",
}

func (IPAddressList) SwaggerDoc() map[string]string {
	return map_IPAddressList
}

var map_IPAddressSpec = map[string]string{
	"":          "IPAddressSpec describe the attributes in an IP Address.",
	"parentRef": "ParentRef references the resource that an IPAddress is attached to. An IPAddress must reference a parent object.",
}

func (IPAddressSpec) SwaggerDoc() map[string]string {
	return map_IPAddressSpec
}

var map_ParentReference = map[string]string{
	"":          "ParentReference describes a reference to a parent object.",
	"group":     "Group is the group of the object being referenced.",
	"resource":  "Resource is the resource of the object being referenced.",
	"namespace": "Namespace is the namespace of the object being referenced.",
	"name":      "Name is the name of the object being referenced.",
}

func (ParentReference) SwaggerDoc() map[string]string {
	return map_ParentReference
}

var map_ServiceCIDR = map[string]string{
	"":         "ServiceCIDR defines a range of IP addresses using CIDR format (e.g. 192.168.0.0/24 or 2001:db2::/64). This range is used to allocate ClusterIPs to Service objects.",
	"metadata": "Standard object's metadata. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#metadata",
	"spec":     "spec is the desired state of the ServiceCIDR. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#spec-and-status",
	"status":   "status represents the current state of the ServiceCIDR. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#spec-and-status",
}

func (ServiceCIDR) SwaggerDoc() map[string]string {
	return map_ServiceCIDR
}

var map_ServiceCIDRList = map[string]string{
	"":         "ServiceCIDRList contains a list of ServiceCIDR objects.",
	"metadata": "Standard object's metadata. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#metadata",
	"items":    "items is the list of ServiceCIDRs.",
}

func (ServiceCIDRList) SwaggerDoc() map[string]string {
	return map_ServiceCIDRList
}

var map_ServiceCIDRSpec = map[string]string{
	"":      "ServiceCIDRSpec define the CIDRs the user wants to use for allocating ClusterIPs for Services.",
	"cidrs": "CIDRs defines the IP blocks in CIDR notation (e.g. \"192.168.0.0/24\" or \"2001:db8::/64\") from which to assign service cluster IPs. Max of two CIDRs is allowed, one of each IP family. This field is immutable.",
}

func (ServiceCIDRSpec) SwaggerDoc() map[string]string {
	return map_ServiceCIDRSpec
}

var map_ServiceCIDRStatus = map[string]string{
	"":           "ServiceCIDRStatus describes the current state of the ServiceCIDR.",
	"conditions": "conditions holds an array of metav1.Condition that describe the state of the ServiceCIDR. Current service state",
}

func (ServiceCIDRStatus) SwaggerDoc() map[string]string {
	return map_ServiceCIDRStatus
}

// AUTO-GENERATED FUNCTIONS END HERE
