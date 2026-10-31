// Package v1alpha1 contains the SkillEval and SandboxProfile APIs of skillci.dev.
// +kubebuilder:object:generate=true
// +groupName=skillci.dev
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is the group and version of the skillci.dev API.
	GroupVersion = schema.GroupVersion{Group: "skillci.dev", Version: "v1alpha1"}

	// SchemeBuilder registers the API types with a runtime.Scheme.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds the API types to a runtime.Scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion, &SkillEval{}, &SkillEvalList{}, &SandboxProfile{}, &SandboxProfileList{})
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}
