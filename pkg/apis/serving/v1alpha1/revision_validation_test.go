/*
Copyright 2018 The Knative Authors

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

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/knative/pkg/apis"
	"github.com/knative/serving/pkg/apis/autoscaling"
	netv1alpha1 "github.com/knative/serving/pkg/apis/networking/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestContainerValidation(t *testing.T) {
	tests := []struct {
		name string
		c    corev1.Container
		want *apis.FieldError
	}{{
		name: "empty container",
		c:    corev1.Container{},
		want: apis.ErrMissingField(apis.CurrentField),
	}, {
		name: "valid container",
		c: corev1.Container{
			Image: "foo",
		},
		want: nil,
	}, {
		name: "invalid container image",
		c: corev1.Container{
			Image: "foo:bar:baz",
		},
		want: &apis.FieldError{
			Message: "Failed to parse image reference",
			Paths:   []string{"image"},
			Details: "image: \"foo:bar:baz\", error: could not parse reference",
		},
	}, {
		name: "has a name",
		c: corev1.Container{
			Name:  "foo",
			Image: "foo",
		},
		want: apis.ErrDisallowedFields("name"),
	}, {
		name: "has resources",
		c: corev1.Container{
			Image: "foo",
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceName("cpu"): resource.MustParse("25m"),
				},
			},
		},
		want: nil,
	}, {
		name: "has no container ports set",
		c: corev1.Container{
			Image: "foo",
			Ports: []corev1.ContainerPort{},
		},
		want: nil,
	}, {
		name: "has valid user port http1",
		c: corev1.Container{
			Image: "foo",
			Ports: []corev1.ContainerPort{{
				Name:          "http1",
				ContainerPort: 8081,
			}},
		},
		want: nil,
	}, {
		name: "has valid user port h2c",
		c: corev1.Container{
			Image: "foo",
			Ports: []corev1.ContainerPort{{
				Name:          "h2c",
				ContainerPort: 8081,
			}},
		},
		want: nil,
	}, {
		name: "has more than one ports with valid names",
		c: corev1.Container{
			Image: "foo",
			Ports: []corev1.ContainerPort{{
				Name:          "h2c",
				ContainerPort: 8080,
			}, {
				Name:          "http1",
				ContainerPort: 8181,
			}},
		},
		want: &apis.FieldError{
			Message: "More than one container port is set",
			Paths:   []string{"ports"},
			Details: "Only a single port is allowed",
		},
	}, {
		name: "has container port value too large",
		c: corev1.Container{
			Image: "foo",
			Ports: []corev1.ContainerPort{{
				ContainerPort: 65536,
			}},
		},
		want: apis.ErrOutOfBoundsValue("65536", "1", "65535", "ports.ContainerPort"),
	}, {
		name: "has an empty port set",
		c: corev1.Container{
			Image: "foo",
			Ports: []corev1.ContainerPort{{}},
		},
		want: apis.ErrOutOfBoundsValue("0", "1", "65535", "ports.ContainerPort"),
	}, {
		name: "has more than one unnamed port",
		c: corev1.Container{
			Image: "foo",
			Ports: []corev1.ContainerPort{{
				ContainerPort: 8080,
			}, {
				ContainerPort: 8181,
			}},
		},
		want: &apis.FieldError{
			Message: "More than one container port is set",
			Paths:   []string{"ports"},
			Details: "Only a single port is allowed",
		},
	}, {
		name: "has tcp protocol",
		c: corev1.Container{
			Image: "foo",
			Ports: []corev1.ContainerPort{{
				Protocol:      corev1.ProtocolTCP,
				ContainerPort: 8080,
			}},
		},
		want: nil,
	}, {
		name: "has invalid protocol",
		c: corev1.Container{
			Image: "foo",
			Ports: []corev1.ContainerPort{{
				Protocol:      "tdp",
				ContainerPort: 8080,
			}},
		},
		want: apis.ErrInvalidValue("tdp", "ports.Protocol"),
	}, {
		name: "has host port",
		c: corev1.Container{
			Image: "foo",
			Ports: []corev1.ContainerPort{{
				HostPort:      80,
				ContainerPort: 8080,
			}},
		},
		want: apis.ErrDisallowedFields("ports.HostPort"),
	}, {
		name: "has host ip",
		c: corev1.Container{
			Image: "foo",
			Ports: []corev1.ContainerPort{{
				HostIP:        "127.0.0.1",
				ContainerPort: 8080,
			}},
		},
		want: apis.ErrDisallowedFields("ports.HostIP"),
	}, {
		name: "port conflicts with queue proxy admin",
		c: corev1.Container{
			Image: "foo",
			Ports: []corev1.ContainerPort{{
				ContainerPort: 8022,
			}},
		},
		want: apis.ErrInvalidValue("8022", "ports.ContainerPort"),
	}, {
		name: "port conflicts with queue proxy",
		c: corev1.Container{
			Image: "foo",
			Ports: []corev1.ContainerPort{{
				ContainerPort: 8012,
			}},
		},
		want: apis.ErrInvalidValue("8012", "ports.ContainerPort"),
	}, {
		name: "port conflicts with queue proxy metrics",
		c: corev1.Container{
			Image: "foo",
			Ports: []corev1.ContainerPort{{
				ContainerPort: 9090,
			}},
		},
		want: apis.ErrInvalidValue("9090", "ports.ContainerPort"),
	}, {
		name: "has invalid port name",
		c: corev1.Container{
			Image: "foo",
			Ports: []corev1.ContainerPort{{
				Name:          "foobar",
				ContainerPort: 8080,
			}},
		},
		want: &apis.FieldError{
			Message: fmt.Sprintf("Port name %v is not allowed", "foobar"),
			Paths:   []string{"ports"},
			Details: "Name must be empty, or one of: 'h2c', 'http1'",
		},
	}, {
		name: "has volumeMounts",
		c: corev1.Container{
			Image: "foo",
			VolumeMounts: []corev1.VolumeMount{{
				MountPath: "mount/path",
				Name:      "name",
			}},
		},
		want: apis.ErrDisallowedFields("volumeMounts"),
	}, {
		name: "has lifecycle",
		c: corev1.Container{
			Image:     "foo",
			Lifecycle: &corev1.Lifecycle{},
		},
		want: apis.ErrDisallowedFields("lifecycle"),
	}, {
		name: "valid with probes (no port)",
		c: corev1.Container{
			Image: "foo",
			ReadinessProbe: &corev1.Probe{
				Handler: corev1.Handler{
					HTTPGet: &corev1.HTTPGetAction{
						Path: "/",
					},
				},
			},
			LivenessProbe: &corev1.Probe{
				Handler: corev1.Handler{
					TCPSocket: &corev1.TCPSocketAction{},
				},
			},
		},
		want: nil,
	}, {
		name: "invalid readiness http probe (has port)",
		c: corev1.Container{
			Image: "foo",
			ReadinessProbe: &corev1.Probe{
				Handler: corev1.Handler{
					HTTPGet: &corev1.HTTPGetAction{
						Path: "/",
						Port: intstr.FromInt(8080),
					},
				},
			},
		},
		want: apis.ErrDisallowedFields("readinessProbe.httpGet.port"),
	}, {
		name: "invalid liveness tcp probe (has port)",
		c: corev1.Container{
			Image: "foo",
			LivenessProbe: &corev1.Probe{
				Handler: corev1.Handler{
					TCPSocket: &corev1.TCPSocketAction{
						Port: intstr.FromString("http"),
					},
				},
			},
		},
		want: apis.ErrDisallowedFields("livenessProbe.tcpSocket.port"),
	}, {
		name: "has numerous problems",
		c: corev1.Container{
			Name: "foo",
			VolumeMounts: []corev1.VolumeMount{{
				MountPath: "mount/path",
				Name:      "name",
			}},
			Lifecycle: &corev1.Lifecycle{},
		},
		want: apis.ErrDisallowedFields("name", "volumeMounts", "lifecycle").Also(
			&apis.FieldError{
				Message: "Failed to parse image reference",
				Paths:   []string{"image"},
				Details: "image: \"\", error: could not parse reference",
			},
		),
	}}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := validateContainer(test.c)
			if diff := cmp.Diff(test.want.Error(), got.Error()); diff != "" {
				t.Errorf("validateContainer (-want, +got) = %v", diff)
			}
		})
	}
}

func TestBuildRefValidation(t *testing.T) {
	tests := []struct {
		name string
		r    *corev1.ObjectReference
		want *apis.FieldError
	}{{
		name: "nil",
	}, {
		name: "no api version",
		r:    &corev1.ObjectReference{},
		want: apis.ErrInvalidValue("", "apiVersion"),
	}, {
		name: "bad api version",
		r: &corev1.ObjectReference{
			APIVersion: "/v1alpha1",
		},
		want: apis.ErrInvalidValue("/v1alpha1", "apiVersion"),
	}, {
		name: "no kind",
		r: &corev1.ObjectReference{
			APIVersion: "foo/v1alpha1",
		},
		want: apis.ErrInvalidValue("", "kind"),
	}, {
		name: "bad kind",
		r: &corev1.ObjectReference{
			APIVersion: "foo/v1alpha1",
			Kind:       "Bad Kind",
		},
		want: apis.ErrInvalidValue("Bad Kind", "kind"),
	}, {
		name: "no namespace",
		r: &corev1.ObjectReference{
			APIVersion: "foo.group/v1alpha1",
			Kind:       "Bar",
			Name:       "the-bar-0001",
		},
		want: nil,
	}, {
		name: "no name",
		r: &corev1.ObjectReference{
			APIVersion: "foo.group/v1alpha1",
			Kind:       "Bar",
		},
		want: apis.ErrInvalidValue("", "name"),
	}, {
		name: "bad name",
		r: &corev1.ObjectReference{
			APIVersion: "foo.group/v1alpha1",
			Kind:       "Bar",
			Name:       "bad name",
		},
		want: apis.ErrInvalidValue("bad name", "name"),
	}, {
		name: "disallowed fields",
		r: &corev1.ObjectReference{
			APIVersion: "foo.group/v1alpha1",
			Kind:       "Bar",
			Name:       "bar0001",

			Namespace:       "foo",
			FieldPath:       "some.field.path",
			ResourceVersion: "234234",
			UID:             "deadbeefcafebabe",
		},
		want: apis.ErrDisallowedFields("namespace", "fieldPath", "resourceVersion", "uid"),
	}, {
		name: "all good",
		r: &corev1.ObjectReference{
			APIVersion: "foo.group/v1alpha1",
			Kind:       "Bar",
			Name:       "bar0001",
		},
		want: nil,
	}}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := validateBuildRef(test.r)
			if diff := cmp.Diff(test.want.Error(), got.Error()); diff != "" {
				t.Errorf("validateBuildRef (-want, +got) = %v", diff)
			}
		})
	}
}

func TestConcurrencyModelValidation(t *testing.T) {
	tests := []struct {
		name string
		cm   RevisionRequestConcurrencyModelType
		want *apis.FieldError
	}{{
		name: "single",
		cm:   RevisionRequestConcurrencyModelSingle,
		want: nil,
	}, {
		name: "multi",
		cm:   RevisionRequestConcurrencyModelMulti,
		want: nil,
	}, {
		name: "empty",
		cm:   "",
		want: nil,
	}, {
		name: "bogus",
		cm:   "bogus",
		want: apis.ErrInvalidValue("bogus", apis.CurrentField),
	}, {
		name: "balderdash",
		cm:   "balderdash",
		want: apis.ErrInvalidValue("balderdash", apis.CurrentField),
	}}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := test.cm.Validate()
			if diff := cmp.Diff(test.want.Error(), got.Error()); diff != "" {
				t.Errorf("Validate (-want, +got) = %v", diff)
			}
		})
	}
}

func TestContainerConcurrencyValidation(t *testing.T) {
	tests := []struct {
		name string
		cc   RevisionContainerConcurrencyType
		cm   RevisionRequestConcurrencyModelType
		want *apis.FieldError
	}{{
		name: "single with only container concurrency",
		cc:   1,
		cm:   RevisionRequestConcurrencyModelType(""),
		want: nil,
	}, {
		name: "single with container currency and concurrency model",
		cc:   1,
		cm:   RevisionRequestConcurrencyModelSingle,
		want: nil,
	}, {
		name: "multi with only container concurrency",
		cc:   0,
		cm:   RevisionRequestConcurrencyModelType(""),
		want: nil,
	}, {
		name: "multi with container concurrency and concurrency model",
		cc:   0,
		cm:   RevisionRequestConcurrencyModelMulti,
		want: nil,
	}, {
		name: "mismatching container concurrency (1) and concurrency model (multi)",
		cc:   1,
		cm:   RevisionRequestConcurrencyModelMulti,
		want: apis.ErrMultipleOneOf("containerConcurrency", "concurrencyModel"),
	}, {
		name: "mismatching container concurrency (0) and concurrency model (single)",
		cc:   0,
		cm:   RevisionRequestConcurrencyModelSingle,
		want: apis.ErrMultipleOneOf("containerConcurrency", "concurrencyModel"),
	}, {
		name: "invalid container concurrency (too small)",
		cc:   -1,
		want: apis.ErrInvalidValue("-1", "containerConcurrency"),
	}, {
		name: "invalid container concurrency (too large)",
		cc:   RevisionContainerConcurrencyMax + 1,
		want: apis.ErrInvalidValue(strconv.Itoa(int(RevisionContainerConcurrencyMax)+1), "containerConcurrency"),
	}}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ValidateContainerConcurrency(test.cc, test.cm)
			if diff := cmp.Diff(test.want.Error(), got.Error()); diff != "" {
				t.Errorf("Validate (-want, +got) = %v", diff)
			}
		})
	}
}

func TestRevisionSpecValidation(t *testing.T) {
	tests := []struct {
		name string
		rs   *RevisionSpec
		want *apis.FieldError
	}{{
		name: "valid",
		rs: &RevisionSpec{
			Container: corev1.Container{
				Image: "helloworld",
			},
			ConcurrencyModel: "Multi",
		},
		want: nil,
	}, {
		name: "has bad build ref",
		rs: &RevisionSpec{
			Container: corev1.Container{
				Image: "helloworld",
			},
			BuildRef: &corev1.ObjectReference{},
		},
		want: apis.ErrInvalidValue("", "buildRef.apiVersion"),
	}, {
		name: "bad concurrency model",
		rs: &RevisionSpec{
			Container: corev1.Container{
				Image: "helloworld",
			},
			ConcurrencyModel: "bogus",
		},
		want: apis.ErrInvalidValue("bogus", "concurrencyModel"),
	}, {
		name: "bad container spec",
		rs: &RevisionSpec{
			Container: corev1.Container{
				Name:  "steve",
				Image: "helloworld",
			},
		},
		want: apis.ErrDisallowedFields("container.name"),
	}, {
		name: "exceed max timeout",
		rs: &RevisionSpec{
			Container: corev1.Container{
				Image: "helloworld",
			},
			TimeoutSeconds: 600,
		},
		want: apis.ErrOutOfBoundsValue("600s", "0s",
			fmt.Sprintf("%ds", int(netv1alpha1.DefaultTimeout.Seconds())),
			"timeoutSeconds"),
	}, {
		name: "negative timeout",
		rs: &RevisionSpec{
			Container: corev1.Container{
				Image: "helloworld",
			},
			TimeoutSeconds: -30,
		},
		want: apis.ErrOutOfBoundsValue("-30s", "0s",
			fmt.Sprintf("%ds", int(netv1alpha1.DefaultTimeout.Seconds())),
			"timeoutSeconds"),
	}}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := test.rs.Validate()
			if diff := cmp.Diff(test.want.Error(), got.Error()); diff != "" {
				t.Errorf("Validate (-want, +got) = %v", diff)
			}
		})
	}
}

func TestRevisionTemplateSpecValidation(t *testing.T) {
	tests := []struct {
		name string
		rts  *RevisionTemplateSpec
		want *apis.FieldError
	}{{
		name: "valid",
		rts: &RevisionTemplateSpec{
			Spec: RevisionSpec{
				Container: corev1.Container{
					Image: "helloworld",
				},
				ConcurrencyModel: "Multi",
			},
		},
		want: nil,
	}, {
		name: "empty spec",
		rts:  &RevisionTemplateSpec{},
		want: apis.ErrMissingField("spec"),
	}, {
		name: "nested spec error",
		rts: &RevisionTemplateSpec{
			Spec: RevisionSpec{
				Container: corev1.Container{
					Name:  "kevin",
					Image: "helloworld",
				},
				ConcurrencyModel: "Multi",
			},
		},
		want: apis.ErrDisallowedFields("spec.container.name"),
	}}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := test.rts.Validate()
			if diff := cmp.Diff(test.want.Error(), got.Error()); diff != "" {
				t.Errorf("Validate (-want, +got) = %v", diff)
			}
		})
	}
}

func TestRevisionValidation(t *testing.T) {
	tests := []struct {
		name string
		r    *Revision
		want *apis.FieldError
	}{{
		name: "valid",
		r: &Revision{
			Spec: RevisionSpec{
				Container: corev1.Container{
					Image: "helloworld",
				},
				ConcurrencyModel: "Multi",
			},
		},
		want: nil,
	}, {
		name: "empty spec",
		r:    &Revision{},
		want: apis.ErrMissingField("spec"),
	}, {
		name: "nested spec error",
		r: &Revision{
			Spec: RevisionSpec{
				Container: corev1.Container{
					Name:  "kevin",
					Image: "helloworld",
				},
				ConcurrencyModel: "Multi",
			},
		},
		want: apis.ErrDisallowedFields("spec.container.name"),
	}, {
		name: "invalid name - dots",
		r: &Revision{
			ObjectMeta: metav1.ObjectMeta{
				Name: "do.not.use.dots",
			},
			Spec: RevisionSpec{
				Container: corev1.Container{
					Image: "helloworld",
				},
				ConcurrencyModel: "Multi",
			},
		},
		want: &apis.FieldError{Message: "Invalid resource name: special character . must not be present", Paths: []string{"metadata.name"}},
	}, {
		name: "invalid metadata.annotations - scale bounds",
		r: &Revision{
			ObjectMeta: metav1.ObjectMeta{
				Name: "scale-bounds",
				Annotations: map[string]string{
					autoscaling.MinScaleAnnotationKey: "5",
					autoscaling.MaxScaleAnnotationKey: "2",
				},
			},
			Spec: RevisionSpec{
				Container: corev1.Container{
					Image: "helloworld",
				},
				ConcurrencyModel: "Multi",
			},
		},
		want: (&apis.FieldError{
			Message: fmt.Sprintf("%s=%v is less than %s=%v", autoscaling.MaxScaleAnnotationKey, 2, autoscaling.MinScaleAnnotationKey, 5),
			Paths:   []string{autoscaling.MaxScaleAnnotationKey, autoscaling.MinScaleAnnotationKey},
		}).ViaField("annotations").ViaField("metadata"),
	}, {
		name: "invalid name - too long",
		r: &Revision{
			ObjectMeta: metav1.ObjectMeta{
				Name: strings.Repeat("a", 65),
			},
			Spec: RevisionSpec{
				Container: corev1.Container{
					Image: "helloworld",
				},
				ConcurrencyModel: "Multi",
			},
		},
		want: &apis.FieldError{Message: "Invalid resource name: length must be no more than 63 characters", Paths: []string{"metadata.name"}},
	}}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := test.r.Validate()
			if diff := cmp.Diff(test.want.Error(), got.Error()); diff != "" {
				t.Errorf("Validate (-want, +got) = %v", diff)
			}
		})
	}
}

type notARevision struct{}

func (nar *notARevision) CheckImmutableFields(apis.Immutable) *apis.FieldError {
	return nil
}

func TestImmutableFields(t *testing.T) {
	tests := []struct {
		name string
		new  apis.Immutable
		old  apis.Immutable
		want *apis.FieldError
	}{{
		name: "good (no change)",
		new: &Revision{
			Spec: RevisionSpec{
				Container: corev1.Container{
					Image: "helloworld",
				},
				ConcurrencyModel: "Multi",
			},
		},
		old: &Revision{
			Spec: RevisionSpec{
				Container: corev1.Container{
					Image: "helloworld",
				},
				ConcurrencyModel: "Multi",
			},
		},
		want: nil,
	}, {
		name: "bad (type mismatch)",
		new: &Revision{
			Spec: RevisionSpec{
				Container: corev1.Container{
					Image: "helloworld",
				},
				ConcurrencyModel: "Multi",
			},
		},
		old:  &notARevision{},
		want: &apis.FieldError{Message: "The provided original was not a Revision"},
	}, {
		name: "bad (resources image change)",
		new: &Revision{
			Spec: RevisionSpec{
				Container: corev1.Container{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceName("cpu"): resource.MustParse("50m"),
						},
					},
				},
				ConcurrencyModel: "Multi",
			},
		},
		old: &Revision{
			Spec: RevisionSpec{
				Container: corev1.Container{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceName("cpu"): resource.MustParse("100m"),
						},
					},
				},
				ConcurrencyModel: "Multi",
			},
		},
		want: &apis.FieldError{
			Message: "Immutable fields changed (-old +new)",
			Paths:   []string{"spec"},
			Details: `{v1alpha1.RevisionSpec}.Container.Resources.Requests["cpu"]:
	-: resource.Quantity{i: resource.int64Amount{value: 100, scale: resource.Scale(-3)}, s: "100m", Format: resource.Format("DecimalSI")}
	+: resource.Quantity{i: resource.int64Amount{value: 50, scale: resource.Scale(-3)}, s: "50m", Format: resource.Format("DecimalSI")}
`,
		},
	}, {
		name: "bad (container image change)",
		new: &Revision{
			Spec: RevisionSpec{
				Container: corev1.Container{
					Image: "helloworld",
				},
				ConcurrencyModel: "Multi",
			},
		},
		old: &Revision{
			Spec: RevisionSpec{
				Container: corev1.Container{
					Image: "busybox",
				},
				ConcurrencyModel: "Multi",
			},
		},
		want: &apis.FieldError{
			Message: "Immutable fields changed (-old +new)",
			Paths:   []string{"spec"},
			Details: `{v1alpha1.RevisionSpec}.Container.Image:
	-: "busybox"
	+: "helloworld"
`,
		},
	}, {
		name: "bad (concurrency model change)",
		new: &Revision{
			Spec: RevisionSpec{
				Container: corev1.Container{
					Image: "helloworld",
				},
				ConcurrencyModel: "Multi",
			},
		},
		old: &Revision{
			Spec: RevisionSpec{
				Container: corev1.Container{
					Image: "helloworld",
				},
				ConcurrencyModel: "Single",
			},
		},
		want: &apis.FieldError{
			Message: "Immutable fields changed (-old +new)",
			Paths:   []string{"spec"},
			Details: `{v1alpha1.RevisionSpec}.ConcurrencyModel:
	-: v1alpha1.RevisionRequestConcurrencyModelType("Single")
	+: v1alpha1.RevisionRequestConcurrencyModelType("Multi")
`,
		},
	}, {
		name: "bad (multiple changes)",
		new: &Revision{
			Spec: RevisionSpec{
				Container: corev1.Container{
					Image: "helloworld",
				},
				ConcurrencyModel: "Multi",
			},
		},
		old: &Revision{
			Spec: RevisionSpec{
				Container: corev1.Container{
					Image: "busybox",
				},
				ConcurrencyModel: "Single",
			},
		},
		want: &apis.FieldError{
			Message: "Immutable fields changed (-old +new)",
			Paths:   []string{"spec"},
			Details: `{v1alpha1.RevisionSpec}.ConcurrencyModel:
	-: v1alpha1.RevisionRequestConcurrencyModelType("Single")
	+: v1alpha1.RevisionRequestConcurrencyModelType("Multi")
{v1alpha1.RevisionSpec}.Container.Image:
	-: "busybox"
	+: "helloworld"
`,
		},
	}}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := test.new.CheckImmutableFields(test.old)
			if diff := cmp.Diff(test.want.Error(), got.Error()); diff != "" {
				t.Errorf("Validate (-want, +got) = %v", diff)
			}
		})
	}
}
