package worker_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	extensionscontroller "github.com/gardener/gardener-extensions/pkg/controller"
	"github.com/gardener/gardener-extensions/pkg/controller/worker"
	mockclient "github.com/gardener/gardener-extensions/pkg/mock/controller-runtime/client"
	mockkubernetes "github.com/gardener/gardener-extensions/pkg/mock/gardener/client/kubernetes"
	"github.com/go-openapi/strfmt"
	"github.com/metal-pod/metal-go/api/models"
	cloudmodels "github.com/metal-stack/cloud-go/api/models"
	"github.com/metal-stack/gardener-extension-provider-metal/pkg/apis/config"
	apismetal "github.com/metal-stack/gardener-extension-provider-metal/pkg/apis/metal"

	. "github.com/metal-stack/gardener-extension-provider-metal/pkg/controller/worker"
	"github.com/metal-stack/gardener-extension-provider-metal/pkg/metal"

	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	extensionsv1alpha1 "github.com/gardener/gardener/pkg/apis/extensions/v1alpha1"

	machinev1alpha1 "github.com/gardener/machine-controller-manager/pkg/apis/machine/v1alpha1"

	"github.com/golang/glog"
	"github.com/golang/mock/gomock"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Machines", func() {
	var (
		ctrl         *gomock.Controller
		c            *mockclient.MockClient
		chartApplier *mockkubernetes.MockChartApplier
	)

	BeforeEach(func() {
		ctrl = gomock.NewController(GinkgoT())

		c = mockclient.NewMockClient(ctrl)
		chartApplier = mockkubernetes.NewMockChartApplier(ctrl)
	})

	AfterEach(func() {
		ctrl.Finish()
	})

	go mockMetalApi()

	Context("workerDelegate", func() {
		workerDelegate := NewWorkerDelegate(nil, nil, nil, nil, nil, "", nil, nil)

		Describe("#MachineClassKind", func() {
			It("should return the correct kind of the machine class", func() {
				Expect(workerDelegate.MachineClassKind()).To(Equal("MetalMachineClass"))
			})
		})

		Describe("#MachineClassList", func() {
			It("should return the correct type for the machine class list", func() {
				Expect(workerDelegate.MachineClassList()).To(Equal(&machinev1alpha1.MetalMachineClassList{}))
			})
		})

		Describe("#GenerateMachineDeployments, #DeployMachineClasses", func() {
			var (
				namespace string

				machineImageName    string
				machineImageVersion string

				machineType string
				userData    []byte

				namePool1           string
				minPool1            int
				maxPool1            int
				maxSurgePool1       intstr.IntOrString
				maxUnavailablePool1 intstr.IntOrString

				shootVersionMajorMinor   string
				shootVersion             string
				cidr                     string
				machineImageToAMIMapping []config.MachineImage
				scheme                   *runtime.Scheme
				decoder                  runtime.Decoder
				cluster                  *extensionscontroller.Cluster
				w                        *extensionsv1alpha1.Worker
			)

			BeforeEach(func() {
				namespace = "shoot--foo--bar"

				machineImageName = "my-os"
				machineImageVersion = "123"

				machineType = "large"
				userData = []byte("some-user-data")

				namePool1 = "pool-1"
				minPool1 = 5
				maxPool1 = 10
				maxSurgePool1 = intstr.FromInt(3)
				maxUnavailablePool1 = intstr.FromInt(2)

				shootVersionMajorMinor = "1.2"
				shootVersion = shootVersionMajorMinor + ".3"

				cidr = "10.250.0.0/19"
				machineImageToAMIMapping = []config.MachineImage{
					{
						Name:    machineImageName,
						Version: machineImageVersion,
						Image:   machineImageName + "-" + machineImageVersion,
					},
				}

				cluster = &extensionscontroller.Cluster{
					Shoot: &gardencorev1beta1.Shoot{
						Spec: gardencorev1beta1.ShootSpec{
							Provider: gardencorev1beta1.Provider{
								InfrastructureConfig: &gardencorev1beta1.ProviderConfig{
									runtime.RawExtension{
										Raw: encode(&apismetal.InfrastructureConfig{
											Firewall: apismetal.Firewall{
												Size:     "c1-xlarge-x86",
												Image:    "firewall-1",
												Networks: []string{"internet-nbg-w8101"},
											},
											PartitionID: "my-partition",
											ProjectID:   "project1",
										}),
									},
								},
							},
							Networking: gardencorev1beta1.Networking{
								Pods:  &cidr,
								Nodes: &cidr,
							},
							Kubernetes: gardencorev1beta1.Kubernetes{
								Version: shootVersion,
							},
						},
					},
				}

				w = &extensionsv1alpha1.Worker{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: namespace,
					},
					Spec: extensionsv1alpha1.WorkerSpec{
						SecretRef: corev1.SecretReference{
							Name:      "secret",
							Namespace: namespace,
						},
						Pools: []extensionsv1alpha1.WorkerPool{
							{
								Name:           namePool1,
								Minimum:        minPool1,
								Maximum:        maxPool1,
								MaxSurge:       maxSurgePool1,
								MaxUnavailable: maxUnavailablePool1,
								MachineType:    machineType,
								MachineImage: extensionsv1alpha1.MachineImage{
									Name:    machineImageName,
									Version: machineImageVersion,
								},
								UserData: userData,
							},
						},
					},
				}

				scheme = runtime.NewScheme()
				_ = apismetal.AddToScheme(scheme)

				decoder = serializer.NewCodecFactory(scheme).UniversalDecoder()
				workerDelegate = NewWorkerDelegate(c, scheme, decoder, machineImageToAMIMapping, chartApplier, "", w, cluster)

			})

			It("should return the expected machine deployments", func() {
				expectGetSecretCallToWork(c)

				// Test workerDelegate.DeployMachineClasses()
				var (
					defaultMachineClass = map[string]interface{}{
						"secret": map[string]interface{}{
							"cloudConfig":  string(userData),
							"metalAPIHMac": "my-hmac",
							"metalAPIKey":  "my-key",
							"metalAPIURL":  "http://localhost:8888/",
						},
						"image":     machineImageName + "-" + machineImageVersion,
						"network":   "my-net",
						"labels":    map[string]string{"garden.sapcloud.io/purpose": "machineclass"},
						"partition": "my-partition",
						"project":   "project1",
						"sshkeys":   []string{},
						"size":      machineType,
						"tags": []string{
							"kubernetes.io/cluster=shoot--foo--bar",
							"kubernetes.io/role=node",
							"topology.kubernetes.io/region=",
							"topology.kubernetes.io/zone=my-partition",
							"node.kubernetes.io/instance-type=large",
							"cluster.metal-pod.io/id=",
							"machine.metal-pod.io/project-id=project1",
						},
					}

					machineClassPool1Zone1 = defaultMachineClass

					machineClassNamePool1Zone1 = fmt.Sprintf("%s-%s", namespace, namePool1)
					workerPoolHash1, _         = worker.WorkerPoolHash(w.Spec.Pools[0], cluster)

					machineClassWithHashPool1Zone1 = fmt.Sprintf("%s-%s", machineClassNamePool1Zone1, workerPoolHash1)
				)

				addNameToMachineClass(machineClassPool1Zone1, machineClassWithHashPool1Zone1)

				var machineClasses = map[string]interface{}{"machineClasses": []map[string]interface{}{
					machineClassPool1Zone1,
				}}

				chartApplier.
					EXPECT().
					ApplyChart(
						context.TODO(),
						filepath.Join(metal.InternalChartsPath, "machineclass"),
						namespace,
						"machineclass",
						machineClasses,
						nil,
					)

				err := workerDelegate.DeployMachineClasses(context.TODO())
				Expect(err).NotTo(HaveOccurred())
				// Test workerDelegate.GenerateMachineDeployments()
				machineDeployments := worker.MachineDeployments{
					{
						Name:           machineClassNamePool1Zone1,
						ClassName:      machineClassWithHashPool1Zone1,
						SecretName:     machineClassWithHashPool1Zone1,
						Minimum:        minPool1,
						Maximum:        maxPool1,
						MaxSurge:       maxSurgePool1,
						MaxUnavailable: maxUnavailablePool1,
					},
				}

				result, err := workerDelegate.GenerateMachineDeployments(context.TODO())
				Expect(err).NotTo(HaveOccurred())

				jr, _ := json.Marshal(result)
				jmd, _ := json.Marshal(machineDeployments)
				Expect(jr).To(MatchJSON(jmd))
			})

			It("should fail because the secret cannot be read", func() {
				c.EXPECT().
					Get(context.TODO(), gomock.Any(), gomock.AssignableToTypeOf(&corev1.Secret{})).
					Return(fmt.Errorf("error"))

				result, err := workerDelegate.GenerateMachineDeployments(context.TODO())
				Expect(err).To(HaveOccurred())
				Expect(result).To(BeNil())
			})

			It("should fail because the version is invalid", func() {
				expectGetSecretCallToWork(c)

				cluster.Shoot.Spec.Kubernetes.Version = "invalid"
				workerDelegate = NewWorkerDelegate(c, scheme, decoder, machineImageToAMIMapping, chartApplier, "", w, cluster)

				result, err := workerDelegate.GenerateMachineDeployments(context.TODO())
				Expect(err).To(HaveOccurred())
				Expect(result).To(BeNil())
			})

			It("should fail because the infrastructure status cannot be decoded", func() {
				expectGetSecretCallToWork(c)

				w.Spec.InfrastructureProviderStatus = &runtime.RawExtension{}

				workerDelegate = NewWorkerDelegate(c, scheme, decoder, machineImageToAMIMapping, chartApplier, "", w, cluster)

				result, err := workerDelegate.GenerateMachineDeployments(context.TODO())
				Expect(err).To(HaveOccurred())
				Expect(result).To(BeNil())
			})
		})
	})
})

func encode(obj runtime.Object) []byte {
	data, _ := json.Marshal(obj)
	return data
}

func expectGetSecretCallToWork(c *mockclient.MockClient) {
	c.EXPECT().
		Get(context.TODO(), gomock.Any(), gomock.AssignableToTypeOf(&corev1.Secret{})).
		DoAndReturn(func(_ context.Context, _ client.ObjectKey, secret *corev1.Secret) error {
			secret.Data = map[string][]byte{
				metal.APIHMac: []byte("my-hmac"),
				metal.APIURL:  []byte("http://localhost:8888/"),
				metal.APIKey:  []byte("my-key"),
			}
			return nil
		})
}

func addNameToMachineClass(class map[string]interface{}, name string) {
	class["name"] = name
}

func mockMetalApi() {

	var (
		ID = "my-net"
		f  = false
		i  = int64(64)
		nr = models.V1NetworkResponse{
			Changed:             strfmt.DateTime(time.Now()),
			Created:             strfmt.DateTime(time.Now()),
			ID:                  &ID,
			Destinationprefixes: []string{"10.0.0.0/22"},
			Labels:              map[string]string{"dummy": "label"},
			Privatesuper:        &f,
			Underlay:            &f,
			Parentnetworkid:     &ID,
			Nat:                 &f,
			Usage: &models.V1NetworkUsage{
				AvailableIps:      &i,
				AvailablePrefixes: &i,
				UsedIps:           &i,
				UsedPrefixes:      &i,
			},
		}

		pr1 = cloudmodels.V1ProjectResponse{
			Project: &cloudmodels.V1Project{
				Meta: &cloudmodels.V1Meta{
					ID:          "project1",
					UpdatedTime: strfmt.DateTime(time.Now()),
					CreatedTime: strfmt.DateTime(time.Now()),
				},
				Name:     "project1",
				TenantID: "tenent",
			},
		}
	)

	http.HandleFunc("/v1/network/find", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		resp, err := json.Marshal([]models.V1NetworkResponse{nr})
		if err != nil {
			return
		}
		w.Write(resp)
	})
	http.HandleFunc("/v1/project/project1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		resp, err := json.Marshal(pr1)
		if err != nil {
			return
		}
		w.Write(resp)
	})
	glog.Fatal(http.ListenAndServe(":8888", nil))
}
