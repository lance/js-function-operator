package jsfunction

import (
	"context"
	"fmt"

	kneventing "knative.dev/eventing/pkg/apis/eventing/v1alpha1"
	knv1alpha1 "knative.dev/serving/pkg/apis/serving/v1alpha1"
	knv1beta1 "knative.dev/serving/pkg/apis/serving/v1beta1"

	faasv1alpha1 "github.com/openshift-cloud-functions/js-function-operator/pkg/apis/faas/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	logf "sigs.k8s.io/controller-runtime/pkg/runtime/log"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

var log = logf.Log.WithName("controller_jsfunction")

// Add creates a new JSFunction Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager) error {
	return add(mgr, newReconciler(mgr))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) reconcile.Reconciler {
	return &ReconcileJSFunction{client: mgr.GetClient(), scheme: mgr.GetScheme()}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("jsfunction-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Create the build Task

	// Watch for changes to primary resource JSFunction
	err = c.Watch(&source.Kind{Type: &faasv1alpha1.JSFunction{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	// Watch for changes to secondary resource Service and requeue the owner JSFunction
	err = c.Watch(&source.Kind{Type: &knv1alpha1.Service{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &faasv1alpha1.JSFunction{},
	})
	if err != nil {
		return err
	}

	// Watch for changes to secondary resources KnChanel and KnSubscription and requeue the owner JSFunction
	err = c.Watch(&source.Kind{Type: &kneventing.Channel{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &faasv1alpha1.JSFunction{},
	})
	if err != nil {
		return err
	}
	err = c.Watch(&source.Kind{Type: &kneventing.Subscription{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &faasv1alpha1.JSFunction{},
	})
	if err != nil {
		return err
	}

	return nil
}

// blank assignment to verify that ReconcileJSFunction implements reconcile.Reconciler
var _ reconcile.Reconciler = &ReconcileJSFunction{}

// ReconcileJSFunction reconciles a JSFunction object
type ReconcileJSFunction struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client client.Client
	scheme *runtime.Scheme
}

// Reconcile reads that state of the cluster for a JSFunction object and makes changes based on the state read
// and what is in the JSFunction.Spec
// Note:
// The Controller will requeue the Request to be processed again if the returned error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
func (r *ReconcileJSFunction) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	reqLogger := log.WithValues("Request.Namespace", request.Namespace, "Request.Name", request.Name)
	reqLogger.Info("Reconciling JSFunction.")

	// Fetch the JSFunction instance
	function := &faasv1alpha1.JSFunction{}
	err := r.client.Get(context.TODO(), request.NamespacedName, function)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			reqLogger.Info("Function resource not found. Reconciled object must have been deleted.")
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		reqLogger.Error(err, "Failed to get function. Requeing the request.")
		return reconcile.Result{}, err
	}

	// ConfigMap section start
	configMap := &corev1.ConfigMap{}
	err = r.client.Get(context.TODO(), types.NamespacedName{Name: function.Name, Namespace: function.Namespace}, configMap)
	if err != nil && errors.IsNotFound(err) {
		// No ConfigMap exists yet, create it
		reqLogger.Info("Creating new ConfigMap for function.")
		configMap, err := r.configMapForFunction(function)

		if err = r.client.Create(context.TODO(), configMap); err != nil {
			reqLogger.Error(err, "Cannot create ConfigMap for function")
			return reconcile.Result{}, err
		}
	} else if err != nil {
		return reconcile.Result{}, err
	} else if configMap.Data["index.js"] != function.Spec.Func ||
		configMap.Data["package.json"] != function.Spec.Package {

		// Be sure the ConfigMap is updated with the latest code
		reqLogger.Info("Updating ConfigMap with function changes")
		configMap.Data = mapFunctionData(function)
		err = r.client.Update(context.TODO(), configMap)
		if err != nil {
			return reconcile.Result{}, err
		}

		// Update the build with potential changes from ConfigMap
		reqLogger.Info("Updating JSFunctionBuild for function")
		if err := r.updateFunctionBuild(function); err != nil {
			return reconcile.Result{}, err
		}
	} else if err != nil {
		return reconcile.Result{}, err
	}
	// ConfigMap section end

	// JSFunction build section start
	jsFunctionBuild := &faasv1alpha1.JSFunctionBuild{}
	err = r.client.Get(context.TODO(), types.NamespacedName{Name: function.Name, Namespace: function.Namespace}, jsFunctionBuild)
	if err != nil && errors.IsNotFound(err) {
		// No JSFunctionBuild exists yet, create it
		jsFunctionBuild, err = r.buildForFunction(function, 1)

		reqLogger.Info("Creating new JSFunctionBuild for function")
		if err = r.client.Create(context.TODO(), jsFunctionBuild); err != nil {
			reqLogger.Error(err, "Cannot create JSFunctionBuild")
			return reconcile.Result{}, err
		}
	} else if err != nil {
		return reconcile.Result{}, err
	}
	// JSFunction build section end

	// knative Service section start
	// Check if a Service for this JSFunction already exists, if not create a new one
	knService := &knv1alpha1.Service{}
	err = r.client.Get(context.TODO(), types.NamespacedName{Name: function.Name, Namespace: function.Namespace}, knService)
	if err != nil && errors.IsNotFound(err) {
		// No service for this function exists. Create a new one
		service, err := r.serviceForFunction(function, jsFunctionBuild.Spec.Image)
		if err != nil {
			return reconcile.Result{}, err
		}

		reqLogger.Info("Creating a new knative Service", "Service.Namespace", service.Namespace, "Service.Name", service.Name)
		err = r.client.Create(context.TODO(), service)
		if err != nil {
			reqLogger.Error(err, "Failed to create new Service.", "Service.Namespace", service.Namespace, "Service.Name", service.Name)
			return reconcile.Result{}, err
		}
	} else if err != nil {
		reqLogger.Error(err, "Failed to get Service for JSFunction")
		return reconcile.Result{}, err
	}
	// knative Service section end

	/////// Knative Eventing section
	// Create or delete Channel
	knChannel := &kneventing.Channel{}
	err = r.client.Get(context.TODO(), types.NamespacedName{Name: function.Name, Namespace: function.Namespace}, knChannel)
	if err != nil && errors.IsNotFound(err) {
		if function.Spec.Events {
			// Create channel
			reqLogger.Info("Creating new knative Channel")
			channel, err := r.channelForFunction(function)
			if err != nil {
				return reconcile.Result{}, err
			}
			err = r.client.Create(context.TODO(), channel)
			if err != nil {
				reqLogger.Error(err, "Failed to create new Channel.", "Channel.Namespace", channel.Namespace, "Channel.Name", channel.Name)
				return reconcile.Result{}, err
			}
		}
	} else {
		if !function.Spec.Events && knChannel.ObjectMeta.DeletionTimestamp == nil {
			err = r.client.Delete(context.TODO(), knChannel)
			if err != nil && !errors.IsNotFound(err) {
				reqLogger.Error(err, "failed to delete Channel")
				return reconcile.Result{}, err
			}
		}
	}

	// Create or delete Subscription
	knSubscription := &kneventing.Subscription{}
	err = r.client.Get(context.TODO(), types.NamespacedName{Name: function.Name, Namespace: function.Namespace}, knSubscription)
	if err != nil && errors.IsNotFound(err) {
		if function.Spec.Events {
			// Create subscription
			reqLogger.Info("Creating new knative Subscription")
			subscription, err := r.subscriptionForFunction(function)
			if err != nil {
				return reconcile.Result{}, err
			}
			err = r.client.Create(context.TODO(), subscription)
			if err != nil {
				reqLogger.Error(err, "Failed to create new Subscription.", "Subscription.Namespace", subscription.Namespace, "Subscription.Name", subscription.Name)
				return reconcile.Result{}, err
			}
		}
	} else {
		if !function.Spec.Events && knSubscription.ObjectMeta.DeletionTimestamp == nil {
			err = r.client.Delete(context.TODO(), knSubscription)
			if err != nil && !errors.IsNotFound(err) {
				reqLogger.Error(err, "failed to delete Subscription")
				return reconcile.Result{}, err
			}
		}

	}
	///////
	return reconcile.Result{}, nil
}

func (r *ReconcileJSFunction) updateFunctionBuild(f *faasv1alpha1.JSFunction) error {
	jsFunctionBuild := &faasv1alpha1.JSFunctionBuild{}
	if err := r.client.Get(context.TODO(), types.NamespacedName{Name: f.Name, Namespace: f.Namespace}, jsFunctionBuild); err != nil {
		return err
	}
	revision := jsFunctionBuild.Spec.Revision + 1
	jsFunctionBuild.Spec.Image = runtimeImageForFunction(f, revision)
	jsFunctionBuild.Spec.Revision = revision
	if err := r.client.Update(context.TODO(), jsFunctionBuild); err != nil {
		return err
	}

	return nil
}

func (r *ReconcileJSFunction) configMapForFunction(f *faasv1alpha1.JSFunction) (*corev1.ConfigMap, error) {
	// Create configmap for function code and package.json
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      f.Name,
			Namespace: f.Namespace,
		},
		Data: mapFunctionData(f),
	}
	if err := controllerutil.SetControllerReference(f, configMap, r.scheme); err != nil {
		return nil, err
	}
	return configMap, nil
}

func (r *ReconcileJSFunction) buildForFunction(f *faasv1alpha1.JSFunction, revision int32) (*faasv1alpha1.JSFunctionBuild, error) {
	build := &faasv1alpha1.JSFunctionBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name:      f.Name,
			Namespace: f.Namespace,
		},
		Spec: faasv1alpha1.JSFunctionBuildSpec{
			Revision: revision,
			Image:    runtimeImageForFunction(f, revision),
		},
	}

	if err := controllerutil.SetControllerReference(f, build, r.scheme); err != nil {
		return nil, err
	}
	return build, nil
}

func (r *ReconcileJSFunction) serviceForFunction(f *faasv1alpha1.JSFunction, imageName string) (*knv1alpha1.Service, error) {
	service := &knv1alpha1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      f.Name,
			Namespace: f.Namespace,
		},
		Spec: knv1alpha1.ServiceSpec{
			ConfigurationSpec: knv1alpha1.ConfigurationSpec{
				Template: &knv1alpha1.RevisionTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{"sidecar.istio.io/inject": "false"},
					},
					Spec: knv1alpha1.RevisionSpec{
						RevisionSpec: knv1beta1.RevisionSpec{
							PodSpec: corev1.PodSpec{
								Containers: []corev1.Container{{
									Image: fmt.Sprintf("%s", imageName),
									Name:  fmt.Sprintf("nodejs-%s", f.Name),
									Ports: []corev1.ContainerPort{{
										ContainerPort: 8080,
									}},
								}},
							},
						},
					},
				},
			},
			RouteSpec: knv1alpha1.RouteSpec{},
		},
	}

	// Set JSFunction instance as the owner and controller
	if err := controllerutil.SetControllerReference(f, service, r.scheme); err != nil {
		return nil, err
	}

	return service, nil
}

func (r *ReconcileJSFunction) channelForFunction(f *faasv1alpha1.JSFunction) (*kneventing.Channel, error) {

	channel := &kneventing.Channel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      f.Name,
			Namespace: f.Namespace,
		},
		Spec: kneventing.ChannelSpec{
			Provisioner: &corev1.ObjectReference{
				Name:       "in-memory",
				Kind:       "InMemoryChannel",
				APIVersion: kneventing.SchemeGroupVersion.String(),
			},
		},
	}

	// Set JSFunction instance as the owner and controller
	if err := controllerutil.SetControllerReference(f, channel, r.scheme); err != nil {
		return nil, err
	}
	return channel, nil
}

func (r *ReconcileJSFunction) subscriptionForFunction(f *faasv1alpha1.JSFunction) (*kneventing.Subscription, error) {
	subscription := &kneventing.Subscription{
		ObjectMeta: metav1.ObjectMeta{
			Name:      f.Name,
			Namespace: f.Namespace,
		},

		Spec: kneventing.SubscriptionSpec{
			Channel: corev1.ObjectReference{
				Name:       f.Name,
				Kind:       "Channel",
				APIVersion: kneventing.SchemeGroupVersion.String(),
			},
			Subscriber: &kneventing.SubscriberSpec{
				Ref: &corev1.ObjectReference{
					Name:       f.Name,
					Kind:       "Service",
					APIVersion: knv1alpha1.SchemeGroupVersion.String(),
				},
			},
		},
	}

	// Set JSFunction instance as the owner and controller
	if err := controllerutil.SetControllerReference(f, subscription, r.scheme); err != nil {
		return nil, err
	}

	return subscription, nil
}

func mapFunctionData(f *faasv1alpha1.JSFunction) map[string]string {
	data := map[string]string{"index.js": f.Spec.Func}

	if f.Spec.Package != "" {
		data["package.json"] = f.Spec.Package
	}
	return data
}

func runtimeImageForFunction(f *faasv1alpha1.JSFunction, revision int32) string {
	return fmt.Sprintf("image-registry.openshift-image-registry.svc:5000/%s/%s-runtime:v%d", f.Namespace, f.Name, revision)
}
