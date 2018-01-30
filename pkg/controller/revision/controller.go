/*
Copyright 2017 The Kubernetes Authors.

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

package revision

import (
	"fmt"
	"log"
	"time"

	"github.com/golang/glog"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"

	"github.com/google/elafros/pkg/apis/ela/v1alpha1"
	clientset "github.com/google/elafros/pkg/client/clientset/versioned"
	elascheme "github.com/google/elafros/pkg/client/clientset/versioned/scheme"
	informers "github.com/google/elafros/pkg/client/informers/externalversions"
	listers "github.com/google/elafros/pkg/client/listers/ela/v1alpha1"
	"github.com/google/elafros/pkg/controller"
	"github.com/google/elafros/pkg/controller/util"
)

var controllerKind = v1alpha1.SchemeGroupVersion.WithKind("ElaDeployment")

const (
	elaServiceLabel string = "elaservice"
	elaVersionLabel string = "revision"

	elaContainerName string = "ela-container"
	elaPortName      string = "ela-port"

	elaContainerLogVolumeName      string = "ela-logs"
	elaContainerLogVolumeMountPath string = "/var/log/app_engine"

	nginxContainerName string = "nginx-proxy"
	nginxSidecarImage  string = "gcr.io/google_appengine/nginx-proxy:latest"
	nginxHttpPortName  string = "nginx-http-port"

	nginxConfigMountPath    string = "/tmp/nginx"
	nginxLogVolumeName      string = "nginx-logs"
	nginxLogVolumeMountPath string = "/var/log/nginx"

	fluentdContainerName string = "fluentd-logger"
	fluentdSidecarImage  string = "gcr.io/google_appengine/fluentd-logger:latest"

	requestQueueContainerName string = "request-queue"
	requestQueuePortName      string = "queue-port"
)

const controllerAgentName = "revision-controller"

var elaPodReplicaCount = int32(2)
var elaPodMaxUnavailable = intstr.IntOrString{Type: intstr.Int, IntVal: 1}
var elaPodMaxSurge = intstr.IntOrString{Type: intstr.Int, IntVal: 1}
var elaPort = 8080
var nginxHttpPort = 8180
var requestQueuePort = 8012

// Helper to make sure we log error messages returned by Reconcile().
func printErr(err error) error {
	if err != nil {
		log.Printf("Logging error: %s", err)
	}
	return err
}

// +controller:group=ela,version=v1alpha1,kind=Revision,resource=revisions
type RevisionControllerImpl struct {
	// kubeClient allows us to talk to the k8s for core APIs
	kubeclientset kubernetes.Interface

	// elaClient allows us to configure Ela objects
	elaclientset clientset.Interface

	// lister indexes properties about Revision
	lister listers.RevisionLister
	synced cache.InformerSynced

	// workqueue is a rate limited work queue. This is used to queue work to be
	// processed instead of performing it as soon as a change happens. This
	// means we can ensure we only process a fixed amount of resources at a
	// time, and makes it easy to ensure we are never processing the same item
	// simultaneously in two different workers.
	workqueue workqueue.RateLimitingInterface
	// recorder is an event recorder for recording Event resources to the
	// Kubernetes API.
	recorder record.EventRecorder
}

// Init initializes the controller and is called by the generated code
// Registers eventhandlers to enqueue events
// config - client configuration for talking to the apiserver
// si - informer factory shared across all controllers for listening to events and indexing resource properties
// queue - message queue for handling new events.  unique to this controller.

//TODO(vaikas): somewhat generic (generic behavior)
func NewController(
	kubeclientset kubernetes.Interface,
	elaclientset clientset.Interface,
	kubeInformerFactory kubeinformers.SharedInformerFactory,
	elaInformerFactory informers.SharedInformerFactory,
	config *rest.Config) controller.Interface {

	// obtain a reference to a shared index informer for the Revision type.
	informer := elaInformerFactory.Elafros().V1alpha1().Revisions()

	// Create event broadcaster
	// Add ela types to the default Kubernetes Scheme so Events can be
	// logged for ela types.
	elascheme.AddToScheme(scheme.Scheme)
	glog.V(4).Info("Creating event broadcaster")
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(glog.Infof)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: kubeclientset.CoreV1().Events("")})
	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: controllerAgentName})

	controller := &RevisionControllerImpl{
		kubeclientset:  kubeclientset,
		elaclientset: elaclientset,
		lister:         informer.Lister(),
		synced:         informer.Informer().HasSynced,
		workqueue:      workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "Revisions"),
		recorder:       recorder,
	}

	glog.Info("Setting up event handlers")
	// Set up an event handler for when Revision resources change
	informer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.enqueueRevision,
		UpdateFunc: func(old, new interface{}) {
			controller.enqueueRevision(new)
		},
	})

	return controller
}

// Run will set up the event handlers for types we are interested in, as well
// as syncing informer caches and starting workers. It will block until stopCh
// is closed, at which point it will shutdown the workqueue and wait for
// workers to finish processing their current work items.
//TODO(grantr): generic
func (c *RevisionControllerImpl) Run(threadiness int, stopCh <-chan struct{}) error {
	defer runtime.HandleCrash()
	defer c.workqueue.ShutDown()

	// Start the informer factories to begin populating the informer caches
	glog.Info("Starting Revision controller")

	// Wait for the caches to be synced before starting workers
	glog.Info("Waiting for informer caches to sync")
	if ok := cache.WaitForCacheSync(stopCh, c.synced); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	glog.Info("Starting workers")
	// Launch threadiness workers to process resources
	for i := 0; i < threadiness; i++ {
		go wait.Until(c.runWorker, time.Second, stopCh)
	}

	glog.Info("Started workers")
	<-stopCh
	glog.Info("Shutting down workers")

	return nil
}

// runWorker is a long-running function that will continually call the
// processNextWorkItem function in order to read and process a message on the
// workqueue.
//TODO(grantr): generic
func (c *RevisionControllerImpl) runWorker() {
	for c.processNextWorkItem() {
	}
}

// processNextWorkItem will read a single work item off the workqueue and
// attempt to process it, by calling the syncHandler.
//TODO(vaikas): generic
func (c *RevisionControllerImpl) processNextWorkItem() bool {
	obj, shutdown := c.workqueue.Get()

	if shutdown {
		return false
	}

	// We wrap this block in a func so we can defer c.workqueue.Done.
	err := func(obj interface{}) error {
		// We call Done here so the workqueue knows we have finished
		// processing this item. We also must remember to call Forget if we
		// do not want this work item being re-queued. For example, we do
		// not call Forget if a transient error occurs, instead the item is
		// put back on the workqueue and attempted again after a back-off
		// period.
		defer c.workqueue.Done(obj)
		var key string
		var ok bool
		// We expect strings to come off the workqueue. These are of the
		// form namespace/name. We do this as the delayed nature of the
		// workqueue means the items in the informer cache may actually be
		// more up to date that when the item was initially put onto the
		// workqueue.
		if key, ok = obj.(string); !ok {
			// As the item in the workqueue is actually invalid, we call
			// Forget here else we'd go into a loop of attempting to
			// process a work item that is invalid.
			c.workqueue.Forget(obj)
			runtime.HandleError(fmt.Errorf("expected string in workqueue but got %#v", obj))
			return nil
		}
		// Run the syncHandler, passing it the namespace/name string of the
		// Foo resource to be synced.
		if err := c.syncHandler(key); err != nil {
			return fmt.Errorf("error syncing '%s': %s", key, err.Error())
		}
		// Finally, if no error occurs we Forget this item so it does not
		// get queued again until another change happens.
		c.workqueue.Forget(obj)
		glog.Infof("Successfully synced '%s'", key)
		return nil
	}(obj)

	if err != nil {
		runtime.HandleError(err)
		return true
	}

	return true
}

// enqueueRevision takes a Revision resource and
// converts it into a namespace/name string which is then put onto the work
// queue. This method should *not* be passed resources of any type other than
// Revision.
//TODO(grantr): generic
func (c *RevisionControllerImpl) enqueueRevision(obj interface{}) {
	var key string
	var err error
	if key, err = cache.MetaNamespaceKeyFunc(obj); err != nil {
		runtime.HandleError(err)
		return
	}
	c.workqueue.AddRateLimited(key)
}

// syncHandler compares the actual state with the desired, and attempts to
// converge the two. It then updates the Status block of the Foo resource
// with the current status of the resource.
//TODO(grantr): not generic
func (c *RevisionControllerImpl) syncHandler(key string) error {
	// Convert the namespace/name string into a distinct namespace and name
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		runtime.HandleError(fmt.Errorf("invalid resource key: %s", key))
		return nil
	}
	log.Printf("Running reconcile Revision for %q:%q\n", namespace, name)

	// Get the Revision resource with this namespace/name
	rev, err := c.lister.Revisions(namespace).Get(name)
	if err != nil {
		// The resource may no longer exist, in which case we stop
		// processing.
		if errors.IsNotFound(err) {
			runtime.HandleError(fmt.Errorf("revision '%s' in work queue no longer exists", key))
			return nil
		}
		return err
	}

	ns, err := util.GetOrCreateRevisionNamespace(namespace, c.kubeclientset)
	if err != nil {
		log.Printf("Failed to create namespace: %s", err)
		panic("Failed to create namespace")
	}
	log.Printf("Namespace %q validated to exist, moving on", ns)

	return printErr(c.reconcileWithImage(rev, namespace))
}

// reconcileWithImage handles enqueued messages that have an image.
func (c *RevisionControllerImpl) reconcileWithImage(u *v1alpha1.Revision, ns string) error {
	accessor, err := meta.Accessor(u)
	if err != nil {
		log.Printf("Failed to get metadata: %s", err)
		panic("Failed to get metadata")
	}

	deletionTimestamp := accessor.GetDeletionTimestamp()
	log.Printf("Check the deletionTimestamp: %s\n", deletionTimestamp)

	elaNS := util.GetElaNamespaceName(u.Namespace)
	if deletionTimestamp == nil {
		log.Printf("Creating or reconciling resources for %s\n", u.Name)
		return c.createK8SResources(u, elaNS)
	} else {
		return c.deleteK8SResources(u, elaNS)
	}
	return nil
}

func (c *RevisionControllerImpl) deleteK8SResources(u *v1alpha1.Revision, ns string) error {
	log.Printf("Deleting the resources for %s\n", u.Name)
	err := c.deleteDeployment(u, ns)
	if err != nil {
		log.Printf("Failed to delete a deployment: %s", err)
	}
	log.Printf("Deleted deployment")

	err = c.deleteAutoscaler(u, ns)
	if err != nil {
		log.Printf("Failed to delete autoscaler: %s", err)
	}
	log.Printf("Deleted autoscaler")

	err = c.deleteNginxConfig(u, ns)
	if err != nil {
		log.Printf("Failed to delete configmap: %s", err)
	}
	log.Printf("Deleted nginx configmap")

	err = c.deleteService(u, ns)
	if err != nil {
		log.Printf("Failed to delete k8s service: %s", err)
	}
	log.Printf("Deleted service")

	// And the deployment is no longer ready, so update that
	u.Status.Conditions = []v1alpha1.RevisionCondition{
		{
			Type:   "Ready",
			Status: "False",
			Reason: "Inactive",
		},
	}
	log.Printf("2. Updating status with the following conditions %+v", u.Status.Conditions)
	if _, err := c.updateStatus(u); err != nil {
		log.Printf("Error recording build completion: %s", err)
		return err
	}

	return nil
}

func (c *RevisionControllerImpl) createK8SResources(u *v1alpha1.Revision, ns string) error {
	// Fire off a Deployment..
	err := c.reconcileDeployment(u, ns)
	if err != nil {
		log.Printf("Failed to create a deployment: %s", err)
		return err
	}

	// Autoscale the service
	err = c.reconcileAutoscaler(u, ns)
	if err != nil {
		log.Printf("Failed to create autoscaler: %s", err)
	}

	// Create nginx config
	err = c.reconcileNginxConfig(u, ns)
	if err != nil {
		log.Printf("Failed to create nginx configmap: %s", err)
	}

	// Create k8s service
	serviceName, err := c.reconcileService(u, ns)
	if err != nil {
		log.Printf("Failed to create k8s service: %s", err)
	} else {
		u.Status.ServiceName = serviceName
	}

	// By updating our deployment status we will trigger a Reconcile()
	// that will watch for Deployment completion.
	u.Status.Conditions = []v1alpha1.RevisionCondition{
		{
			Type:   "Ready",
			Status: "False",
			Reason: "Deploying",
		},
	}
	log.Printf("2. Updating status with the following conditions %+v", u.Status.Conditions)
	if _, err := c.updateStatus(u); err != nil {
		log.Printf("Error recording build completion: %s", err)
		return err
	}

	return nil
}

func (c *RevisionControllerImpl) deleteDeployment(u *v1alpha1.Revision, ns string) error {
	deploymentName := util.GetRevisionDeploymentName(u)
	dc := c.kubeclientset.ExtensionsV1beta1().Deployments(ns)
	_, err := dc.Get(deploymentName, metav1.GetOptions{})
	if err != nil && apierrs.IsNotFound(err) {
		return nil
	}

	log.Printf("Deleting Deployment %q", deploymentName)
	tmp := metav1.DeletePropagationForeground
	err = dc.Delete(deploymentName, &metav1.DeleteOptions{
		PropagationPolicy: &tmp,
	})
	if err != nil && !apierrs.IsNotFound(err) {
		log.Printf("deployments.Delete for %q failed: %s", deploymentName, err)
		return err
	}
	return nil
}

func (c *RevisionControllerImpl) reconcileDeployment(u *v1alpha1.Revision, ns string) error {
	//TODO(grantr): migrate this to AppsV1 when it goes GA. See
	// https://kubernetes.io/docs/reference/workloads-18-19.
	dc := c.kubeclientset.ExtensionsV1beta1().Deployments(ns)

	// First, check if deployment exists already.
	deploymentName := util.GetRevisionDeploymentName(u)
	_, err := dc.Get(deploymentName, metav1.GetOptions{})
	if err != nil {
		if !apierrs.IsNotFound(err) {
			log.Printf("deployments.Get for %q failed: %s", deploymentName, err)
			return err
		}
		log.Printf("Deployment %q doesn't exist, creating", deploymentName)
	} else {
		log.Printf("Found existing deployment %q", deploymentName)
		return nil
	}

	// Create the deployment.
	controllerRef := metav1.NewControllerRef(u, controllerKind)
	// Create a single pod so that it gets created before deployment->RS to try to speed
	// things up
	podSpec := MakeElaPodSpec(u)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      util.GetRevisionPodName(u),
			Namespace: ns,
		},
		Spec: *podSpec,
	}
	pod.OwnerReferences = append(pod.OwnerReferences, *controllerRef)
	pc := c.kubeclientset.Core().Pods(ns)
	_, err = pc.Create(pod)
	if err != nil {
		// It's fine if this doesn't work because deployment creates things
		// below, just slower.
		log.Printf("Failed to create pod: %s", err)
	}
	deployment := MakeElaDeployment(u, ns)
	deployment.OwnerReferences = append(deployment.OwnerReferences, *controllerRef)
	deployment.Spec.Template.Spec = *podSpec

	log.Printf("Creating Deployment: %q", deployment.Name)
	_, createErr := dc.Create(deployment)
	return createErr
}

func (c *RevisionControllerImpl) deleteNginxConfig(u *v1alpha1.Revision, ns string) error {
	configMapName := util.GetRevisionNginxConfigMapName(u)
	cmc := c.kubeclientset.Core().ConfigMaps(ns)
	_, err := cmc.Get(configMapName, metav1.GetOptions{})
	if err != nil && apierrs.IsNotFound(err) {
		return nil
	}

	log.Printf("Deleting configmap %q", configMapName)
	tmp := metav1.DeletePropagationForeground
	err = cmc.Delete(configMapName, &metav1.DeleteOptions{
		PropagationPolicy: &tmp,
	})
	if err != nil && !apierrs.IsNotFound(err) {
		log.Printf("configMap.Delete for %q failed: %s", configMapName, err)
		return err
	}
	return nil
}

func (c *RevisionControllerImpl) reconcileNginxConfig(u *v1alpha1.Revision, ns string) error {
	cmc := c.kubeclientset.Core().ConfigMaps(ns)
	configMapName := util.GetRevisionNginxConfigMapName(u)
	_, err := cmc.Get(configMapName, metav1.GetOptions{})
	if err != nil {
		if !apierrs.IsNotFound(err) {
			log.Printf("configmaps.Get for %q failed: %s", configMapName, err)
			return err
		}
		log.Printf("ConfigMap %q doesn't exist, creating", configMapName)
	} else {
		log.Printf("Found existing ConfigMap %q", configMapName)
		return nil
	}

	controllerRef := metav1.NewControllerRef(u, controllerKind)
	configMap := MakeNginxConfigMap(u, ns)
	configMap.OwnerReferences = append(configMap.OwnerReferences, *controllerRef)
	log.Printf("Creating configmap: %q", configMap.Name)
	_, err = cmc.Create(configMap)
	return err
}

func (c *RevisionControllerImpl) deleteService(u *v1alpha1.Revision, ns string) error {
	sc := c.kubeclientset.Core().Services(ns)
	serviceName := util.GetElaK8SServiceNameForRevision(u)

	log.Printf("Deleting service %q", serviceName)
	tmp := metav1.DeletePropagationForeground
	err := sc.Delete(serviceName, &metav1.DeleteOptions{
		PropagationPolicy: &tmp,
	})
	if err != nil && !apierrs.IsNotFound(err) {
		log.Printf("service.Delete for %q failed: %s", serviceName, err)
		return err
	}
	return nil
}

func (c *RevisionControllerImpl) reconcileService(u *v1alpha1.Revision, ns string) (string, error) {
	sc := c.kubeclientset.Core().Services(ns)
	serviceName := util.GetElaK8SServiceNameForRevision(u)
	_, err := sc.Get(serviceName, metav1.GetOptions{})
	if err != nil {
		if !apierrs.IsNotFound(err) {
			log.Printf("services.Get for %q failed: %s", serviceName, err)
			return "", err
		}
		log.Printf("serviceName %q doesn't exist, creating", serviceName)
	} else {
		// TODO(vaikas): Check that the service is legit and matches what we expect
		// to have there.
		log.Printf("Found existing service %q", serviceName)
		return serviceName, nil
	}

	controllerRef := metav1.NewControllerRef(u, controllerKind)
	service := MakeRevisionK8sService(u, ns)
	service.OwnerReferences = append(service.OwnerReferences, *controllerRef)
	log.Printf("Creating service: %q", service.Name)
	_, err = sc.Create(service)
	return serviceName, err
}

func (c *RevisionControllerImpl) deleteAutoscaler(u *v1alpha1.Revision, ns string) error {
	autoscalerName := util.GetRevisionAutoscalerName(u)
	hpas := c.kubeclientset.AutoscalingV1().HorizontalPodAutoscalers(ns)
	_, err := hpas.Get(autoscalerName, metav1.GetOptions{})
	if err != nil && apierrs.IsNotFound(err) {
		return nil
	}

	log.Printf("Deleting autoscaler %q", autoscalerName)
	tmp := metav1.DeletePropagationForeground
	err = hpas.Delete(autoscalerName, &metav1.DeleteOptions{
		PropagationPolicy: &tmp,
	})
	if err != nil && !apierrs.IsNotFound(err) {
		log.Printf("autoscaler.Delete for %q failed: %s", autoscalerName, err)
		return err
	}
	return nil

}

func (c *RevisionControllerImpl) reconcileAutoscaler(u *v1alpha1.Revision, ns string) error {
	autoscalerName := util.GetRevisionAutoscalerName(u)
	hpas := c.kubeclientset.AutoscalingV1().HorizontalPodAutoscalers(ns)

	_, err := hpas.Get(autoscalerName, metav1.GetOptions{})
	if err != nil {
		if !apierrs.IsNotFound(err) {
			log.Printf("autoscaler.Get for %q failed: %s", autoscalerName, err)
			return err
		}
		log.Printf("Autoscaler %q doesn't exist, creating", autoscalerName)
	} else {
		log.Printf("Found existing Autoscaler %q", autoscalerName)
		return nil
	}

	controllerRef := metav1.NewControllerRef(u, controllerKind)
	autoscaler := MakeElaAutoscaler(u, ns)
	autoscaler.OwnerReferences = append(autoscaler.OwnerReferences, *controllerRef)
	log.Printf("Creating autoscaler: %q", autoscaler.Name)
	_, err = hpas.Create(autoscaler)
	return err
}

func (c *RevisionControllerImpl) removeFinalizers(u *v1alpha1.Revision, ns string) error {
	log.Printf("Removing finalizers for %q\n", u.Name)
	accessor, err := meta.Accessor(u)
	if err != nil {
		log.Printf("Failed to get metadata: %s", err)
		panic("Failed to get metadata")
	}
	finalizers := accessor.GetFinalizers()
	for i, v := range finalizers {
		if v == "controller" {
			finalizers = append(finalizers[:i], finalizers[i+1:]...)
		}
	}
	accessor.SetFinalizers(finalizers)
	prClient := c.elaclientset.ElafrosV1alpha1().Revisions(u.Namespace)
	prClient.Update(u)
	log.Printf("The finalizer 'controller' is removed.")

	return nil
}

func (c *RevisionControllerImpl) updateStatus(u *v1alpha1.Revision) (*v1alpha1.Revision, error) {
	prClient := c.elaclientset.ElafrosV1alpha1().Revisions(u.Namespace)
	newu, err := prClient.Get(u.Name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	newu.Status = u.Status

	// TODO: for CRD there's no updatestatus, so use normal update
	return prClient.Update(newu)
	//	return prClient.UpdateStatus(newu)
}