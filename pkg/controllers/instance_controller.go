package controllers

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	architectv1alpha1 "github.com/loopholelabs/architekt/pkg/api/k8s/v1alpha1"
	iclient "github.com/loopholelabs/architekt/pkg/client"
)

const (
	InstanceStateCreating = "creating"
	InstanceStateRunning  = "running"
)

type InstanceReconciler struct {
	client   client.Client
	scheme   *runtime.Scheme
	recorder record.EventRecorder

	managerRESTClient iclient.ManagerRESTClient
}

func NewInstanceReconciler(
	client client.Client,
	scheme *runtime.Scheme,
	recorder record.EventRecorder,

	managerRESTClient iclient.ManagerRESTClient,
) *InstanceReconciler {
	return &InstanceReconciler{
		client:   client,
		scheme:   scheme,
		recorder: recorder,

		managerRESTClient: managerRESTClient,
	}
}

// See: https://book.kubebuilder.io/reference/markers.html
// +kubebuilder:rbac:groups=io.loopholelabs.architekt,resources=instances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=io.loopholelabs.architekt,resources=instances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=io.loopholelabs.architekt,resources=instances/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
func (r *InstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	instance := &architectv1alpha1.Instance{}
	if err := r.client.Get(ctx, req.NamespacedName, instance); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Could not find instance, skipping since object must have been deleted", "packageRaddr", instance.Spec.PackageRaddr, "nodeName", instance.Spec.NodeName)

			return ctrl.Result{}, nil
		}

		log.Error(err, "Could not get instance, retrying", "packageRaddr", instance.Spec.PackageRaddr, "nodeName", instance.Spec.NodeName)

		return ctrl.Result{}, err
	}

	if instance.Status.State == InstanceStateCreating {
		log.Info("Instance creation already in progress, skipping", "packageRaddr", instance.Spec.PackageRaddr, "nodeName", instance.Spec.NodeName)

		return ctrl.Result{}, nil
	}

	if instance.Status.State == InstanceStateRunning {
		log.Info("Instance already exists, skipping", "packageRaddr", instance.Spec.PackageRaddr, "nodeName", instance.Spec.NodeName)

		return ctrl.Result{}, nil
	}

	log.Info("Creating instance", "packageRaddr", instance.Spec.PackageRaddr, "nodeName", instance.Spec.NodeName)

	instance.Status.State = InstanceStateCreating

	if err := r.client.Status().Update(ctx, instance); err != nil {
		log.Error(err, "Could not update Instance status, retrying", "packageRaddr", instance.Spec.PackageRaddr, "nodeName", instance.Spec.NodeName)

		return ctrl.Result{}, err
	}

	outputPackageRaddr, err := r.managerRESTClient.CreateInstance(instance.Spec.NodeName, instance.Spec.PackageRaddr)
	if err != nil {
		log.Error(err, "Could not create instance, retrying", "packageRaddr", instance.Spec.PackageRaddr, "nodeName", instance.Spec.NodeName)

		return ctrl.Result{}, err
	}

	instance.Status.PackageRaddr = outputPackageRaddr
	instance.Status.NodeName = instance.Spec.NodeName
	instance.Status.State = InstanceStateRunning

	if err := r.client.Status().Update(ctx, instance); err != nil {
		log.Error(err, "Could not update Instance status, retrying", "packageRaddr", instance.Spec.PackageRaddr, "nodeName", instance.Spec.NodeName)

		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *InstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&architectv1alpha1.Instance{}).
		Owns(&appsv1.Deployment{}).
		Complete(r)
}
